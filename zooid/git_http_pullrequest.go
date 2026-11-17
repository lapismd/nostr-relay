package zooid

import (
	"context"
	"encoding/json"
	"fiatjaf.com/nostr"
	"net/http"
	"strings"
	"time"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

type pullRequestDiffResponse struct {
	PullRequest  string                           `json:"pull_request"`
	Repository   string                           `json:"repository"`
	Lifecycle    projecttask.PullRequestLifecycle `json:"lifecycle"`
	BaseSource   string                           `json:"base_source"`
	TargetBranch string                           `json:"target_branch,omitempty"`
	Base         string                           `json:"base"`
	Head         string                           `json:"head"`
	MergeBase    string                           `json:"merge_base,omitempty"`
	Files        []forge.DiffFile                 `json:"files"`
	Patch        string                           `json:"patch"`
	forge.ResultMetadata
}

// ServePullRequestAPI provides the application-owned base-to-head diff needed
// by the web client. Collaboration authority still comes from the Nostr PR
// graph; WalGit is asked only to compare immutable Git objects.
func (policy *GitForgePolicy) ServePullRequestAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowedForGit(w)
		return
	}
	identifier, ok := pullRequestDiffPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	expectedURL, err := policy.publicRequestURL(r)
	if err != nil {
		writeGitError(w, http.StatusBadRequest, "invalid_request_url")
		return
	}
	authEvent, err := gitforge.VerifyReadAuth(r, expectedURL, time.Now())
	if err != nil {
		gitAuthRequired(w, r.Method)
		return
	}

	root, found, err := policy.ProjectTasks.EventByID(r.Context(), identifier)
	if err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "event_store_unavailable")
		return
	}
	if !found || root.Kind != projecttask.KindPullRequest || policy.ProjectTasks.IsTombstoned(r.Context(), root) {
		http.NotFound(w, r)
		return
	}
	tag := root.Tags.Find("a")
	if tag == nil || len(tag) < 2 {
		writeGitError(w, http.StatusUnprocessableEntity, "invalid_pull_request")
		return
	}
	coordinate, err := projecttask.ParseRepositoryCoordinate(tag[1])
	if err != nil {
		writeGitError(w, http.StatusUnprocessableEntity, "invalid_pull_request")
		return
	}
	resolved, found, err := policy.ResolveIntegration(r.Context(), coordinate)
	if err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "event_store_unavailable")
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if resolved.Content == nil || !resolved.Capabilities.Compare {
		writeGitError(w, http.StatusConflict, "repository_compare_unavailable")
		return
	}
	if status, code := policy.authorizePullRequest(r.Context(), authEvent.PubKey, root, resolved.Repository); status != 0 {
		writeGitError(w, status, code)
		return
	}
	if status, code := policy.requireReady(r.Context(), resolved.Repository); status != 0 {
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "2")
		}
		writeGitError(w, status, code)
		return
	}

	related := policy.pullRequestRelatedEvents(r.Context(), root.ID)
	state, err := projecttask.ReducePullRequest(root, resolved.Repository.Announcement, related, policy.ProjectTasks.repositoryAuthority(resolved.Repository.Announcement))
	if err != nil || state.CurrentCommit == "" {
		writeGitError(w, http.StatusUnprocessableEntity, "invalid_pull_request")
		return
	}
	base := state.MergeBase
	baseSource := "merge-base"
	useMergeBase := false
	if base == "" && state.BaseCommit != "" {
		base = state.BaseCommit
		baseSource = "base-commit"
		useMergeBase = true
	}
	if base == "" {
		if diffBackend, ok := resolved.Collaboration.(forge.PullRequestDiffBackend); ok {
			mapping, found, mappingErr := policy.loadMappingByNostrRoot(
				r.Context(), resolved.Repository.Integration.CollaborationBackend,
				resolved.Repository.Integration.ConnectionID, root.ID.Hex(),
			)
			if mappingErr != nil {
				writeGitError(w, http.StatusServiceUnavailable, "mapping_store_unavailable")
				return
			}
			if found && mapping.ObjectType == "pull_request" && mapping.ExternalNumber > 0 {
				comparison, diffErr := diffBackend.PullRequestDiff(r.Context(), resolved.Repository, forge.PullRequestDiffRequest{
					Number: mapping.ExternalNumber, MaxFiles: policy.Config.Git.MaxDiffFiles,
					MaxPatchBytes: policy.Config.Git.MaxDiffBytes, Principal: authEvent.PubKey.Hex(),
				})
				if diffErr != nil {
					writeGitBackendFailure(w, diffErr)
					return
				}
				writePullRequestDiff(w, r.Method, pullRequestDiffResponse{
					PullRequest: root.ID.Hex(), Repository: coordinate.String(), Lifecycle: state.Lifecycle,
					BaseSource: "provider-pull-request", TargetBranch: state.TargetBranch,
					Base: comparison.Base, Head: comparison.Head, MergeBase: comparison.MergeBase,
					Files: comparison.Files, Patch: comparison.Patch, ResultMetadata: comparison.ResultMetadata,
				}, resolved.Capabilities.CapabilityLimits)
				return
			}
		}
		baseSource = "target-branch"
		target := strings.TrimPrefix(state.TargetBranch, "refs/heads/")
		if target == "" {
			target = "HEAD"
		}
		request := forge.RevisionRequest{Revision: target, Principal: authEvent.PubKey.Hex()}
		baseResult, resolveErr := policy.repositoryContentRead(r.Context(), resolved, target, func(backend forge.RepositoryContentBackend) (any, error) {
			return backend.Resolve(r.Context(), resolved.Repository, request)
		})
		err = resolveErr
		if err != nil {
			writeGitBackendFailure(w, err)
			return
		}
		resolvedBase, ok := baseResult.(forge.Revision)
		if !ok || !fullForgeObjectID(resolvedBase.SHA) {
			writeGitError(w, http.StatusBadGateway, "invalid_content_backend_response")
			return
		}
		base = resolvedBase.SHA
		useMergeBase = true
	}
	for _, oid := range []string{base, state.CurrentCommit} {
		request := forge.RevisionRequest{Revision: oid, Principal: authEvent.PubKey.Hex()}
		if _, err := policy.repositoryContentRead(r.Context(), resolved, oid, func(backend forge.RepositoryContentBackend) (any, error) {
			return backend.Resolve(r.Context(), resolved.Repository, request)
		}); err != nil {
			writeGitBackendFailure(w, err)
			return
		}
	}
	compareRequest := forge.CompareRequest{
		Base: base, Head: state.CurrentCommit, UseMergeBase: useMergeBase,
		MaxFiles: policy.Config.Git.MaxDiffFiles, MaxPatchBytes: policy.Config.Git.MaxDiffBytes,
		Principal: authEvent.PubKey.Hex(),
	}
	comparisonResult, err := policy.repositoryContentRead(r.Context(), resolved, state.CurrentCommit, func(backend forge.RepositoryContentBackend) (any, error) {
		return backend.Compare(r.Context(), resolved.Repository, compareRequest)
	})
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	comparison, ok := comparisonResult.(forge.Diff)
	if !ok {
		writeGitError(w, http.StatusBadGateway, "invalid_content_backend_response")
		return
	}

	response := pullRequestDiffResponse{
		PullRequest: root.ID.Hex(), Repository: coordinate.String(), Lifecycle: state.Lifecycle,
		BaseSource: baseSource, TargetBranch: state.TargetBranch,
		Base: comparison.Base, Head: comparison.Head, MergeBase: comparison.MergeBase,
		Files: comparison.Files, Patch: comparison.Patch,
	}
	response.ResultMetadata = comparison.ResultMetadata
	writePullRequestDiff(w, r.Method, response, resolved.Capabilities.CapabilityLimits)
}

