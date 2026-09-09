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
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
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

	resolved, status, code := policy.resolvePublicRepository(r.Context(), ownerText, repositoryText)
	if status != 0 {
		writeGitError(w, status, code)
		return
	}
	if status, code := policy.authorizeRepository(authEvent.PubKey, resolved.Repository, *write); status != 0 {
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

	backend, ok := resolved.Authority.(forge.SmartHTTPBackend)
	if !ok || (!resolved.Capabilities.SmartHTTP.Read && !*write) || (!resolved.Capabilities.SmartHTTP.Write && *write) {
		writeGitError(w, http.StatusConflict, "provider_git_required")
		return
	}
	backend.ServeGitHTTP(w, r, resolved.Repository, subpath, authEvent.PubKey.Hex())
}

// ServeRepositoryAPI serializes provider-neutral repository content results.
// Adapters never own the public JSON contract.
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
	resolved, status, code := policy.resolvePublicRepository(r.Context(), ownerText, repositoryText)
	if status != 0 {
		writeGitError(w, status, code)
		return
	}
	if status, code := policy.authorizeRepository(authEvent.PubKey, resolved.Repository, false); status != 0 {
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

	if resolved.Content == nil || !resolved.Capabilities.Browse {
		writeGitError(w, http.StatusConflict, "repository_browse_unavailable")
		return
	}
	result, err := policy.repositoryAPIResult(r.Context(), resolved, subpath, authEvent.PubKey.Hex(), r.URL.Query())
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeRepositoryResult(w, r.Method, result, resolved.Capabilities.CapabilityLimits)
}

func (policy *GitForgePolicy) repositoryAPIResult(ctx context.Context, resolved forge.ResolvedRepository, subpath, principal string, query url.Values) (any, error) {
	switch {
	case subpath == "" || subpath == "refs":
		return policy.repositoryRefsForRead(ctx, resolved, "", query.Get("continuation"), principal)
	case subpath == "refs/branches" || subpath == "refs/tags":
		kind := strings.TrimPrefix(subpath, "refs/")
		return policy.repositoryRefsForRead(ctx, resolved, kind, query.Get("continuation"), principal)
	case subpath == "resolve" || subpath == "resolve/":
		request := forge.RevisionRequest{Revision: "HEAD", Principal: principal}
		return policy.repositoryContentRead(ctx, resolved, "HEAD", func(backend forge.RepositoryContentBackend) (any, error) {
			return backend.Resolve(ctx, resolved.Repository, request)
		})
	case strings.HasPrefix(subpath, "resolve/"):
		revision := strings.TrimPrefix(subpath, "resolve/")
		request := forge.RevisionRequest{Revision: revision, Principal: principal}
		return policy.repositoryContentRead(ctx, resolved, revision, func(backend forge.RepositoryContentBackend) (any, error) {
			return backend.Resolve(ctx, resolved.Repository, request)
		})
	case strings.HasPrefix(subpath, "tree/"):
		revision := strings.TrimPrefix(subpath, "tree/")
		request := forge.RevisionRequest{Revision: revision, Principal: principal}
		return policy.repositoryContentRead(ctx, resolved, revision, func(backend forge.RepositoryContentBackend) (any, error) {
			return backend.Tree(ctx, resolved.Repository, request)
		})
	case strings.HasPrefix(subpath, "blob/"):
		revision := strings.TrimPrefix(subpath, "blob/")
		request := forge.RevisionRequest{Revision: revision, Principal: principal}
		return policy.repositoryContentRead(ctx, resolved, revision, func(backend forge.RepositoryContentBackend) (any, error) {
			return backend.Blob(ctx, resolved.Repository, request)
		})
	case subpath == "commits":
		skip, _ := strconv.Atoi(query.Get("skip"))
		limit, _ := strconv.Atoi(query.Get("n"))
		request := forge.CommitsRequest{Ref: query.Get("ref"), Path: query.Get("path"), Skip: skip, Limit: limit, Continuation: query.Get("continuation"), Principal: principal}
		return policy.repositoryContentRead(ctx, resolved, request.Ref, func(backend forge.RepositoryContentBackend) (any, error) {
			return backend.Commits(ctx, resolved.Repository, request)
		})
	case strings.HasPrefix(subpath, "commit/"):
		revision := strings.TrimPrefix(subpath, "commit/")
		request := forge.RevisionRequest{Revision: revision, Principal: principal}
		return policy.repositoryContentRead(ctx, resolved, revision, func(backend forge.RepositoryContentBackend) (any, error) {
			return backend.Commit(ctx, resolved.Repository, request)
		})
	default:
		return nil, &forge.Error{Status: http.StatusNotFound, Code: "git_object_not_found"}
	}
}

