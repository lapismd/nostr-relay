package zooid

import (
	"bytes"
	"encoding/json"
	"fiatjaf.com/nostr"
	"net/http"
	"zooid/internal/forge"
)

func (policy *GitForgePolicy) serveRepositoryForgeAPI(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, idempotencyKey string, body []byte) {
	ownerText, repositoryText, action, ok := splitRepositoryPath(r.URL.Path, "/api/forge/repositories/")
	if !ok {
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
			"repository": resolved.Repository.Coordinate.String(), "capabilities": policy.advertiseCapabilities(r.Context(), resolved, actor.Hex()),
		})
	case r.Method == http.MethodGet && action == "protection":
		policy.serveRepositoryProtection(w, r, actor, resolved)
	case r.Method == http.MethodPut && action == "protection":
		policy.updateRepositoryProtection(w, r, actor, resolved, body)
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
	case r.Method == http.MethodPost && action == "ci":
		policy.enqueueForgeCI(w, r, actor, idempotencyKey, resolved, body)
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
	case r.Method == http.MethodPost && action == "migration":
		policy.startForgeMigration(w, r, actor, idempotencyKey, resolved, body)
	case r.Method == http.MethodPost && action == "migration/activate":
		policy.activateForgeMigrationHTTP(w, r, actor, resolved.Repository, body)
	default:
		http.NotFound(w, r)
	}
}

func (policy *GitForgePolicy) startForgeMigration(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, idempotencyKey string, resolved forge.ResolvedRepository, body []byte) {
	if !policy.Config.CanManage(actor) {
		writeGitError(w, http.StatusForbidden, "relay_admin_required")
		return
	}
	var request struct {
		ExpectedAnnouncementID    string `json:"expected_announcement_id"`
		AcknowledgeProviderFreeze bool   `json:"acknowledge_provider_write_freeze"`
		KeepGitHubCollaboration   bool   `json:"keep_github_collaboration"`
		RetainGitHubMirror        bool   `json:"retain_github_mirror"`
	}
	if !decodeStrictJSON(body, &request) || !request.AcknowledgeProviderFreeze {
		writeGitError(w, http.StatusBadRequest, "provider_write_freeze_required")
		return
	}
	if request.ExpectedAnnouncementID != resolved.Repository.Announcement.ID.Hex() {
		writeGitError(w, http.StatusConflict, "announcement_changed")
		return
	}
	if resolved.Repository.Integration.GitAuthorityBackend != forge.BackendGitHub && resolved.Repository.Integration.GitBackend != forge.BackendGitHub || resolved.Authority == nil {
		writeGitError(w, http.StatusConflict, "github_source_required")
		return
	}
	if _, ok := policy.Registry.ContentBackend(forge.BackendWalGit).(forge.RepositoryImporter); !ok {
		writeGitError(w, http.StatusConflict, "walgit_import_unavailable")
		return
	}
	operation, err := policy.enqueueForgeMigration(r.Context(), actor, resolved.Repository, idempotencyKey, request.KeepGitHubCollaboration, request.RetainGitHubMirror)
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusAccepted, publicForgeOperation(operation))
}

func (policy *GitForgePolicy) activateForgeMigrationHTTP(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, repository forge.Repository, body []byte) {
	if !policy.Config.CanManage(actor) {
		writeGitError(w, http.StatusForbidden, "relay_admin_required")
		return
	}
	var request struct {
		ExpectedAnnouncementID string `json:"expected_announcement_id"`
	}
	if !decodeStrictJSON(body, &request) || request.ExpectedAnnouncementID == "" {
		writeGitError(w, http.StatusBadRequest, "invalid_migration_activation")
		return
	}
	integration, err := policy.activateForgeMigration(r.Context(), repository, request.ExpectedAnnouncementID)
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	if integration.CollaborationBackend == forge.BackendGitHub || integration.MirrorBackend == forge.BackendGitHub {
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
