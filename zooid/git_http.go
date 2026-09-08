package zooid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

const maxGitControlBody = 1 << 20

// ServeGitHTTP is the public, authenticated smart-HTTP/LFS/bundle gateway.
// It deliberately exposes only Git data-plane routes, never WalGit policy or
// administrative endpoints.
func (policy *GitForgePolicy) ServeGitHTTP(w http.ResponseWriter, r *http.Request) {
	ownerText, repositoryText, subpath, ok := splitRepositoryPath(r.URL.Path, "/git/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	write, err := classifyGitOperation(r, subpath)
	if err != nil {
		writeGitError(w, http.StatusBadRequest, "invalid_git_request")
		return
	}
	if write == nil {
		methodNotAllowedForGit(w)
		return
	}

	repositoryRoot, err := policy.publicGitRoot(ownerText, repositoryText)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	authEvent, err := gitforge.VerifyGitAuth(r, repositoryRoot, time.Now())
	if err != nil {
		gitAuthRequired(w, r.Method)
		return
	}

	repository, status, code := policy.resolvePublicRepository(r.Context(), ownerText, repositoryText)
	if status != 0 {
		writeGitError(w, status, code)
		return
	}
	if status, code := policy.authorizeRepository(authEvent.PubKey, repository, *write); status != 0 {
		writeGitError(w, status, code)
		return
	}
	if status, code := policy.requireReady(r.Context(), repository); status != 0 {
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "2")
		}
		writeGitError(w, status, code)
		return
	}

	policy.Backend.ProxyGitHTTP(w, r, repository, subpath, authEvent.PubKey.Hex())
}

// ServeRepositoryAPI authorizes and streams WalGit's read-only repository API
// so browser clients can browse source without local Git access.
func (policy *GitForgePolicy) ServeRepositoryAPI(w http.ResponseWriter, r *http.Request) {
	ownerText, repositoryText, subpath, ok := splitRepositoryPath(r.URL.Path, "/api/git/")
	if !ok || !repositoryAPIPathAllowed(r.Method, subpath) {
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
	repository, status, code := policy.resolvePublicRepository(r.Context(), ownerText, repositoryText)
	if status != 0 {
		writeGitError(w, status, code)
		return
	}
	if status, code := policy.authorizeRepository(authEvent.PubKey, repository, false); status != 0 {
		writeGitError(w, status, code)
		return
	}
	if status, code := policy.requireReady(r.Context(), repository); status != 0 {
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "2")
		}
		writeGitError(w, status, code)
		return
	}

	policy.Backend.ProxyRepositoryAPI(w, r, repository, subpath, authEvent.PubKey.Hex())
}

type pullRequestDiffResponse struct {
	PullRequest  string                           `json:"pull_request"`
	Repository   string                           `json:"repository"`
	Lifecycle    projecttask.PullRequestLifecycle `json:"lifecycle"`
	BaseSource   string                           `json:"base_source"`
	TargetBranch string                           `json:"target_branch,omitempty"`
	Base         string                           `json:"base"`
	Head         string                           `json:"head"`
	MergeBase    string                           `json:"merge_base,omitempty"`
	Files        []gitforge.CompareFile           `json:"files"`
	Patch        string                           `json:"patch"`
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
	repository, found, err := policy.resolveRepository(r.Context(), coordinate)
	if err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "event_store_unavailable")
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if repository.Class != gitforge.RepositoryHosted {
		writeGitError(w, http.StatusConflict, "repository_not_hosted")
		return
	}
	if status, code := policy.authorizePullRequest(r.Context(), authEvent.PubKey, root, repository); status != 0 {
		writeGitError(w, status, code)
		return
	}
	if status, code := policy.requireReady(r.Context(), repository); status != 0 {
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "2")
		}
		writeGitError(w, status, code)
		return
	}

	related := policy.pullRequestRelatedEvents(r.Context(), root.ID)
	state, err := projecttask.ReducePullRequest(root, repository.Announcement, related)
	if err != nil || state.CurrentCommit == "" {
		writeGitError(w, http.StatusUnprocessableEntity, "invalid_pull_request")
		return
	}
	base := state.MergeBase
	baseSource := "merge-base"
	useMergeBase := false
	if base == "" {
		baseSource = "target-branch"
		target := strings.TrimPrefix(state.TargetBranch, "refs/heads/")
		if target == "" {
			target = "HEAD"
		}
		base, err = policy.Backend.Resolve(r.Context(), repository, target, authEvent.PubKey.Hex())
		if err != nil {
			writeGitBackendFailure(w, err)
			return
		}
		useMergeBase = true
	}
	comparison, err := policy.Backend.Compare(r.Context(), repository, gitforge.CompareRequest{
		Base: base, Head: state.CurrentCommit, UseMergeBase: useMergeBase,
		MaxFiles: policy.Config.Git.MaxDiffFiles, MaxPatchBytes: policy.Config.Git.MaxDiffBytes,
		Principal: authEvent.PubKey.Hex(),
	})
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	response := pullRequestDiffResponse{
		PullRequest: root.ID.Hex(), Repository: coordinate.String(), Lifecycle: state.Lifecycle,
		BaseSource: baseSource, TargetBranch: state.TargetBranch,
		Base: comparison.Base, Head: comparison.Head, MergeBase: comparison.MergeBase,
		Files: comparison.Files, Patch: comparison.Patch,
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(w).Encode(response)
}

