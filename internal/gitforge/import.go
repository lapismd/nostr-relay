package gitforge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"zooid/internal/forge"
)

func (backend *WalGitBackend) ImportRepository(ctx context.Context, repository forge.Repository, request forge.ImportRequest) (forge.ImportResult, error) {
	source, err := url.Parse(strings.TrimSpace(request.SourceCloneURL))
	if err != nil || source.Scheme != "https" || source.Host != "github.com" || source.User != nil || source.RawQuery != "" || source.Fragment != "" {
		return forge.ImportResult{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_import_source"}
	}
	if request.SourceUsername == "" || request.SourcePassword == "" || strings.ContainsAny(request.SourceUsername+request.SourcePassword, "\x00\r\n") {
		return forge.ImportResult{}, &forge.Error{Status: http.StatusConflict, Code: "import_credentials_unavailable"}
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return forge.ImportResult{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "git_executable_unavailable"}
	}
	if err := backend.EnsureRepository(ctx, repository.Coordinate); err != nil {
		return forge.ImportResult{}, err
	}
	directory, err := os.MkdirTemp("", "lapis-forge-import-")
	if err != nil {
		return forge.ImportResult{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "import_workspace_unavailable"}
	}
	defer os.RemoveAll(directory)
	bare := filepath.Join(directory, "repository.git")
	askpass := filepath.Join(directory, "askpass.sh")
	askpassScript := []byte("#!/bin/sh\ncase \"$1\" in *sername*) printf '%s' \"$LAPIS_GIT_USERNAME\" ;; *) printf '%s' \"$LAPIS_GIT_PASSWORD\" ;; esac\n")
	if err := os.WriteFile(askpass, askpassScript, 0o700); err != nil {
		return forge.ImportResult{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "import_workspace_unavailable"}
	}
	sourceEnv := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="+askpass,
		"LAPIS_GIT_USERNAME="+request.SourceUsername, "LAPIS_GIT_PASSWORD="+request.SourcePassword,
	)
	if err := runGit(ctx, gitPath, directory, sourceEnv, "init", "--bare", bare); err != nil {
		return forge.ImportResult{}, err
	}
	if err := runGit(ctx, gitPath, bare, sourceEnv, "remote", "add", "source", source.String()); err != nil {
		return forge.ImportResult{}, err
	}
	if err := runGit(ctx, gitPath, bare, sourceEnv, "fetch", "--prune", "source",
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return forge.ImportResult{}, err
	}
	if err := runGit(ctx, gitPath, bare, sourceEnv, "lfs", "install", "--local"); err != nil {
		return forge.ImportResult{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "git_lfs_unavailable"}
	}
	if err := runGit(ctx, gitPath, bare, sourceEnv, "lfs", "fetch", "--all", "source"); err != nil {
		return forge.ImportResult{}, err
	}
	if err := runGit(ctx, gitPath, bare, sourceEnv, "fsck", "--full", "--no-dangling"); err != nil {
		return forge.ImportResult{}, &forge.Error{Status: http.StatusUnprocessableEntity, Code: "import_object_verification_failed"}
	}
	refs, err := localGitRefs(ctx, gitPath, bare, sourceEnv)
	if err != nil {
		return forge.ImportResult{}, err
	}
	if !equalRefs(refs, request.ExpectedRefs) {
		return forge.ImportResult{}, &forge.Error{Status: http.StatusConflict, Code: "import_ref_snapshot_mismatch"}
	}

	destination := *backend.base
	destination.Path = "/" + repository.Coordinate.Owner.Hex() + "/" + url.PathEscape(repository.Coordinate.ID) + ".git"
	configPath := filepath.Join(directory, "destination.gitconfig")
	config := fmt.Sprintf("[http \"%s\"]\n\textraHeader = Authorization: Bearer %s\n\textraHeader = X-Walgit-Principal: %s\n",
		escapeGitConfig(destination.String()), escapeGitConfig(backend.token), repository.Coordinate.Owner.Hex())
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return forge.ImportResult{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "import_workspace_unavailable"}
	}
	destinationEnv := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL="+configPath)
	if err := runGit(ctx, gitPath, bare, destinationEnv, "remote", "add", "destination", destination.String()); err != nil {
		return forge.ImportResult{}, err
	}
	if err := runGit(ctx, gitPath, bare, destinationEnv, "push", "--mirror", "destination"); err != nil {
		return forge.ImportResult{}, err
	}
	if err := runGit(ctx, gitPath, bare, destinationEnv, "lfs", "push", "--all", "destination"); err != nil {
		return forge.ImportResult{}, err
	}
	remoteRefs, err := lsRemoteRefs(ctx, gitPath, directory, destinationEnv, destination.String())
	if err != nil {
		return forge.ImportResult{}, err
	}
	if !equalRefs(remoteRefs, request.ExpectedRefs) {
		return forge.ImportResult{}, &forge.Error{Status: http.StatusUnprocessableEntity, Code: "import_destination_verification_failed"}
	}
	return forge.ImportResult{Refs: remoteRefs}, nil
}

