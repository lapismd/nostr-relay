package zooid

import (
	"net/http"
	"time"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
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
	if *write && subpath == "git-receive-pack" && r.Method == http.MethodPost {
		if status, code := policy.authorizeProtectedReceivePack(r, resolved, authEvent.PubKey); status != 0 {
			writeGitError(w, status, code)
			return
		}
	}

	backend, ok := resolved.Authority.(forge.SmartHTTPBackend)
	if !ok || (!resolved.Capabilities.SmartHTTP.Read && !*write) || (!resolved.Capabilities.SmartHTTP.Write && *write) {
		writeGitError(w, http.StatusConflict, "provider_git_required")
		return
	}
	backend.ServeGitHTTP(w, r, resolved.Repository, subpath, authEvent.PubKey.Hex())
}