func (policy *GitForgePolicy) repositoryRefsForRead(ctx context.Context, resolved forge.ResolvedRepository, kind, continuation, principal string) (any, error) {
	if resolved.Repository.Integration.Replica != nil && resolved.Repository.Integration.Replica.Enabled {
		page, usable, err := policy.replicaRefPage(ctx, resolved.Repository, kind, continuation)
		if err != nil {
			return nil, err
		}
		if usable {
			return page, nil
		}
		config := resolved.Repository.Integration.Replica
		reason := "ref-read-" + strconv.FormatInt(time.Now().UTC().Unix()/5, 10)
		_ = policy.enqueueGitReplicaSync(ctx, resolved.Repository.Coordinate,
			resolved.Repository.Integration.Generation, forge.ReplicaSyncRequest{Reason: reason})
		if config.WaitMilliseconds > 0 && (config.ReadPolicy == forge.ReplicaReadWait || config.ReadPolicy == forge.ReplicaReadFallback) {
			deadline := time.Now().Add(time.Duration(config.WaitMilliseconds) * time.Millisecond)
			for time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(50 * time.Millisecond):
				}
				page, usable, err = policy.replicaRefPage(ctx, resolved.Repository, kind, continuation)
				if err != nil {
					return nil, err
				}
				if usable {
					return page, nil
				}
			}
		}
		if config.ReadPolicy == forge.ReplicaReadFallback && resolved.Authority != nil {
			return resolved.Authority.Refs(ctx, resolved.Repository, forge.RefsRequest{Kind: kind, Continuation: continuation, Principal: principal})
		}
		return nil, &forge.Error{Status: http.StatusServiceUnavailable, Code: "replica_stale", Retryable: true}
	}
	if resolved.Authority == nil {
		return nil, &forge.Error{Status: http.StatusConflict, Code: "repository_refs_unavailable"}
	}
	return resolved.Authority.Refs(ctx, resolved.Repository, forge.RefsRequest{Kind: kind, Continuation: continuation, Principal: principal})
}

// repositoryContentRead keeps the existing public API independent from the
// selected provider. A configured replica is primary; immutable commits are
// pinned into WalGit on first miss, and provider content is used only when the
// explicit read policy permits fallback.
func (policy *GitForgePolicy) repositoryContentRead(ctx context.Context, resolved forge.ResolvedRepository, address string, operation func(forge.RepositoryContentBackend) (any, error)) (any, error) {
	primary, fallback, stale, err := policy.contentBackendsForRead(ctx, resolved, address)
	if err != nil {
		return nil, err
	}
	if primary == nil {
		return nil, &forge.Error{Status: http.StatusConflict, Code: "repository_browse_unavailable"}
	}
	result, readErr := operation(primary)
	if readErr == nil {
		if stale {
			result = markStaleResult(result)
		}
		return result, nil
	}
	if resolved.Repository.Integration.Replica != nil && resolved.Repository.Integration.Replica.Enabled &&
		primary.Name() == forge.BackendWalGit && immutableAddress(address) && isGitObjectMissing(readErr) {
		oid := strings.SplitN(strings.Trim(address, "/"), "/", 2)[0]
		if importErr := policy.ensureReplicaObject(ctx, resolved.Repository, oid); importErr == nil {
			if result, retryErr := operation(primary); retryErr == nil {
				return result, nil
			} else {
				readErr = retryErr
			}
		} else {
			readErr = importErr
		}
	}
	if fallback != nil && fallback != primary {
		return operation(fallback)
	}
	return nil, readErr
}

func isGitObjectMissing(err error) bool {
	var forgeError *forge.Error
	if errors.As(err, &forgeError) {
		return forgeError.Status == http.StatusNotFound || forgeError.Code == "git_object_not_found"
	}
	var backendError *gitforge.BackendError
	return errors.As(err, &backendError) && (backendError.Status == http.StatusNotFound || backendError.Code == "git_object_not_found")
}

func writeRepositoryResult(w http.ResponseWriter, method string, result any, limits forge.ContentLimits) {
	result = applyResultLimits(result, limits)
	metadata := repositoryResultMetadata(result)
	w.Header().Set("Content-Type", "application/json")
	cache := "private, no-store"
	if metadata != nil {
		if metadata.CacheControl != "" {
			cache = metadata.CacheControl
		}
		if metadata.ETag != "" {
			w.Header().Set("ETag", metadata.ETag)
		}
		if metadata.LastModified != "" {
			w.Header().Set("Last-Modified", metadata.LastModified)
		}
		if metadata.Truncated {
			w.Header().Set("X-Lapis-Truncated", "true")
		}
	}
	w.Header().Set("Cache-Control", cache)
	w.WriteHeader(http.StatusOK)
	if method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(result)
	}
}

