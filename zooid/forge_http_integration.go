package zooid

import (
	"fiatjaf.com/nostr"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/githubforge"
)

func (policy *GitForgePolicy) updateRepositoryIntegration(w http.ResponseWriter, r *http.Request, repository forge.Repository, body []byte) {
	var request struct {
		ExpectedAnnouncementID      string               `json:"expected_announcement_id"`
		GitBackend                  string               `json:"git_backend"`
		GitAuthorityBackend         string               `json:"git_authority_backend"`
		ContentBackend              string               `json:"content_backend"`
		GitReplicationTargetBackend string               `json:"git_replication_target_backend"`
		CollaborationBackend        string               `json:"collaboration_backend"`
		MirrorBackend               string               `json:"mirror_backend"`
		CIBackend                   string               `json:"ci_backend"`
		ConnectionID                string               `json:"connection_id"`
		ExternalRepositoryID        string               `json:"external_repository_id"`
		CIConnectionID              string               `json:"ci_connection_id"`
		CIExternalRepositoryID      string               `json:"ci_external_repository_id"`
		CIWorkflow                  string               `json:"ci_workflow"`
		Replica                     *forge.ReplicaConfig `json:"replica"`
	}
	if !decodeStrictJSON(body, &request) || request.ExpectedAnnouncementID == "" || request.ExpectedAnnouncementID != repository.Announcement.ID.Hex() {
		writeGitError(w, http.StatusConflict, "announcement_changed")
		return
	}
	if request.CollaborationBackend == "" {
		request.CollaborationBackend = forge.BackendRelay
	}
	if request.GitAuthorityBackend == "" {
		request.GitAuthorityBackend = request.GitBackend
	}
	if request.ContentBackend == "" {
		request.ContentBackend = request.GitBackend
	}
	if request.GitBackend == "" {
		request.GitBackend = request.GitAuthorityBackend
	}
	if request.MirrorBackend == forge.BackendNone {
		request.MirrorBackend = ""
	}
	if request.CIBackend == forge.BackendNone {
		request.CIBackend = ""
	}
	if request.CIBackend != "" && request.CIBackend != forge.BackendGitHub {
		writeGitError(w, http.StatusBadRequest, "unknown_ci_backend")
		return
	}
	if request.MirrorBackend != "" && request.MirrorBackend != forge.BackendGitHub {
		writeGitError(w, http.StatusBadRequest, "unknown_mirror_backend")
		return
	}
	integration := forge.Integration{
		Coordinate: repository.Coordinate, AnnouncementID: repository.Announcement.ID.Hex(),
		ConfigurationSource: forge.ConfigurationExplicit,
		GitBackend:          request.GitBackend, GitAuthorityBackend: request.GitAuthorityBackend,
		ContentBackend: request.ContentBackend, GitReplicationTargetBackend: request.GitReplicationTargetBackend,
		CollaborationBackend: request.CollaborationBackend,
		MirrorBackend:        request.MirrorBackend, CIBackend: request.CIBackend,
		ConnectionID: request.ConnectionID, ExternalRepositoryID: request.ExternalRepositoryID,
		CIConnectionID: request.CIConnectionID, CIExternalRepositoryID: request.CIExternalRepositoryID,
		CIWorkflow:  request.CIWorkflow,
		HealthState: "ready", Replica: request.Replica,
	}
	if integration.Replica != nil && integration.Replica.Enabled {
		if integration.Replica.AuthorityBackend == "" {
			integration.Replica.AuthorityBackend = request.GitAuthorityBackend
		}
		if integration.Replica.ReplicaBackend == "" {
			integration.Replica.ReplicaBackend = request.ContentBackend
		}
		if integration.Replica.Direction == "" {
			integration.Replica.Direction = "authority-to-replica"
		}
		if integration.Replica.Mode == "" {
			integration.Replica.Mode = forge.ReplicaModeContinuous
		}
		if integration.Replica.ReadPolicy == "" {
			integration.Replica.ReadPolicy = forge.ReplicaReadFallback
		}
		if !integration.Replica.SyncBranches && !integration.Replica.SyncTags {
			integration.Replica.SyncBranches = true
			integration.Replica.SyncTags = true
		}
		if integration.Replica.WaitMilliseconds == 0 {
			integration.Replica.WaitMilliseconds = 1500
		}
		integration.HealthState = "provisioning"
	}
	githubNeeded := request.GitAuthorityBackend == forge.BackendGitHub || request.ContentBackend == forge.BackendGitHub || request.CollaborationBackend == forge.BackendGitHub || request.MirrorBackend == forge.BackendGitHub || request.GitReplicationTargetBackend == forge.BackendGitHub
	var providerMetadata githubforge.RepositoryMetadata
	if githubNeeded {
		if policy.GitHub == nil || request.CollaborationBackend != forge.BackendRelay && request.CollaborationBackend != forge.BackendGitHub {
			writeGitError(w, http.StatusConflict, "github_collaboration_not_enabled")
			return
		}
		repositoryID, err := strconv.ParseInt(request.ExternalRepositoryID, 10, 64)
		if err != nil || repositoryID <= 0 || request.ConnectionID == "" {
			writeGitError(w, http.StatusBadRequest, "invalid_github_repository")
			return
		}
		providerMetadata, err = policy.GitHub.RepositoryByID(r.Context(), request.ConnectionID, repositoryID)
		if err != nil {
			writeGitBackendFailure(w, err)
			return
		}
		integration.ExternalRepository = providerMetadata.FullName
	}
	if request.CIBackend == forge.BackendGitHub {
		if policy.GitHub == nil || !validGitHubWorkflow(request.CIWorkflow) {
			writeGitError(w, http.StatusConflict, "github_ci_not_enabled")
			return
		}
		ciRepositoryID, err := strconv.ParseInt(request.CIExternalRepositoryID, 10, 64)
		if err != nil || ciRepositoryID <= 0 || request.CIConnectionID == "" {
			writeGitError(w, http.StatusBadRequest, "invalid_github_ci_repository")
			return
		}
		ciMetadata, err := policy.GitHub.RepositoryByID(r.Context(), request.CIConnectionID, ciRepositoryID)
		if err != nil {
			writeGitBackendFailure(w, err)
			return
		}
		integration.CIExternalRepository = ciMetadata.FullName
		if request.GitAuthorityBackend == forge.BackendGitHub &&
			(request.CIConnectionID != request.ConnectionID || request.CIExternalRepositoryID != request.ExternalRepositoryID) {
			writeGitError(w, http.StatusConflict, "github_ci_repository_must_match_source")
			return
		}
	}
	switch request.GitAuthorityBackend {
	case forge.BackendGitHub:
		if !githubAnnouncementMatches(repository.Announcement, providerMetadata) {
			writeGitError(w, http.StatusConflict, "announcement_provider_mismatch")
			return
		}
	case forge.BackendWalGit:
		if repository.Classification != string(gitforge.RepositoryHosted) || request.CollaborationBackend != forge.BackendRelay && request.CollaborationBackend != forge.BackendGitHub {
			writeGitError(w, http.StatusConflict, "invalid_walgit_integration")
			return
		}
	case forge.BackendUnmanaged:
		if repository.CloneURL == "" || request.CollaborationBackend != forge.BackendRelay || request.MirrorBackend != "" {
			writeGitError(w, http.StatusConflict, "invalid_unmanaged_integration")
			return
		}
		integration.ConnectionID, integration.ExternalRepositoryID = "", ""
	case forge.BackendNone:
		if repository.Classification != string(gitforge.RepositoryAnchor) || request.CollaborationBackend != forge.BackendRelay || request.MirrorBackend != "" {
			writeGitError(w, http.StatusConflict, "invalid_anchor_integration")
			return
		}
		integration.ConnectionID, integration.ExternalRepositoryID = "", ""
	default:
		writeGitError(w, http.StatusBadRequest, "unknown_git_backend")
		return
	}
	switch request.ContentBackend {
	case forge.BackendGitHub:
		if request.GitAuthorityBackend != forge.BackendGitHub {
			writeGitError(w, http.StatusConflict, "invalid_content_backend")
			return
		}
	case forge.BackendWalGit:
		if request.GitAuthorityBackend != forge.BackendWalGit && (integration.Replica == nil || !integration.Replica.Enabled) {
			writeGitError(w, http.StatusConflict, "replica_configuration_required")
			return
		}
	case forge.BackendUnmanaged:
		if request.GitAuthorityBackend != forge.BackendUnmanaged {
			writeGitError(w, http.StatusConflict, "invalid_content_backend")
			return
		}
	case forge.BackendNone:
		if request.GitAuthorityBackend != forge.BackendNone {
			writeGitError(w, http.StatusConflict, "invalid_content_backend")
			return
		}
	default:
		writeGitError(w, http.StatusBadRequest, "unknown_content_backend")
		return
	}
	if integration.Replica != nil && integration.Replica.Enabled {
		if request.GitAuthorityBackend != forge.BackendGitHub || request.ContentBackend != forge.BackendWalGit ||
			integration.Replica.AuthorityBackend != forge.BackendGitHub || integration.Replica.ReplicaBackend != forge.BackendWalGit ||
			integration.Replica.Direction != "authority-to-replica" ||
			(integration.Replica.Mode != forge.ReplicaModeContinuous && integration.Replica.Mode != forge.ReplicaModeOnDemand) ||
			(!integration.Replica.SyncBranches && !integration.Replica.SyncTags) ||
			(integration.Replica.ReadPolicy != forge.ReplicaReadFallback && integration.Replica.ReadPolicy != forge.ReplicaReadWait && integration.Replica.ReadPolicy != forge.ReplicaReadStale) ||
			integration.Replica.WaitMilliseconds < 0 || integration.Replica.WaitMilliseconds > 10000 {
			writeGitError(w, http.StatusConflict, "invalid_replica_configuration")
			return
		}
		if _, ok := policy.Registry.ContentBackend(forge.BackendWalGit).(forge.RepositoryImporter); !ok {
			writeGitError(w, http.StatusConflict, "walgit_replica_unavailable")
			return
		}
	}
	if request.GitReplicationTargetBackend != "" && request.GitReplicationTargetBackend != forge.BackendGitHub {
		writeGitError(w, http.StatusBadRequest, "unknown_git_replication_target")
		return
	}
	if request.GitReplicationTargetBackend != "" && request.GitAuthorityBackend != forge.BackendWalGit {
		writeGitError(w, http.StatusConflict, "git_replication_target_requires_walgit_authority")
		return
	}
	if request.GitReplicationTargetBackend != "" {
		if policy.Registry.GitReplicationTarget(request.GitReplicationTargetBackend) == nil {
			writeGitError(w, http.StatusConflict, "git_replication_target_unavailable")
			return
		}
		if _, ok := policy.Registry.AuthorityBackend(request.GitAuthorityBackend).(forge.GitReplicationSource); !ok {
			writeGitError(w, http.StatusConflict, "git_replication_source_unavailable")
			return
		}
	}
	if !githubNeeded {
		integration.ConnectionID, integration.ExternalRepositoryID, integration.ExternalRepository = "", "", ""
	}
	if request.CIBackend == "" {
		integration.CIConnectionID, integration.CIExternalRepositoryID, integration.CIExternalRepository, integration.CIWorkflow = "", "", "", ""
	}
	if err := policy.setExplicitIntegration(r.Context(), integration, request.ExpectedAnnouncementID); err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	if err := policy.configureGitReplica(r.Context(), integration); err != nil {
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

func validGitHubWorkflow(value string) bool {
	if value == "" || len(value) > 255 || strings.ContainsAny(value, "\x00\r\n/") {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-') {
			return false
		}
	}
	return true
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
