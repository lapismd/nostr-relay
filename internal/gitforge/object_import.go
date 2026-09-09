package gitforge

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"zooid/internal/forge"
)

// ImportObject pins one provider commit under an internal namespace. Normal
// repository ref listings expose only heads/tags, so this keeps PR-only commits
// available for repeated review without advertising another public ref.
func (backend *WalGitBackend) ImportObject(ctx context.Context, repository forge.Repository, request forge.ImportObjectRequest) error {
	source, err := url.Parse(strings.TrimSpace(request.SourceCloneURL))
	if err != nil || source.Scheme != "https" || source.Host != "github.com" || source.User != nil || source.RawQuery != "" || source.Fragment != "" {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_import_source"}
	}
	if !isFullObjectID(request.ObjectOID) || request.SourceUsername == "" || request.SourcePassword == "" || strings.ContainsAny(request.SourceUsername+request.SourcePassword, "\x00\r\n") {
		return &forge.Error{Status: http.StatusConflict, Code: "import_credentials_unavailable"}
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "git_executable_unavailable"}
	}
	if err := backend.EnsureRepository(ctx, repository.Coordinate); err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "lapis-forge-object-")
	if err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "import_workspace_unavailable"}
	}
	defer os.RemoveAll(directory)
	bare := filepath.Join(directory, "repository.git")
	askpass := filepath.Join(directory, "askpass.sh")
	if err := os.WriteFile(askpass, []byte("#!/bin/sh\ncase \"$1\" in *sername*) printf '%s' \"$LAPIS_GIT_USERNAME\" ;; *) printf '%s' \"$LAPIS_GIT_PASSWORD\" ;; esac\n"), 0o700); err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "import_workspace_unavailable"}
	}
	sourceEnv := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="+askpass, "LAPIS_GIT_USERNAME="+request.SourceUsername, "LAPIS_GIT_PASSWORD="+request.SourcePassword)
	if err := runGit(ctx, gitPath, directory, sourceEnv, "init", "--bare", bare); err != nil {
		return err
	}
	if err := runGit(ctx, gitPath, bare, sourceEnv, "remote", "add", "source", source.String()); err != nil {
		return err
	}
	oid := strings.ToLower(request.ObjectOID)
	if err := runGit(ctx, gitPath, bare, sourceEnv, "fetch", "--no-tags", "source", oid); err != nil {
		return err
	}

	destination := *backend.base
	destination.Path = "/" + repository.Coordinate.Owner.Hex() + "/" + url.PathEscape(repository.Coordinate.ID) + ".git"
	principal := strings.TrimSpace(request.DestinationPrincipal)
	if principal == "" {
		principal = repository.Coordinate.Owner.Hex()
	}
	if strings.ContainsAny(principal, "\x00\r\n") || len(principal) > 1024 {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_destination_principal"}
	}
	configPath := filepath.Join(directory, "destination.gitconfig")
	config := fmt.Sprintf("[http \"%s\"]\n\textraHeader = Authorization: Bearer %s\n\textraHeader = X-Walgit-Principal: %s\n", escapeGitConfig(destination.String()), escapeGitConfig(backend.token), escapeGitConfig(principal))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "import_workspace_unavailable"}
	}
	destinationEnv := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL="+configPath)
	pin := "refs/lapis/pins/" + oid
	if err := runGit(ctx, gitPath, bare, destinationEnv, "push", destination.String(), "+"+oid+":"+pin); err != nil {
		return err
	}
	refs, err := lsRemoteRefsIncluding(ctx, gitPath, directory, destinationEnv, destination.String(), pin)
	if err != nil {
		return err
	}
	if !strings.EqualFold(refs[pin], oid) {
		return &forge.Error{Status: http.StatusUnprocessableEntity, Code: "import_destination_verification_failed"}
	}
	return nil
}

func lsRemoteRefsIncluding(ctx context.Context, gitPath, directory string, env []string, remote string, patterns ...string) (map[string]string, error) {
	arguments := append([]string{"ls-remote", remote}, patterns...)
	command := exec.CommandContext(ctx, gitPath, arguments...)
	command.Dir, command.Env = directory, env
	output, err := command.Output()
	if err != nil {
		return nil, &forge.Error{Status: http.StatusBadGateway, Code: "git_ref_snapshot_failed", Retryable: true}
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !isFullObjectID(fields[0]) {
			return nil, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_git_ref_snapshot"}
		}
		refs[fields[1]] = strings.ToLower(fields[0])
	}
	return refs, nil
}

var _ forge.RepositoryObjectImporter = (*WalGitBackend)(nil)