func applyResultLimits(result any, limits forge.ContentLimits) any {
	switch value := result.(type) {
	case forge.Revision:
		if value.CapabilityLimits == nil {
			value.CapabilityLimits = &limits
		}
		return value
	case forge.RefPage:
		if value.CapabilityLimits == nil {
			value.CapabilityLimits = &limits
		}
		return value
	case forge.Tree:
		if value.CapabilityLimits == nil {
			value.CapabilityLimits = &limits
		}
		return value
	case forge.Blob:
		if value.CapabilityLimits == nil {
			value.CapabilityLimits = &limits
		}
		return value
	case forge.CommitPage:
		if value.CapabilityLimits == nil {
			value.CapabilityLimits = &limits
		}
		return value
	case forge.CommitDetail:
		if value.CapabilityLimits == nil {
			value.CapabilityLimits = &limits
		}
		return value
	case forge.Diff:
		if value.CapabilityLimits == nil {
			value.CapabilityLimits = &limits
		}
		return value
	default:
		return result
	}
}

func repositoryResultMetadata(result any) *forge.ResultMetadata {
	switch value := result.(type) {
	case forge.Revision:
		return &value.ResultMetadata
	case forge.RefPage:
		return &value.ResultMetadata
	case forge.Tree:
		return &value.ResultMetadata
	case forge.Blob:
		return &value.ResultMetadata
	case forge.CommitPage:
		return &value.ResultMetadata
	case forge.CommitDetail:
		return &value.ResultMetadata
	case forge.Diff:
		return &value.ResultMetadata
	default:
		return nil
	}
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
	state, err := projecttask.ReducePullRequest(root, resolved.Repository.Announcement, related)
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

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	response := pullRequestDiffResponse{
		PullRequest: root.ID.Hex(), Repository: coordinate.String(), Lifecycle: state.Lifecycle,
		BaseSource: baseSource, TargetBranch: state.TargetBranch,
		Base: comparison.Base, Head: comparison.Head, MergeBase: comparison.MergeBase,
		Files: comparison.Files, Patch: comparison.Patch,
	}
	response.ResultMetadata = comparison.ResultMetadata
	if response.CapabilityLimits == nil {
		response.CapabilityLimits = &resolved.Capabilities.CapabilityLimits
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

func (policy *GitForgePolicy) resolvePublicRepository(ctx context.Context, ownerText, repositoryText string) (forge.ResolvedRepository, int, string) {
	owner, err := nostr.PubKeyFromHex(ownerText)
	if err != nil {
		return forge.ResolvedRepository{}, http.StatusNotFound, "repository_not_found"
	}
	candidates := []string{repositoryText}
	if strings.HasSuffix(repositoryText, ".git") {
		candidates = append(candidates, strings.TrimSuffix(repositoryText, ".git"))
	}
	for _, identifier := range slices.Compact(candidates) {
		repository, found, err := policy.ResolveIntegration(ctx, projecttask.RepositoryCoordinate{Owner: owner, ID: identifier})
		if err != nil {
			var backendError *forge.Error
			if errors.As(err, &backendError) {
				return forge.ResolvedRepository{}, backendError.Status, backendError.Code
			}
			return forge.ResolvedRepository{}, http.StatusServiceUnavailable, "event_store_unavailable"
		}
		if !found {
			continue
		}
		return repository, 0, ""
	}
	return forge.ResolvedRepository{}, http.StatusNotFound, "repository_not_found"
}

func (policy *GitForgePolicy) authorizeRepository(pubkey nostr.PubKey, repository forge.Repository, write bool) (int, string) {
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

func (policy *GitForgePolicy) authorizePullRequest(ctx context.Context, pubkey nostr.PubKey, root nostr.Event, repository forge.Repository) (int, string) {
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

func (policy *GitForgePolicy) requireReady(ctx context.Context, repository forge.Repository) (int, string) {
	if repository.Integration.GitBackend != forge.BackendWalGit {
		if repository.Integration.HealthState == "inactive" || repository.Integration.HealthState == "disabled" {
			return http.StatusServiceUnavailable, "repository_backend_disabled"
		}
		return 0, ""
	}
	state, found, err := policy.intentState(ctx, repository.Coordinate)
	if err != nil {
		return http.StatusServiceUnavailable, "repository_state_unavailable"
	}
	if !found {
		legacy := gitforge.Repository{
			Coordinate: repository.Coordinate, Announcement: repository.Announcement,
			Channel: repository.Channel, Class: gitforge.RepositoryClass(repository.Classification),
			CloneURL: repository.CloneURL,
		}
		if err := policy.upsertIntent(ctx, legacy); err != nil {
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
	var forgeError *forge.Error
	if errors.As(err, &forgeError) {
		status := forgeError.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		writeGitError(w, status, forgeError.Code)
		return
	}
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
