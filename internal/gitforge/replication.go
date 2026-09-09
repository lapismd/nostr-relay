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

// ReplicateRepository publishes the current WalGit heads/tags to one explicit
// provider target. It never configures a persistent remote and is only called
// after WalGit has become the repository's authority.
func (backend *WalGitBackend) ReplicateRepository(ctx context.Context, repository forge.Repository, target forge.GitReplicationTarget, branches, tags bool) error {
	parsedTarget, err := url.Parse(strings.TrimSpace(target.CloneURL))
	if err != nil || parsedTarget.Scheme != "https" || !strings.EqualFold(parsedTarget.Host, "github.com") || parsedTarget.User != nil || parsedTarget.RawQuery != "" || parsedTarget.Fragment != "" {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_git_replication_target"}
	}
	if target.Username == "" || target.Password == "" || strings.ContainsAny(target.Username+target.Password, "\x00\r\n") {
		return &forge.Error{Status: http.StatusConflict, Code: "git_replication_credentials_unavailable"}
	}
	if !branches && !tags {
		branches, tags = true, true
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "git_executable_unavailable"}
	}
	directory, err := os.MkdirTemp("", "lapis-git-replication-")
	if err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "git_replication_workspace_unavailable"}
	}
	defer os.RemoveAll(directory)
	bare := filepath.Join(directory, "repository.git")
	askpass := filepath.Join(directory, "askpass.sh")
	if err := os.WriteFile(askpass, []byte("#!/bin/sh\ncase \"$1\" in *sername*) printf '%s' \"$LAPIS_GIT_USERNAME\" ;; *) printf '%s' \"$LAPIS_GIT_PASSWORD\" ;; esac\n"), 0o700); err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "git_replication_workspace_unavailable"}
	}
	source := *backend.base
	source.Path = "/" + repository.Coordinate.Owner.Hex() + "/" + url.PathEscape(repository.Coordinate.ID) + ".git"
	configPath := filepath.Join(directory, "gitconfig")
	config := "[http \"" + escapeGitConfig(source.String()) + "\"]\n" +
		"\textraHeader = Authorization: Bearer " + escapeGitConfig(backend.token) + "\n" +
		"\textraHeader = X-Walgit-Principal: " + repository.Coordinate.Owner.Hex() + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "git_replication_workspace_unavailable"}
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
	refspecs := replicaRefspecs(branches, tags)
	if err := runGit(ctx, gitPath, bare, environment, append([]string{"fetch", "--prune", "source"}, refspecs...)...); err != nil {
		return err
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
	if err := runGit(ctx, gitPath, bare, environment, append([]string{"push", "--prune", "target"}, refspecs...)...); err != nil {
		return err
	}
	if err := runGit(ctx, gitPath, bare, environment, "lfs", "push", "--all", "target"); err != nil {
		return err
	}
	wanted, err := localGitRefs(ctx, gitPath, bare, environment)
	if err != nil {
		return err
	}
	wanted = filterManagedRefs(wanted, branches, tags)
	actual, err := lsRemoteRefs(ctx, gitPath, directory, environment, parsedTarget.String())
	if err != nil {
		return err
	}
	actual = filterManagedRefs(actual, branches, tags)
	if !equalRefs(wanted, actual) {
		return &forge.Error{Status: http.StatusUnprocessableEntity, Code: "git_replication_verification_failed", Retryable: true}
	}
	return nil
}

func filterManagedRefs(refs map[string]string, branches, tags bool) map[string]string {
	filtered := make(map[string]string)
	for name, oid := range refs {
		if (branches && strings.HasPrefix(name, "refs/heads/")) || (tags && strings.HasPrefix(name, "refs/tags/")) {
			filtered[name] = oid
		}
	}
	return filtered
}

var _ forge.GitReplicationSource = (*WalGitBackend)(nil)
