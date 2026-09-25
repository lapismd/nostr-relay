package zooid

import (
	"database/sql"
	"fiatjaf.com/nostr"
	"net/http"
	"strings"
	"zooid/internal/forge"
)

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
	if r.Method == http.MethodPost && action == "cancel" && operation.Command == "ci_start" {
		if err := policy.cancelForgeCI(r.Context(), operation); err != nil {
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
	ciRepository := ""
	if resolved.Repository.Integration.GitAuthorityBackend == forge.BackendGitHub || resolved.Repository.Integration.GitBackend == forge.BackendGitHub {
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
	var replicaHealth any
	if resolved.Repository.Integration.Replica != nil && resolved.Repository.Integration.Replica.Enabled {
		replica, found, err := policy.loadGitReplica(r.Context(), resolved.Repository.Coordinate)
		if err != nil {
			state, detail = "degraded", "Git replica health is unavailable"
		} else if !found {
			state, detail = "degraded", "Git replica has not been provisioned"
			replicaHealth = map[string]any{"state": "missing", "behind_refs": 0}
		} else {
			behind, countErr := policy.gitReplicaBehindRefs(r.Context(), resolved.Repository.Coordinate)
			if countErr != nil {
				state, detail = "degraded", "Git replica ref health is unavailable"
			}
			if replica.State != "current" && state != "disabled" {
				state = "degraded"
				if detail == "" {
					detail = "Git replica is " + replica.State
				}
			}
			replicaHealth = map[string]any{
				"state": replica.State, "behind_refs": behind,
				"last_sync_at":            nullableTime(replica.LastSyncAt),
				"last_success_at":         nullableTime(replica.LastSuccessAt),
				"last_authority_check_at": nullableTime(replica.LastAuthorityCheckAt),
				"last_reconcile_at":       nullableTime(replica.LastReconcileAt),
			}
			if replica.LastError != "" {
				replicaHealth.(map[string]any)["failure"] = replica.LastError
			}
		}
	}
	var replicationHealth any
	if resolved.Repository.Integration.GitReplicationTargetBackend != "" {
		if value, found, err := policy.gitReplicationHealth(r.Context(), resolved.Repository.Coordinate); err != nil {
			state, detail = "degraded", "Git replication health is unavailable"
		} else if found {
			replicationHealth = value
		} else {
			replicationHealth = map[string]any{
				"target_backend": resolved.Repository.Integration.GitReplicationTargetBackend,
				"state":          "pending",
			}
		}
	}
	if resolved.Repository.Integration.CIBackend == forge.BackendGitHub {
		if policy.GitHub == nil {
			state, detail = "disabled", "GitHub CI adapter is disabled"
		} else {
			metadata, err := policy.GitHub.RepositoryByID(r.Context(), resolved.Repository.Integration.CIConnectionID, mustRepositoryID(resolved.Repository.Integration.CIExternalRepositoryID))
			if err != nil {
				state, detail = "degraded", "GitHub CI repository is unavailable"
			} else {
				ciRepository = metadata.FullName
			}
		}
	}
	writeForgeJSON(w, http.StatusOK, map[string]any{
		"repository": resolved.Repository.Coordinate.String(), "state": state,
		"detail": detail, "provider_repository": providerRepository, "ci_repository": ciRepository,
		"generation":      resolved.Repository.Integration.Generation,
		"git_authority":   resolved.Repository.Integration.GitAuthorityBackend,
		"content_backend": resolved.Repository.Integration.ContentBackend,
		"replica":         replicaHealth,
		"git_replication": replicationHealth,
	})
}

func nullableTime(value sql.NullTime) any {
	if !value.Valid {
		return nil
	}
	return value.Time.UTC()
}
