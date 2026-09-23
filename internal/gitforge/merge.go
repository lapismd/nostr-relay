package gitforge

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"zooid/internal/forge"
)

func (backend *WalGitBackend) MergeProtectedRef(ctx context.Context, repository forge.Repository, request forge.ProtectedRefMergeRequest) (forge.CollaborationResult, error) {
	if !isFullObjectID(request.ExpectedHead) || strings.TrimSpace(request.TargetRef) == "" {
		return forge.CollaborationResult{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_merge_request"}
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return forge.CollaborationResult{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "git_executable_unavailable"}
	}
	directory, err := os.MkdirTemp("", "lapis-walgit-merge-")
	if err != nil {
		return forge.CollaborationResult{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "merge_workspace_unavailable"}
	}
	defer os.RemoveAll(directory)
	work := filepath.Join(directory, "work")
	destination := *backend.base
	destination.Path = "/" + repository.Coordinate.Owner.Hex() + "/" + url.PathEscape(repository.Coordinate.ID) + ".git"
	principal := strings.TrimSpace(request.Principal)
	if principal == "" {
		principal = MergeCoordinatorPrincipal(repository.Coordinate.Owner.Hex(), repository.Coordinate.ID)
	}
	configPath := filepath.Join(directory, "destination.gitconfig")
	if err := os.WriteFile(configPath, []byte(mergeCoordinatorGitConfig(destination.String(), backend.token, principal)), 0o600); err != nil {
		return forge.CollaborationResult{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "merge_workspace_unavailable"}
	}
	env := mergeCoordinatorGitEnv(configPath)
	if err := runGit(ctx, gitPath, directory, env, "clone", "--", destination.String(), work); err != nil {
		return forge.CollaborationResult{}, err
	}
	target := NormalizeRefName(request.TargetRef)
	branch := strings.TrimPrefix(target, "refs/heads/")
	if err := runGit(ctx, gitPath, work, env, "checkout", "--force", branch); err != nil {
		if err := runGit(ctx, gitPath, work, env, "checkout", "--force", "-B", branch, "origin/"+branch); err != nil {
			return forge.CollaborationResult{}, err
		}
	}
	if request.TargetSHA != "" {
		current, err := runGitOutput(ctx, gitPath, work, env, "rev-parse", "HEAD")
		if err != nil || !strings.EqualFold(strings.TrimSpace(current), request.TargetSHA) {
			return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "protected_target_changed"}
		}
	}
	if err := fetchMergeHead(ctx, gitPath, work, env, request.ExpectedHead); err != nil {
		return forge.CollaborationResult{}, err
	}
	message := strings.TrimSpace(request.Message)
	if message == "" {
		message = strings.TrimSpace(request.Title)
	}
	if message == "" {
		message = "Merge pull request"
	}
	method := request.Method
	if method == "" {
		method = "merge"
	}
	if err := applyProtectedMerge(ctx, gitPath, work, env, method, message, request.ExpectedHead); err != nil {
		return forge.CollaborationResult{}, err
	}
	merged, err := runGitOutput(ctx, gitPath, work, env, "rev-parse", "HEAD")
	if err != nil {
		return forge.CollaborationResult{}, err
	}
	merged = strings.ToLower(strings.TrimSpace(merged))
	if err := runGit(ctx, gitPath, work, env, "push", "origin", "HEAD:"+target); err != nil {
		return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "protected_ref_update_denied"}
	}
	remote, err := lsRemoteRefsIncluding(ctx, gitPath, directory, env, destination.String(), target)
	if err != nil {
		return forge.CollaborationResult{}, err
	}
	if !strings.EqualFold(remote[target], merged) {
		return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "merge_ref_unconfirmed"}
	}
	return forge.CollaborationResult{State: "merged", MergedCommit: merged}, nil
}

func (adapter *WalGitAdapter) MergeProtectedRef(ctx context.Context, repository forge.Repository, request forge.ProtectedRefMergeRequest) (forge.CollaborationResult, error) {
	backend, ok := adapter.legacy.(*WalGitBackend)
	if !ok {
		return forge.CollaborationResult{}, &forge.Error{Status: http.StatusNotImplemented, Code: "walgit_merge_unavailable"}
	}
	return backend.MergeProtectedRef(ctx, repository, request)
}

func runGitOutput(ctx context.Context, gitPath, directory string, env []string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, gitPath, arguments...)
	command.Dir, command.Env = directory, env
	output, err := command.Output()
	if err != nil {
		return "", &forge.Error{Status: http.StatusBadGateway, Code: "git_operation_failed", Retryable: true}
	}
	return string(output), nil
}

var _ forge.ProtectedRefMerger = (*WalGitBackend)(nil)
var _ forge.ProtectedRefMerger = (*WalGitAdapter)(nil)
