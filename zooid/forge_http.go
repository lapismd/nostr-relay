package zooid

import (
	"bytes"
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

	path := strings.TrimPrefix(r.URL.Path, "/api/forge/")
	switch {
	case r.Method == http.MethodPost && path == "connections/github":
		policy.createGitHubConnection(w, r, authEvent.PubKey, body)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "connections/github/") && strings.HasSuffix(path, "/repositories"):
		policy.listGitHubRepositories(w, r, authEvent.PubKey, path)
	case strings.HasPrefix(path, "repositories/"):
		policy.serveRepositoryForgeAPI(w, r, authEvent.PubKey, body)
	default:
		http.NotFound(w, r)
	}
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

func (policy *GitForgePolicy) serveRepositoryForgeAPI(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, body []byte) {
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
	default:
		http.NotFound(w, r)
	}
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
		if policy.GitHub == nil || request.CollaborationBackend != forge.BackendRelay {
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

// ServeGitHubWebhook is enabled by the collaboration milestone. Keeping the
// route closed during read-only GitHub attachment prevents accepting delivery
// data before durable decoding and workers are available.
func (policy *GitForgePolicy) ServeGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowedForGit(w)
		return
	}
	writeGitError(w, http.StatusServiceUnavailable, "github_webhook_not_enabled")
}
