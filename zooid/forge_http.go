package zooid

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/githubforge"
	"zooid/internal/projecttask"
)

const maxForgeAPIRequest = 1 << 20

// ServeForgeAPI is the NIP-98 protected application surface for capability
// discovery and explicit provider administration. Nostr event APIs remain the
// native collaboration path.
func (policy *GitForgePolicy) ServeForgeAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := readForgeBody(w, r)
	if !ok {
		return
	}
	expectedURL, err := policy.publicRequestURL(r)
	if err != nil {
		writeGitError(w, http.StatusBadRequest, "invalid_request_url")
		return
	}
	authEvent, err := gitforge.VerifyRequestAuth(r, expectedURL, body, time.Now())
	if err != nil {
		gitAuthRequired(w, r.Method)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	idempotencyKey, ok := forgeIdempotencyKey(r, authEvent)
	if !ok {
		writeGitError(w, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/forge/")
	switch {
	case r.Method == http.MethodPost && path == "connections/github":
		policy.createGitHubConnection(w, r, authEvent.PubKey, body)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "connections/github/") && strings.HasSuffix(path, "/repositories"):
		policy.listGitHubRepositories(w, r, authEvent.PubKey, path)
	case strings.HasPrefix(path, "repositories/"):
		policy.serveRepositoryForgeAPI(w, r, authEvent.PubKey, idempotencyKey, body)
	case strings.HasPrefix(path, "work-items/"):
		policy.serveWorkItemForgeAPI(w, r, authEvent.PubKey, idempotencyKey, body)
	case strings.HasPrefix(path, "pull-requests/"):
		policy.servePullRequestForgeAPI(w, r, authEvent.PubKey, idempotencyKey, body)
	case strings.HasPrefix(path, "operations/"):
		policy.serveForgeOperationAPI(w, r, authEvent.PubKey, path)
	default:
		http.NotFound(w, r)
	}
}

func forgeIdempotencyKey(r *http.Request, auth nostr.Event) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if value == "" {
		return "nip98:" + auth.ID.Hex(), true
	}
	if len(value) > 200 {
		return "", false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return "", false
		}
	}
	return "client:" + auth.PubKey.Hex() + ":" + r.Method + ":" + r.URL.Path + ":" + value, true
}

func readForgeBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil, true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxForgeAPIRequest+1))
	if err != nil || len(body) > maxForgeAPIRequest {
		writeGitError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return nil, false
	}
	return body, true
}

func (policy *GitForgePolicy) createGitHubConnection(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, body []byte) {
	if !policy.Config.CanManage(actor) {
		writeGitError(w, http.StatusForbidden, "relay_admin_required")
		return
	}
	if policy.GitHub == nil || policy.Credentials == nil {
		writeGitError(w, http.StatusServiceUnavailable, "github_disabled")
		return
	}
	var request struct {
		AppID               string `json:"app_id"`
		InstallationID      int64  `json:"installation_id"`
		CredentialRef       string `json:"credential_ref"`
		WebhookSecretRef    string `json:"webhook_secret_ref"`
		BridgeCredentialRef string `json:"bridge_credential_ref"`
		BridgePubkey        string `json:"bridge_pubkey"`
	}
	if !decodeStrictJSON(body, &request) || request.AppID == "" || request.InstallationID <= 0 {
		writeGitError(w, http.StatusBadRequest, "invalid_github_connection")
		return
	}
	for _, reference := range []string{request.CredentialRef, request.WebhookSecretRef, request.BridgeCredentialRef} {
		if githubforge.ValidateCredentialReference(reference) != nil {
			writeGitError(w, http.StatusBadRequest, "invalid_credential_reference")
			return
		}
	}
	if !policy.validateBridgeCredentials(request.WebhookSecretRef, request.BridgeCredentialRef, request.BridgePubkey) {
		writeGitError(w, http.StatusConflict, "github_credentials_unavailable")
		return
	}
	id, err := newOperationID("ghc")
	if err != nil {
		writeGitError(w, http.StatusInternalServerError, "connection_id_failed")
		return
	}
	connection := githubforge.Connection{
		ID: id, Host: "github.com", AppID: request.AppID, InstallationID: request.InstallationID,
		CredentialRef: request.CredentialRef, WebhookSecretRef: request.WebhookSecretRef,
		BridgeCredentialRef: request.BridgeCredentialRef, BridgePubkey: request.BridgePubkey,
	}
	connection, err = policy.GitHub.ValidateConnection(r.Context(), connection)
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	if err := policy.saveGitHubConnection(r.Context(), connection); err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "connection_store_failed")
		return
	}
	writeForgeJSON(w, http.StatusCreated, publicGitHubConnection(connection))
}