func writePullRequestDiff(w http.ResponseWriter, method string, response pullRequestDiffResponse, limits forge.ContentLimits) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	if response.CapabilityLimits == nil {
		response.CapabilityLimits = &limits
	}
	if method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(w).Encode(response)
}

func pullRequestDiffPath(path string) (nostr.ID, bool) {
	rest := strings.TrimPrefix(path, "/api/pull-requests/")
	if rest == path || !strings.HasSuffix(rest, "/diff") {
		return nostr.ZeroID, false
	}
	identifier := strings.TrimSuffix(rest, "/diff")
	if len(identifier) != 64 || identifier != strings.ToLower(identifier) || strings.Contains(identifier, "/") {
		return nostr.ZeroID, false
	}
	id, err := nostr.IDFromHex(identifier)
	return id, err == nil
}

func (policy *GitForgePolicy) pullRequestRelatedEvents(ctx context.Context, root nostr.ID) []nostr.Event {
	kinds := []nostr.Kind{
		projecttask.KindPullRequestUpdate,
		nostr.KindStatusOpen, nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft,
		nostr.KindTextNote, nostr.KindComment,
	}
	seen := make(map[nostr.ID]nostr.Event)
	for _, tag := range []string{"e", "E"} {
		filter := nostr.Filter{Kinds: kinds, Tags: nostr.TagMap{tag: {root.Hex()}}, Limit: 2000}
		for event := range policy.Events.QueryEventsBefore(ctx, filter, "", 2000) {
			if !policy.ProjectTasks.IsTombstoned(ctx, event) {
				seen[event.ID] = event
			}
		}
	}
	events := make([]nostr.Event, 0, len(seen))
	for _, event := range seen {
		events = append(events, event)
	}
	return events
}
