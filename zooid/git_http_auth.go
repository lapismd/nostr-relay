package zooid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

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
