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
	workspace, err := backend.openCoordinatorClone(ctx, repository, request.Principal)
	if err != nil {
		return forge.CollaborationResult{}, err
	}
	defer workspace.close()
	gitPath, work, directory, env := workspace.gitPath, workspace.work, workspace.directory, workspace.env
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
	if request.ReviewRef != "" {
		if err := materializeReviewRef(ctx, gitPath, work, directory, env, workspace.remote, request.ReviewRef, request.ExpectedHead); err != nil {
			return forge.CollaborationResult{}, err
		}
	} else if err := fetchMergeHead(ctx, gitPath, work, env, request.ExpectedHead); err != nil {
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
	remote, err := lsRemoteRefsIncluding(ctx, gitPath, directory, env, workspace.remote, target)
	if err != nil {
		return forge.CollaborationResult{}, err
	}
	if !strings.EqualFold(remote[target], merged) {
		return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "merge_ref_unconfirmed"}
	}
	return forge.CollaborationResult{State: "merged", MergedCommit: merged}, nil
}

type coordinatorClone struct {
	gitPath   string
	directory string
	work      string
	env       []string
	remote    string
	close     func()
}

func (backend *WalGitBackend) openCoordinatorClone(ctx context.Context, repository forge.Repository, principal string) (coordinatorClone, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return coordinatorClone{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "git_executable_unavailable"}
	}
	directory, err := os.MkdirTemp("", "lapis-walgit-merge-")
	if err != nil {
		return coordinatorClone{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "merge_workspace_unavailable"}
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	work := filepath.Join(directory, "work")
	destination := *backend.base
	destination.Path = "/" + repository.Coordinate.Owner.Hex() + "/" + url.PathEscape(repository.Coordinate.ID) + ".git"
	principal = strings.TrimSpace(principal)
	if principal == "" {
		principal = MergeCoordinatorPrincipal(repository.Coordinate.Owner.Hex(), repository.Coordinate.ID)
	}
	configPath := filepath.Join(directory, "destination.gitconfig")
	if err := os.WriteFile(configPath, []byte(mergeCoordinatorGitConfig(destination.String(), backend.token, principal)), 0o600); err != nil {
		cleanup()
		return coordinatorClone{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "merge_workspace_unavailable"}
	}
	env := mergeCoordinatorGitEnv(configPath)
	if err := runGit(ctx, gitPath, directory, env, "clone", "--", destination.String(), work); err != nil {
		cleanup()
		return coordinatorClone{}, err
	}
	return coordinatorClone{
		gitPath: gitPath, directory: directory, work: work, env: env, remote: destination.String(), close: cleanup,
	}, nil
}

func (backend *WalGitBackend) PublishReviewRef(ctx context.Context, repository forge.Repository, principal, reviewRef, head string) error {
	if !isFullObjectID(head) || strings.TrimSpace(reviewRef) == "" {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_merge_request"}
	}
	workspace, err := backend.openCoordinatorClone(ctx, repository, principal)
	if err != nil {
		return err
	}
	defer workspace.close()
	return materializeReviewRef(ctx, workspace.gitPath, workspace.work, workspace.directory, workspace.env, workspace.remote, reviewRef, head)
}

func (adapter *WalGitAdapter) PublishReviewRef(ctx context.Context, repository forge.Repository, principal, reviewRef, head string) error {
	backend, ok := adapter.legacy.(*WalGitBackend)
	if !ok {
		return &forge.Error{Status: http.StatusNotImplemented, Code: "walgit_merge_unavailable"}
	}
	return backend.PublishReviewRef(ctx, repository, principal, reviewRef, head)
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