func runGit(ctx context.Context, gitPath, directory string, env []string, arguments ...string) error {
	command := exec.CommandContext(ctx, gitPath, arguments...)
	command.Dir = directory
	command.Env = env
	var output limitedBuffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return &forge.Error{Status: http.StatusGatewayTimeout, Code: "git_operation_timed_out", Retryable: true}
		}
		return &forge.Error{Status: http.StatusBadGateway, Code: "git_operation_failed", Retryable: true}
	}
	return nil
}

type limitedBuffer struct {
	buffer bytes.Buffer
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	remaining := (64 << 10) - buffer.buffer.Len()
	if remaining > 0 {
		_, _ = buffer.buffer.Write(value[:min(len(value), remaining)])
	}
	return len(value), nil
}

func localGitRefs(ctx context.Context, gitPath, directory string, env []string) (map[string]string, error) {
	command := exec.CommandContext(ctx, gitPath, "for-each-ref", "--format=%(objectname)%09%(refname)", "refs/heads", "refs/tags")
	command.Dir, command.Env = directory, env
	output, err := command.Output()
	if err != nil {
		return nil, &forge.Error{Status: http.StatusBadGateway, Code: "git_ref_snapshot_failed", Retryable: true}
	}
	return parseRefOutput(output)
}

func lsRemoteRefs(ctx context.Context, gitPath, directory string, env []string, remote string) (map[string]string, error) {
	command := exec.CommandContext(ctx, gitPath, "ls-remote", remote, "refs/heads/*", "refs/tags/*")
	command.Dir, command.Env = directory, env
	output, err := command.Output()
	if err != nil {
		return nil, &forge.Error{Status: http.StatusBadGateway, Code: "git_ref_snapshot_failed", Retryable: true}
	}
	return parseRefOutput(output)
}

func parseRefOutput(output []byte) (map[string]string, error) {
	refs := make(map[string]string)
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		fields := bytes.Fields(line)
		if len(fields) != 2 {
			return nil, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_git_ref_snapshot"}
		}
		oid, ref := string(fields[0]), string(fields[1])
		if strings.HasSuffix(ref, "^{}") {
			continue
		}
		if !isFullObjectID(oid) || !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
			return nil, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_git_ref_snapshot"}
		}
		refs[ref] = strings.ToLower(oid)
	}
	return refs, nil
}

func equalRefs(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	keys := make([]string, 0, len(left))
	for ref := range left {
		keys = append(keys, ref)
	}
	sort.Strings(keys)
	for _, ref := range keys {
		if !strings.EqualFold(left[ref], right[ref]) {
			return false
		}
	}
	return true
}

func escapeGitConfig(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

var _ io.Writer = (*limitedBuffer)(nil)