func splitRepositoryPath(requestPath, prefix string) (string, string, string, bool) {
	rest := strings.TrimPrefix(requestPath, prefix)
	if rest == requestPath || rest == "" {
		return "", "", "", false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil || len(owner) != 64 || owner != strings.ToLower(owner) {
		return "", "", "", false
	}
	if _, err := nostr.PubKeyFromHex(owner); err != nil {
		return "", "", "", false
	}
	repository, err := url.PathUnescape(parts[1])
	if err != nil || repository == "" || strings.Contains(repository, "/") {
		return "", "", "", false
	}
	subpath := ""
	if len(parts) == 3 {
		subpath = parts[2]
	}
	if unsafePath(repository) || unsafePath(subpath) {
		return "", "", "", false
	}
	return owner, repository, subpath, true
}

func unsafePath(value string) bool {
	for _, component := range strings.Split(value, "/") {
		if component == "." || component == ".." || strings.ContainsRune(component, '\x00') {
			return true
		}
	}
	return false
}

func (policy *GitForgePolicy) publicGitRoot(owner, repository string) (string, error) {
	base, err := url.Parse(strings.TrimRight(policy.Config.Git.PublicURL, "/"))
	if err != nil {
		return "", err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/git/" + url.PathEscape(owner) + "/" + url.PathEscape(repository)
	base.RawPath = ""
	base.RawQuery = ""
	return base.String(), nil
}

func (policy *GitForgePolicy) publicRequestURL(r *http.Request) (string, error) {
	base, err := url.Parse(strings.TrimRight(policy.Config.Git.PublicURL, "/"))
	if err != nil {
		return "", err
	}
	requestPath := r.URL.Path
	prefix := strings.TrimRight(base.Path, "/")
	if prefix != "" && prefix != "/" && requestPath != prefix && !strings.HasPrefix(requestPath, prefix+"/") {
		requestPath = prefix + "/" + strings.TrimLeft(requestPath, "/")
	}
	base.Path = requestPath
	base.RawPath = ""
	base.RawQuery = r.URL.RawQuery
	return base.String(), nil
}

func (policy *GitForgePolicy) resolvePublicRepository(ctx context.Context, ownerText, repositoryText string) (gitforge.Repository, int, string) {
	owner, err := nostr.PubKeyFromHex(ownerText)
	if err != nil {
		return gitforge.Repository{}, http.StatusNotFound, "repository_not_found"
	}
	candidates := []string{repositoryText}
	if strings.HasSuffix(repositoryText, ".git") {
		candidates = append(candidates, strings.TrimSuffix(repositoryText, ".git"))
	}
	for _, identifier := range slices.Compact(candidates) {
		repository, found, err := policy.resolveRepository(ctx, projecttask.RepositoryCoordinate{Owner: owner, ID: identifier})
		if err != nil {
			return gitforge.Repository{}, http.StatusServiceUnavailable, "event_store_unavailable"
		}
		if !found {
			continue
		}
		if repository.Class != gitforge.RepositoryHosted {
			return gitforge.Repository{}, http.StatusConflict, "repository_not_hosted"
		}
		return repository, 0, ""
	}
	return gitforge.Repository{}, http.StatusNotFound, "repository_not_found"
}

func (policy *GitForgePolicy) authorizeRepository(pubkey nostr.PubKey, repository gitforge.Repository, write bool) (int, string) {
	metadata, found := policy.Groups.GetMetadata(repository.Channel)
	if !found {
		return http.StatusNotFound, "repository_scope_not_found"
	}
	if !policy.Groups.HasAccess(repository.Channel, pubkey) {
		if HasTag(metadata.Tags, "private") || HasTag(metadata.Tags, "hidden") {
			return http.StatusNotFound, "repository_not_found"
		}
		return http.StatusForbidden, "repository_access_denied"
	}
	if write && !policy.Groups.CanWrite(repository.Channel, pubkey) {
		return http.StatusForbidden, "repository_write_denied"
	}
	return 0, ""
}

func (policy *GitForgePolicy) authorizePullRequest(ctx context.Context, pubkey nostr.PubKey, root nostr.Event, repository gitforge.Repository) (int, string) {
	scope, err := policy.ProjectTasks.resolveStored(ctx, root)
	if err != nil || !scope.Relevant || len(scope.Channels) == 0 {
		return http.StatusNotFound, "pull_request_scope_not_found"
	}
	channels := append([]string{repository.Channel}, scope.Channels...)
	slices.Sort(channels)
	channels = slices.Compact(channels)
	for _, channel := range channels {
		metadata, found := policy.Groups.GetMetadata(channel)
		if !found {
			return http.StatusNotFound, "pull_request_scope_not_found"
		}
		if policy.Groups.HasAccess(channel, pubkey) {
			continue
		}
		if HasTag(metadata.Tags, "private") || HasTag(metadata.Tags, "hidden") {
			return http.StatusNotFound, "pull_request_not_found"
		}
		return http.StatusForbidden, "pull_request_access_denied"
	}
	return 0, ""
}

func (policy *GitForgePolicy) requireReady(ctx context.Context, repository gitforge.Repository) (int, string) {
	state, found, err := policy.intentState(ctx, repository.Coordinate)
	if err != nil {
		return http.StatusServiceUnavailable, "repository_state_unavailable"
	}
	if !found {
		if err := policy.upsertIntent(ctx, repository); err != nil {
			return http.StatusServiceUnavailable, "repository_state_unavailable"
		}
		return http.StatusServiceUnavailable, "repository_provisioning"
	}
	if state != "ready" {
		return http.StatusServiceUnavailable, "repository_provisioning"
	}
	return 0, ""
}

func classifyGitOperation(r *http.Request, subpath string) (*bool, error) {
	read, write := false, true
	switch {
	case subpath == "info/refs" && r.Method == http.MethodGet:
		switch r.URL.Query().Get("service") {
		case "git-upload-pack":
			return &read, nil
		case "git-receive-pack":
			return &write, nil
		default:
			return nil, fmt.Errorf("unknown Git service")
		}
	case subpath == "git-upload-pack" && r.Method == http.MethodPost:
		return &read, nil
	case subpath == "git-receive-pack" && r.Method == http.MethodPost:
		return &write, nil
	case subpath == "info/lfs/objects/batch" && r.Method == http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, maxGitControlBody+1))
		if err != nil || len(body) > maxGitControlBody {
			return nil, fmt.Errorf("invalid LFS batch")
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		var batch struct {
			Operation string `json:"operation"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, err
		}
		switch batch.Operation {
		case "download":
			return &read, nil
		case "upload":
			return &write, nil
		default:
			return nil, fmt.Errorf("unknown LFS operation")
		}
	case strings.HasPrefix(subpath, "info/lfs/objects/") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		return &read, nil
	case strings.HasPrefix(subpath, "info/lfs/objects/") && r.Method == http.MethodPut:
		return &write, nil
	case subpath == "info/lfs/verify" && r.Method == http.MethodPost:
		return &write, nil
	case (subpath == "bundles/list" || subpath == "bundles/catchup" || strings.HasPrefix(subpath, "bundles/")) && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		return &read, nil
	default:
		return nil, nil
	}
}

func repositoryAPIPathAllowed(method, subpath string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if subpath == "" || subpath == "refs" || subpath == "resolve" || subpath == "commits" {
		return true
	}
	if strings.HasPrefix(subpath, "resolve/") || strings.HasPrefix(subpath, "tree/") || strings.HasPrefix(subpath, "blob/") {
		return true
	}
	if subpath == "refs/branches" || subpath == "refs/tags" {
		return true
	}
	if strings.HasPrefix(subpath, "commit/") {
		return !strings.Contains(strings.TrimPrefix(subpath, "commit/"), "/")
	}
	return false
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

func writeGitBackendFailure(w http.ResponseWriter, err error) {
	var backendError *gitforge.BackendError
	if errors.As(err, &backendError) {
		status := backendError.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		writeGitError(w, status, backendError.Code)
		return
	}
	writeGitError(w, http.StatusBadGateway, "walgit_unavailable")
}

func gitAuthRequired(w http.ResponseWriter, method string) {
	w.Header().Set("WWW-Authenticate", `Nostr realm="git", method="`+method+`"`)
	writeGitError(w, http.StatusUnauthorized, "authentication_required")
}

func methodNotAllowedForGit(w http.ResponseWriter) {
	writeGitError(w, http.StatusMethodNotAllowed, "method_not_allowed")
}

func writeGitError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