func (policy *GitForgePolicy) validateBridgeCredentials(webhookReference, bridgeReference, bridgePubkey string) bool {
	webhookSecret, err := policy.Credentials.Load(webhookReference)
	if err != nil || len(bytes.TrimSpace(webhookSecret)) == 0 {
		return false
	}
	bridgeSecret, err := policy.Credentials.Load(bridgeReference)
	if err != nil {
		return false
	}
	secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(string(bridgeSecret)))
	if err != nil {
		return false
	}
	pubkey, err := nostr.PubKeyFromHex(bridgePubkey)
	return err == nil && secret.Public() == pubkey
}

func publicGitHubConnection(connection githubforge.Connection) map[string]any {
	return map[string]any{
		"id": connection.ID, "provider": "github", "host": connection.Host,
		"installation_id": connection.InstallationID, "account_id": connection.AccountID,
		"account_login": connection.AccountLogin, "bridge_pubkey": connection.BridgePubkey,
		"status": connection.Status,
	}
}

func (policy *GitForgePolicy) listGitHubRepositories(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, path string) {
	if !policy.Config.CanManage(actor) {
		writeGitError(w, http.StatusForbidden, "relay_admin_required")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, "connections/github/"), "/repositories")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	connection, found, err := policy.LoadGitHubConnection(r.Context(), id)
	if err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "connection_store_failed")
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	repositories, err := policy.GitHub.ListRepositories(r.Context(), connection)
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusOK, map[string]any{"repositories": repositories})
}

func (policy *GitForgePolicy) serveRepositoryForgeAPI(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, idempotencyKey string, body []byte) {
	ownerText, repositoryText, action, ok := splitRepositoryPath(r.URL.Path, "/api/forge/repositories/")
	if !ok || strings.Contains(action, "/") {
		http.NotFound(w, r)
		return
	}
	resolved, status, code := policy.resolvePublicRepository(r.Context(), ownerText, repositoryText)
	if status != 0 {
		writeGitError(w, status, code)
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "capabilities":
		if status, code := policy.authorizeRepository(actor, resolved.Repository, false); status != 0 {
			writeGitError(w, status, code)
			return
		}
		writeForgeJSON(w, http.StatusOK, map[string]any{
			"repository": resolved.Repository.Coordinate.String(), "capabilities": resolved.Capabilities,
		})
	case r.Method == http.MethodGet && action == "health":
		if !policy.Config.CanManage(actor) {
			writeGitError(w, http.StatusForbidden, "relay_admin_required")
			return
		}
		policy.serveRepositoryHealth(w, r, resolved)
	case r.Method == http.MethodPut && action == "integration":
		if !policy.Config.CanManage(actor) {
			writeGitError(w, http.StatusForbidden, "relay_admin_required")
			return
		}
		policy.updateRepositoryIntegration(w, r, resolved.Repository, body)
	case r.Method == http.MethodPost && action == "issues":
		policy.enqueueRepositoryCommand(w, r, actor, idempotencyKey, resolved, "create_issue", "issue", body)
	case r.Method == http.MethodPost && action == "pull-requests":
		policy.enqueueRepositoryCommand(w, r, actor, idempotencyKey, resolved, "create_pull_request", "pull_request", body)
	case r.Method == http.MethodPost && action == "import":
		if !policy.Config.CanManage(actor) {
			writeGitError(w, http.StatusForbidden, "relay_admin_required")
			return
		}
		if len(bytes.TrimSpace(body)) != 0 && string(bytes.TrimSpace(body)) != "{}" {
			writeGitError(w, http.StatusBadRequest, "invalid_import_request")
			return
		}
		policy.enqueueInitialImport(w, r, actor, idempotencyKey, resolved)
	default:
		http.NotFound(w, r)
	}
}

