package gitforge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"zooid/internal/forge"
)

func TestFetchMergeHeadImportsJJSyncCommit(t *testing.T) {
	gitPath := lookPathGit(t)
	root := t.TempDir()
	env := testGitEnv()
	seed := filepath.Join(root, "seed")
	initCommitRepo(t, gitPath, seed, env, "base.txt", "base\n", "base")
	base := strings.TrimSpace(runTestGitOutput(t, gitPath, seed, env, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(seed, "dest-git.txt"), "snapshot\n")
	runTestGit(t, gitPath, seed, env, "add", "dest-git.txt")
	runTestGit(t, gitPath, seed, env, "commit", "-m", "dest-Git snapshot")
	head := strings.TrimSpace(runTestGitOutput(t, gitPath, seed, env, "rev-parse", "HEAD"))
	remote := filepath.Join(root, "dest.git")
	runTestGit(t, gitPath, root, env, "clone", "--bare", seed, remote)
	runTestGit(t, gitPath, remote, env, "update-ref", "refs/heads/main", base)
	runTestGit(t, gitPath, remote, env, "update-ref", "refs/jj-sync/v1/scope/op/digest", head)
	runTestGit(t, gitPath, remote, env, "config", "uploadpack.allowReachableSHA1InWant", "true")
	work := filepath.Join(root, "work")
	runTestGit(t, gitPath, root, env, "clone", "--no-local", remote, work)
	runTestGit(t, gitPath, work, env, "config", "user.name", "test")
	runTestGit(t, gitPath, work, env, "config", "user.email", "test@example.com")
	require.False(t, gitHasCommit(context.Background(), gitPath, work, env, head))
	_, err := runGitCombined(context.Background(), gitPath, work, env, "merge", "--no-ff", "-m", "Merge pull request", head)
	require.Error(t, err)
	require.NoError(t, fetchMergeHead(context.Background(), gitPath, work, env, head))
	require.True(t, gitHasCommit(context.Background(), gitPath, work, env, head))
	require.NoError(t, applyProtectedMerge(context.Background(), gitPath, work, env, "merge", "Merge pull request", head))
	require.FileExists(t, filepath.Join(work, "dest-git.txt"))
}

func TestApplyProtectedMergeAllowsUnrelatedHistories(t *testing.T) {
	gitPath := lookPathGit(t)
	root := t.TempDir()
	env := testGitEnv()
	target := filepath.Join(root, "target")
	source := filepath.Join(root, "source")
	initCommitRepo(t, gitPath, target, env, "target.txt", "target\n", "target root")
	initCommitRepo(t, gitPath, source, env, "dest-git.txt", "snapshot\n", "dest-Git root")
	head := strings.TrimSpace(runTestGitOutput(t, gitPath, source, env, "rev-parse", "HEAD"))
	runTestGit(t, gitPath, target, env, "fetch", source, head)
	output, err := runGitCombined(context.Background(), gitPath, target, env, "merge", "--no-ff", "-m", "Merge pull request", head)
	require.Error(t, err)
	require.Equal(t, "unrelated_histories", mergeFailureCode(output))
	require.NoError(t, applyProtectedMerge(context.Background(), gitPath, target, env, "merge", "Merge pull request", head))
	require.FileExists(t, filepath.Join(target, "dest-git.txt"))
	require.FileExists(t, filepath.Join(target, "target.txt"))
}

func TestFetchMergeHeadMissingCommit(t *testing.T) {
	gitPath := lookPathGit(t)
	root := t.TempDir()
	env := testGitEnv()
	repo := filepath.Join(root, "repo")
	initCommitRepo(t, gitPath, repo, env, "base.txt", "base\n", "base")
	err := fetchMergeHead(context.Background(), gitPath, repo, env, strings.Repeat("a", 40))
	require.Error(t, err)
	var forgeErr *forge.Error
	require.ErrorAs(t, err, &forgeErr)
	require.Equal(t, "merge_head_not_found", forgeErr.Code)
}

func lookPathGit(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)
	return gitPath
}

func testGitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func initCommitRepo(t *testing.T, gitPath, directory string, env []string, name, contents, message string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(directory, 0o700))
	runTestGit(t, gitPath, directory, env, "init", "--initial-branch=main")
	runTestGit(t, gitPath, directory, env, "config", "user.name", "test")
	runTestGit(t, gitPath, directory, env, "config", "user.email", "test@example.com")
	writeFile(t, filepath.Join(directory, name), contents)
	runTestGit(t, gitPath, directory, env, "add", name)
	runTestGit(t, gitPath, directory, env, "commit", "-m", message)
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
}

func runTestGit(t *testing.T, gitPath, directory string, env []string, arguments ...string) {
	t.Helper()
	require.NoError(t, runGit(context.Background(), gitPath, directory, env, arguments...))
}

func runTestGitOutput(t *testing.T, gitPath, directory string, env []string, arguments ...string) string {
	t.Helper()
	output, err := runGitOutput(context.Background(), gitPath, directory, env, arguments...)
	require.NoError(t, err)
	return output
}
