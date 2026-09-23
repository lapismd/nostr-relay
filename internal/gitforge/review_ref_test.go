package gitforge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"github.com/stretchr/testify/require"
	"zooid/internal/forge"
)

func TestLogReviewRef(t *testing.T) {
	author := nostr.Generate().Public()
	changeID := strings.Repeat("ab", 16)
	branch := "review/" + nip19.EncodeNpub(author) + "/" + strings.ToUpper(changeID)
	ref, ok := LogReviewRef(branch, author)
	require.True(t, ok)
	require.Equal(t, "refs/heads/review/"+nip19.EncodeNpub(author)+"/"+changeID, ref)
	require.True(t, IsLogReviewBranchName(branch))

	_, ok = LogReviewRef(branch, nostr.Generate().Public())
	require.False(t, ok)
	_, ok = LogReviewRef("review/npub1notvalid/"+changeID, author)
	require.False(t, ok)
	_, ok = LogReviewRef("feature/files", author)
	require.False(t, ok)
	require.False(t, IsLogReviewBranchName("feature/files"))
}

func TestMaterializeReviewRefPublishesCarrierCommit(t *testing.T) {
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
	author := nostr.Generate().Public()
	reviewRef, ok := LogReviewRef("review/"+nip19.EncodeNpub(author)+"/"+strings.Repeat("cd", 16), author)
	require.True(t, ok)
	require.NoError(t, materializeReviewRef(context.Background(), gitPath, work, root, env, remote, reviewRef, head))
	published := strings.TrimSpace(runTestGitOutput(t, gitPath, remote, env, "rev-parse", reviewRef))
	require.Equal(t, head, published)
	require.NoError(t, applyProtectedMerge(context.Background(), gitPath, work, env, "merge", "Merge pull request", head))
}

func TestMaterializeReviewRefSkipsMissingCommit(t *testing.T) {
	gitPath := lookPathGit(t)
	root := t.TempDir()
	env := testGitEnv()
	repo := filepath.Join(root, "repo")
	initCommitRepo(t, gitPath, repo, env, "base.txt", "base\n", "base")
	author := nostr.Generate().Public()
	reviewRef, ok := LogReviewRef("review/"+nip19.EncodeNpub(author)+"/"+strings.Repeat("ef", 16), author)
	require.True(t, ok)
	err := materializeReviewRef(context.Background(), gitPath, repo, root, env, repo, reviewRef, strings.Repeat("a", 40))
	var forgeErr *forge.Error
	require.ErrorAs(t, err, &forgeErr)
	require.Equal(t, "merge_head_not_found", forgeErr.Code)
	require.Error(t, runGit(context.Background(), gitPath, repo, env, "rev-parse", "--verify", reviewRef))
}