func (policy *GitForgePolicy) enqueueInitialImport(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, idempotencyKey string, resolved forge.ResolvedRepository) {
	if resolved.Collaboration == nil || resolved.Repository.Integration.CollaborationBackend != forge.BackendGitHub {
		writeGitError(w, http.StatusConflict, "provider_collaboration_required")
		return
	}
	if _, ok := resolved.Collaboration.(forge.CollaborationImporter); !ok {
		writeGitError(w, http.StatusConflict, "provider_import_unsupported")
		return
	}
	operationID, err := newOperationID("fop")
	if err != nil {
		writeGitError(w, http.StatusInternalServerError, "operation_id_failed")
		return
	}
	operation, err := policy.enqueueForgeOperation(r.Context(), forgeOperation{
		ID: operationID, Provider: forge.BackendGitHub,
		ConnectionID: resolved.Repository.Integration.ConnectionID,
		Coordinate:   resolved.Repository.Coordinate, Command: "initial_import", ObjectType: "import",
		Actor: actor, Payload: json.RawMessage(`{}`), IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusAccepted, publicForgeOperation(operation))
}

func (policy *GitForgePolicy) enqueueRepositoryCommand(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, idempotencyKey string, resolved forge.ResolvedRepository, command, objectType string, body []byte) {
	if status, code := policy.authorizeRepository(actor, resolved.Repository, true); status != 0 {
		writeGitError(w, status, code)
		return
	}
	if resolved.Collaboration == nil || resolved.Repository.Integration.CollaborationBackend != forge.BackendGitHub {
		writeGitError(w, http.StatusConflict, "provider_collaboration_required")
		return
	}
	var payload map[string]any
	if !decodeStrictJSON(body, &payload) {
		writeGitError(w, http.StatusBadRequest, "invalid_forge_command")
		return
	}
	title := stringValue(payload["title"])
	content := stringValue(payload["body"])
	if !validForgeTitle(title) || len(content) > 64<<10 {
		writeGitError(w, http.StatusBadRequest, "invalid_forge_command")
		return
	}
	if command == "create_pull_request" {
		if !validGitHubRef(stringValue(payload["head"])) || !validGitHubRef(stringValue(payload["base"])) || !fullForgeObjectID(stringValue(payload["head_sha"])) {
			writeGitError(w, http.StatusBadRequest, "invalid_pull_request_command")
			return
		}
		for key := range payload {
			if key != "title" && key != "body" && key != "head" && key != "base" && key != "head_sha" && key != "draft" {
				writeGitError(w, http.StatusBadRequest, "invalid_forge_command")
				return
			}
		}
	} else {
		for key := range payload {
			if key != "title" && key != "body" {
				writeGitError(w, http.StatusBadRequest, "invalid_forge_command")
				return
			}
		}
	}
	encoded, _ := json.Marshal(payload)
	operationID, err := newOperationID("fop")
	if err != nil {
		writeGitError(w, http.StatusInternalServerError, "operation_id_failed")
		return
	}
	operation, err := policy.enqueueForgeOperation(r.Context(), forgeOperation{
		ID: operationID, Provider: forge.BackendGitHub,
		ConnectionID: resolved.Repository.Integration.ConnectionID,
		Coordinate:   resolved.Repository.Coordinate, Command: command, ObjectType: objectType,
		Actor: actor, Payload: encoded, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusAccepted, publicForgeOperation(operation))
}

func (policy *GitForgePolicy) serveWorkItemForgeAPI(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, idempotencyKey string, body []byte) {
	eventID, action, ok := splitObjectAction(strings.TrimPrefix(r.URL.Path, "/api/forge/work-items/"))
	if !ok || r.Method != http.MethodPost || (action != "comments" && action != "state") {
		http.NotFound(w, r)
		return
	}
	root, resolved, mapping, status, code := policy.resolveMappedWorkItem(r.Context(), eventID, actor, true)
	if status != 0 {
		writeGitError(w, status, code)
		return
	}
	var payload map[string]any
	if !decodeStrictJSON(body, &payload) {
		writeGitError(w, http.StatusBadRequest, "invalid_forge_command")
		return
	}
	command := "comment"
	objectType := "comment"
	if action == "comments" {
		if text := stringValue(payload["body"]); text == "" || len(text) > 64<<10 || len(payload) != 1 {
			writeGitError(w, http.StatusBadRequest, "invalid_comment_command")
			return
		}
	} else {
		command, objectType = "state", mapping.ObjectType
		state := strings.ToLower(stringValue(payload["state"]))
		allowed := state == "open" || state == "closed"
		if root.Kind == projecttask.KindPullRequest {
			allowed = allowed || state == "draft" || state == "ready"
		}
		if !allowed || len(payload) != 1 {
			writeGitError(w, http.StatusBadRequest, "invalid_state_command")
			return
		}
		payload["state"] = state
	}
	payload["external_number"] = mapping.ExternalNumber
	payload["object_type"] = mapping.ObjectType
	encoded, _ := json.Marshal(payload)
	operationID, err := newOperationID("fop")
	if err != nil {
		writeGitError(w, http.StatusInternalServerError, "operation_id_failed")
		return
	}
	operation, err := policy.enqueueForgeOperation(r.Context(), forgeOperation{
		ID: operationID, Provider: forge.BackendGitHub,
		ConnectionID: resolved.Repository.Integration.ConnectionID,
		Coordinate:   resolved.Repository.Coordinate, Command: command, ObjectType: objectType,
		Actor: actor, RootEventID: root.ID.Hex(), Payload: encoded, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusAccepted, publicForgeOperation(operation))
}

func (policy *GitForgePolicy) servePullRequestForgeAPI(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, idempotencyKey string, body []byte) {
	eventID, action, ok := splitObjectAction(strings.TrimPrefix(r.URL.Path, "/api/forge/pull-requests/"))
	if !ok || r.Method != http.MethodPost || action != "merge" {
		http.NotFound(w, r)
		return
	}
	root, resolved, mapping, status, code := policy.resolveMappedWorkItem(r.Context(), eventID, actor, true)
	if status != 0 {
		writeGitError(w, status, code)
		return
	}
	if root.Kind != projecttask.KindPullRequest || actor != resolved.Repository.Coordinate.Owner && !policy.Config.CanManage(actor) {
		writeGitError(w, http.StatusForbidden, "merge_not_authorized")
		return
	}
	var request struct {
		ExpectedHead string `json:"expected_head"`
		Method       string `json:"method"`
		Title        string `json:"title"`
		Message      string `json:"message"`
	}
	if !decodeStrictJSON(body, &request) || !fullForgeObjectID(request.ExpectedHead) {
		writeGitError(w, http.StatusBadRequest, "invalid_merge_request")
		return
	}
	state, err := projecttask.ReducePullRequest(root, resolved.Repository.Announcement, policy.pullRequestRelatedEvents(r.Context(), root.ID))
	if err != nil || !strings.EqualFold(state.CurrentCommit, request.ExpectedHead) {
		writeGitError(w, http.StatusConflict, "pull_request_head_changed")
		return
	}
	if request.Method == "" {
		request.Method = "merge"
	}
	if request.Method != "merge" && request.Method != "squash" && request.Method != "rebase" {
		writeGitError(w, http.StatusBadRequest, "invalid_merge_request")
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"external_number": mapping.ExternalNumber, "object_type": mapping.ObjectType,
		"commit_title": request.Title, "commit_message": request.Message, "merge_method": request.Method,
		"state": "merged",
	})
	operationID, err := newOperationID("fop")
	if err != nil {
		writeGitError(w, http.StatusInternalServerError, "operation_id_failed")
		return
	}
	operation, err := policy.enqueueForgeOperation(r.Context(), forgeOperation{
		ID: operationID, Provider: forge.BackendGitHub,
		ConnectionID: resolved.Repository.Integration.ConnectionID,
		Coordinate:   resolved.Repository.Coordinate, Command: "merge", ObjectType: "pull_request",
		Actor: actor, RootEventID: root.ID.Hex(), ExpectedHead: strings.ToLower(request.ExpectedHead),
		Payload: payload, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusAccepted, publicForgeOperation(operation))
}

func (policy *GitForgePolicy) resolveMappedWorkItem(ctx context.Context, rawID string, actor nostr.PubKey, write bool) (nostr.Event, forge.ResolvedRepository, forgeObjectMapping, int, string) {
	id, err := nostr.IDFromHex(rawID)
	if err != nil || id.Hex() != rawID {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusNotFound, "work_item_not_found"
	}
	root, found, err := policy.ProjectTasks.EventByID(ctx, id)
	if err != nil || !found || policy.ProjectTasks.IsTombstoned(ctx, root) || root.Kind != nostr.KindIssue && root.Kind != projecttask.KindPullRequest {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusNotFound, "work_item_not_found"
	}
	coordinate, err := repositoryCoordinateFromEvent(root)
	if err != nil {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusNotFound, "repository_not_found"
	}
	resolved, found, err := policy.ResolveIntegration(ctx, coordinate)
	if err != nil || !found || resolved.Collaboration == nil || resolved.Repository.Integration.CollaborationBackend != forge.BackendGitHub {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusConflict, "provider_collaboration_required"
	}
	if status, code := policy.authorizeWorkItem(ctx, actor, root, resolved.Repository, write); status != 0 {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, status, code
	}
	mapping, found, err := policy.loadMappingByNostrRoot(ctx, forge.BackendGitHub, resolved.Repository.Integration.ConnectionID, root.ID.Hex())
	if err != nil || !found || mapping.ExternalNumber <= 0 {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusConflict, "forge_mapping_missing"
	}
	return root, resolved, mapping, 0, ""
}

func (policy *GitForgePolicy) authorizeWorkItem(ctx context.Context, actor nostr.PubKey, root nostr.Event, repository forge.Repository, write bool) (int, string) {
	if status, code := policy.authorizeRepository(actor, repository, write); status != 0 {
		return status, code
	}
	scope, err := policy.ProjectTasks.resolveStored(ctx, root)
	if err != nil || !scope.Relevant {
		return http.StatusNotFound, "work_item_scope_not_found"
	}
	for _, channel := range scope.Channels {
		metadata, found := policy.Groups.GetMetadata(channel)
		if !found {
			return http.StatusNotFound, "work_item_scope_not_found"
		}
		if !policy.Groups.HasAccess(channel, actor) {
			if HasTag(metadata.Tags, "private") || HasTag(metadata.Tags, "hidden") {
				return http.StatusNotFound, "work_item_not_found"
			}
			return http.StatusForbidden, "work_item_access_denied"
		}
		if write && !policy.Groups.CanWrite(channel, actor) {
			return http.StatusForbidden, "work_item_write_denied"
		}
	}
	return 0, ""
}

func repositoryCoordinateFromEvent(event nostr.Event) (projecttask.RepositoryCoordinate, error) {
	tag := event.Tags.Find("a")
	if tag == nil || len(tag) < 2 {
		return projecttask.RepositoryCoordinate{}, errors.New("missing repository")
	}
	return projecttask.ParseRepositoryCoordinate(tag[1])
}

func splitObjectAction(value string) (string, string, bool) {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 2 || len(parts[0]) != 64 || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func validGitHubRef(value string) bool {
	return value != "" && len(value) <= 1024 && !strings.ContainsAny(value, "\x00\r\n") && !strings.HasPrefix(value, "-")
}

func (policy *GitForgePolicy) serveForgeOperationAPI(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, path string) {
	rest := strings.Trim(strings.TrimPrefix(path, "operations/"), "/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" || strings.Contains(action, "/") {
		http.NotFound(w, r)
		return
	}
	operation, found, err := policy.loadForgeOperation(r.Context(), id)
	if err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "operation_store_unavailable")
		return
	}
	if !found || actor != operation.Actor && !policy.Config.CanManage(actor) {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet && action == "" {
		writeForgeJSON(w, http.StatusOK, publicForgeOperation(operation))
		return
	}
	if r.Method == http.MethodPost && action == "retry" && policy.Config.CanManage(actor) {
		if err := policy.retryForgeOperation(r.Context(), operation.ID); err != nil {
			writeGitBackendFailure(w, err)
			return
		}
		operation, _, _ = policy.loadForgeOperation(r.Context(), operation.ID)
		writeForgeJSON(w, http.StatusAccepted, publicForgeOperation(operation))
		return
	}
	http.NotFound(w, r)
}

func publicForgeOperation(operation forgeOperation) map[string]any {
	result := map[string]any{
		"operation_id": operation.ID, "state": operation.State,
		"command": operation.Command, "repository": operation.Coordinate.String(),
		"created_at": operation.CreatedAt, "updated_at": operation.UpdatedAt,
	}
	if operation.ProviderResult != nil {
		result["result"] = operation.ProviderResult
	}
	if operation.Failure != "" {
		result["failure"] = operation.Failure
	}
	return result
}

func (policy *GitForgePolicy) serveRepositoryHealth(w http.ResponseWriter, r *http.Request, resolved forge.ResolvedRepository) {
	state, detail := resolved.Repository.Integration.HealthState, resolved.Repository.Integration.HealthDetail
	providerRepository := ""
	if resolved.Repository.Integration.GitBackend == forge.BackendGitHub {
		if policy.GitHub == nil {
			state, detail = "disabled", "GitHub adapter is disabled"
		} else {
			metadata, err := policy.GitHub.RepositoryByID(r.Context(), resolved.Repository.Integration.ConnectionID, mustRepositoryID(resolved.Repository.Integration.ExternalRepositoryID))
			if err != nil {
				state, detail = "degraded", err.Error()
			} else {
				providerRepository = metadata.FullName
				if githubAnnouncementMatches(resolved.Repository.Announcement, metadata) {
					state, detail = "ready", ""
				} else {
					state, detail = "degraded", "signed clone/web URL is stale or does not match the attached GitHub repository"
				}
				_ = policy.updateIntegrationHealth(r.Context(), resolved.Repository.Coordinate, state, detail, metadata.FullName)
			}
		}
	}
	writeForgeJSON(w, http.StatusOK, map[string]any{
		"repository": resolved.Repository.Coordinate.String(), "state": state,
		"detail": detail, "provider_repository": providerRepository,
		"generation": resolved.Repository.Integration.Generation,
	})
}

func (policy *GitForgePolicy) updateRepositoryIntegration(w http.ResponseWriter, r *http.Request, repository forge.Repository, body []byte) {
	var request struct {
		ExpectedAnnouncementID string `json:"expected_announcement_id"`
		GitBackend             string `json:"git_backend"`
		CollaborationBackend   string `json:"collaboration_backend"`
		MirrorBackend          string `json:"mirror_backend"`
		CIBackend              string `json:"ci_backend"`
		ConnectionID           string `json:"connection_id"`
		ExternalRepositoryID   string `json:"external_repository_id"`
	}
	if !decodeStrictJSON(body, &request) || request.ExpectedAnnouncementID == "" || request.ExpectedAnnouncementID != repository.Announcement.ID.Hex() {
		writeGitError(w, http.StatusConflict, "announcement_changed")
		return
	}
	if request.CollaborationBackend == "" {
		request.CollaborationBackend = forge.BackendRelay
	}
	if request.MirrorBackend != "" || request.CIBackend != "" {
		writeGitError(w, http.StatusConflict, "capability_not_enabled")
		return
	}
	integration := forge.Integration{
		Coordinate: repository.Coordinate, AnnouncementID: repository.Announcement.ID.Hex(),
		ConfigurationSource: forge.ConfigurationExplicit,
		GitBackend:          request.GitBackend, CollaborationBackend: request.CollaborationBackend,
		ConnectionID: request.ConnectionID, ExternalRepositoryID: request.ExternalRepositoryID,
		HealthState: "ready",
	}
	switch request.GitBackend {
	case forge.BackendGitHub:
		if policy.GitHub == nil || request.CollaborationBackend != forge.BackendRelay && request.CollaborationBackend != forge.BackendGitHub {
			writeGitError(w, http.StatusConflict, "github_collaboration_not_enabled")
			return
		}
		repositoryID, err := strconv.ParseInt(request.ExternalRepositoryID, 10, 64)
		if err != nil || repositoryID <= 0 || request.ConnectionID == "" {
			writeGitError(w, http.StatusBadRequest, "invalid_github_repository")
			return
		}
		metadata, err := policy.GitHub.RepositoryByID(r.Context(), request.ConnectionID, repositoryID)
		if err != nil {
			writeGitBackendFailure(w, err)
			return
		}
		if !githubAnnouncementMatches(repository.Announcement, metadata) {
			writeGitError(w, http.StatusConflict, "announcement_provider_mismatch")
			return
		}
		integration.ExternalRepository = metadata.FullName
	case forge.BackendWalGit:
		if repository.Classification != string(gitforge.RepositoryHosted) || request.CollaborationBackend != forge.BackendRelay {
			writeGitError(w, http.StatusConflict, "invalid_walgit_integration")
			return
		}
		integration.ConnectionID, integration.ExternalRepositoryID = "", ""
	case forge.BackendUnmanaged:
		if repository.CloneURL == "" || request.CollaborationBackend != forge.BackendRelay {
			writeGitError(w, http.StatusConflict, "invalid_unmanaged_integration")
			return
		}
		integration.ConnectionID, integration.ExternalRepositoryID = "", ""
	case forge.BackendNone:
		if repository.Classification != string(gitforge.RepositoryAnchor) || request.CollaborationBackend != forge.BackendRelay {
			writeGitError(w, http.StatusConflict, "invalid_anchor_integration")
			return
		}
		integration.ConnectionID, integration.ExternalRepositoryID = "", ""
	default:
		writeGitError(w, http.StatusBadRequest, "unknown_git_backend")
		return
	}
	if err := policy.setExplicitIntegration(r.Context(), integration, request.ExpectedAnnouncementID); err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	if integration.CollaborationBackend == forge.BackendGitHub {
		if err := policy.rebuildRepositoryForgeOutbox(r.Context(), repository.Coordinate); err != nil {
			writeGitBackendFailure(w, err)
			return
		}
	}
	resolved, _, err := policy.ResolveIntegration(r.Context(), repository.Coordinate)
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusOK, map[string]any{"integration": resolved.Repository.Integration, "capabilities": resolved.Capabilities})
}

func githubAnnouncementMatches(event nostr.Event, repository githubforge.RepositoryMetadata) bool {
	cloneExpected := normalizeGitHubURL(repository.CloneURL)
	webExpected := normalizeGitHubURL(repository.HTMLURL)
	cloneMatches, webMatches := false, false
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "clone":
			for _, value := range tag[1:] {
				cloneMatches = cloneMatches || normalizeGitHubURL(value) == cloneExpected
			}
		case "web":
			for _, value := range tag[1:] {
				webMatches = webMatches || normalizeGitHubURL(value) == webExpected
			}
		}
	}
	return cloneExpected != "" && webExpected != "" && cloneMatches && webMatches
}

func normalizeGitHubURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	path := strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), ".git")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return "https://github.com/" + strings.ToLower(parts[0]+"/"+parts[1])
}

