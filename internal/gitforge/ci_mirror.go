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

func (backend *WalGitBackend) MirrorCommit(ctx context.Context, repository forge.Repository, commit string, target forge.CIMirrorTarget, mirrorRef string) error {
	parsedTarget, err := url.Parse(strings.TrimSpace(target.CloneURL))
	if err != nil || parsedTarget.Scheme != "https" || !strings.EqualFold(parsedTarget.Host, "github.com") || parsedTarget.User != nil || parsedTarget.RawQuery != "" || parsedTarget.Fragment != "" {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_ci_mirror_target"}
	}
	if !isFullObjectID(commit) || !validCIMirrorRef(mirrorRef) || target.Username == "" || target.Password == "" || strings.ContainsAny(target.Username+target.Password, "\x00\r\n") {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_ci_mirror_request"}
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "git_executable_unavailable"}
	}
	directory, err := os.MkdirTemp("", "lapis-ci-mirror-")
	if err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "ci_mirror_workspace_unavailable"}
	}
	defer os.RemoveAll(directory)
	bare := filepath.Join(directory, "repository.git")
	askpass := filepath.Join(directory, "askpass.sh")
	askpassScript := []byte("#!/bin/sh\ncase \"$1\" in *sername*) printf '%s' \"$LAPIS_GIT_USERNAME\" ;; *) printf '%s' \"$LAPIS_GIT_PASSWORD\" ;; esac\n")
	if err := os.WriteFile(askpass, askpassScript, 0o700); err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "ci_mirror_workspace_unavailable"}
	}
	source := *backend.base
	source.Path = "/" + repository.Coordinate.Owner.Hex() + "/" + url.PathEscape(repository.Coordinate.ID) + ".git"
	configPath := filepath.Join(directory, "source.gitconfig")
	config := "[http \"" + escapeGitConfig(source.String()) + "\"]\n" +
		"\textraHeader = Authorization: Bearer " + escapeGitConfig(backend.token) + "\n" +
		"\textraHeader = X-Walgit-Principal: " + repository.Coordinate.Owner.Hex() + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "ci_mirror_workspace_unavailable"}
	}
	environment := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL="+configPath, "GIT_ASKPASS="+askpass,
		"LAPIS_GIT_USERNAME="+target.Username, "LAPIS_GIT_PASSWORD="+target.Password,
	)
	if err := runGit(ctx, gitPath, directory, environment, "init", "--bare", bare); err != nil {
		return err
	}
	if err := runGit(ctx, gitPath, bare, environment, "remote", "add", "source", source.String()); err != nil {
		return err
	}
	if err := runGit(ctx, gitPath, bare, environment, "fetch", "--prune", "source", "+refs/heads/*:refs/remotes/source/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return err
	}
	if err := runGit(ctx, gitPath, bare, environment, "cat-file", "-e", strings.ToLower(commit)+"^{commit}"); err != nil {
		return &forge.Error{Status: http.StatusConflict, Code: "ci_commit_not_found"}
	}
	if err := runGit(ctx, gitPath, bare, environment, "lfs", "install", "--local"); err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "git_lfs_unavailable"}
	}
	if err := runGit(ctx, gitPath, bare, environment, "lfs", "fetch", "--all", "source"); err != nil {
		return err
	}
	if err := runGit(ctx, gitPath, bare, environment, "remote", "add", "target", parsedTarget.String()); err != nil {
		return err
	}
	if err := runGit(ctx, gitPath, bare, environment, "push", "target", strings.ToLower(commit)+":"+mirrorRef); err != nil {
		return err
	}
	if err := runGit(ctx, gitPath, bare, environment, "lfs", "push", "--all", "target"); err != nil {
		return err
	}
	return nil
}

func validCIMirrorRef(value string) bool {
	const prefix = "refs/heads/lapis-ci/"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	identifier := strings.TrimPrefix(value, prefix)
	if identifier == "" || len(identifier) > 100 {
		return false
	}
	for _, character := range identifier {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_') {
			return false
		}
	}
	return true
}
