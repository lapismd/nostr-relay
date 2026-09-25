package zooid

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
)

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