func mustRepositoryID(value string) int64 {
	id, _ := strconv.ParseInt(value, 10, 64)
	return id
}

func decodeStrictJSON(body []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}

func writeForgeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func forgeHTTPError(err error) (int, string) {
	var backendError *forge.Error
	if errors.As(err, &backendError) {
		return backendError.Status, backendError.Code
	}
	return http.StatusBadGateway, "forge_operation_failed"
}

func (policy *GitForgePolicy) ServeGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowedForGit(w)
		return
	}
	if policy.EventSource == nil || !policy.Config.GitHub.Enabled {
		writeGitError(w, http.StatusServiceUnavailable, "github_webhook_not_enabled")
		return
	}
	limit := policy.Config.GitHub.MaxWebhookBytes
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		writeGitError(w, http.StatusRequestEntityTooLarge, "github_webhook_too_large")
		return
	}
	events, err := policy.EventSource.VerifyAndDecode(r.Context(), r.Header, body)
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	if len(events) != 1 {
		writeGitError(w, http.StatusBadRequest, "invalid_github_webhook")
		return
	}
	digest := sha256.Sum256(body)
	inserted, err := policy.storeGitHubWebhook(r.Context(), events[0], hex.EncodeToString(digest[:]))
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusAccepted, map[string]any{
		"delivery_id": events[0].DeliveryID, "queued": inserted, "duplicate": !inserted,
	})
}
