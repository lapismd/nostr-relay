package zooid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"github.com/stretchr/testify/require"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

type fakeRelayMerger struct {
	requests  []forge.ProtectedRefMergeRequest
	published []string
	result    forge.CollaborationResult
}

func (merger *fakeRelayMerger) MergeProtectedRef(_ context.Context, _ forge.Repository, request forge.ProtectedRefMergeRequest) (forge.CollaborationResult, error) {
	merger.requests = append(merger.requests, request)
	return merger.result, nil
}

func (merger *fakeRelayMerger) PublishReviewRef(_ context.Context, _ forge.Repository, _, reviewRef, head string) error {
	merger.published = append(merger.published, reviewRef+"@"+head)
	return nil
}

func TestRelayMergeRequiresMaintainerApprovalAndUsesCoordinator(t *testing.T) {
	backend := &recordingGitBackend{
		refsJSON: `{"head":{"name":"refs/heads/main","sha":"` + strings.Repeat("a", 40) + `"},"refs":[{"name":"refs/heads/main","sha":"` + strings.Repeat("a", 40) + `"}]}`,
	}
	policy, store := createGitForgeTestPolicy(t, backend)
	merger := &fakeRelayMerger{result: forge.CollaborationResult{State: "merged", MergedCommit: strings.Repeat("c", 40)}}
	policy.RelayMerger = merger
	owner := nostr.Generate()
	author := nostr.Generate()
	maintainer := nostr.Generate()
	repository := readyHostedRepository(t, policy, store, owner, "notes", "repo-channel")
	grantGitGroup(policy, "repo-channel", author.Public())
	grantGitGroup(policy, "repo-channel", maintainer.Public())
	policy.ProjectTasks.Groups.SetMemberRoles("repo-channel", maintainer.Public(), []string{"maintainer"})
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	head := strings.Repeat("b", 40)
	root := nostr.Event{Kind: projecttask.KindPullRequest, CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"a", coordinate.String()}, {"p", repository.PubKey.Hex()}, {"subject", "Merge me"},
		{"c", head}, {"clone", "https://relay.example/git/repo.git"}, {"target-branch", "main"},
	}}
	require.NoError(t, root.Sign(author))
	require.NoError(t, store.SaveEvent(root))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), root))

	body, _ := json.Marshal(map[string]string{"expected_head": head, "method": "merge"})
	target := "https://relay.example/api/forge/pull-requests/" + root.ID.Hex() + "/merge"
	request := signedForgeRequest(t, http.MethodPost, target, body, maintainer)
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, request)
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Contains(t, recorder.Body.String(), "maintainer_approval_required")

	approval := nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Now(), Content: "lgtm", Tags: nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"t", "approval"}, {"c", head},
	}}
	require.NoError(t, approval.Sign(maintainer))
	require.NoError(t, store.SaveEvent(approval))

	request = signedForgeRequest(t, http.MethodPost, target, body, maintainer)
	recorder = httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, request)
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Len(t, merger.requests, 1)
	require.True(t, strings.HasPrefix(merger.requests[0].Principal, "merge-coordinator:"))
	require.Equal(t, "refs/heads/main", merger.requests[0].TargetRef)
}

func TestRelayMergeUsesExtraProtectedTarget(t *testing.T) {
	headSHA := strings.Repeat("a", 40)
	backend := &recordingGitBackend{
		refsJSON: `{"head":{"name":"refs/heads/main","sha":"` + headSHA + `"},"refs":[{"name":"refs/heads/main","sha":"` + headSHA + `"},{"name":"refs/heads/release/1.0","sha":"` + headSHA + `"}]}`,
	}
	policy, store := createGitForgeTestPolicy(t, backend)
	merger := &fakeRelayMerger{result: forge.CollaborationResult{State: "merged", MergedCommit: strings.Repeat("c", 40)}}
	policy.RelayMerger = merger
	owner := nostr.Generate()
	author := nostr.Generate()
	maintainer := nostr.Generate()
	repository := readyHostedRepository(t, policy, store, owner, "notes", "repo-channel", nostr.Tag{projecttask.TagProtectedRef, "release/*"})
	grantGitGroup(policy, "repo-channel", author.Public())
	grantGitGroup(policy, "repo-channel", maintainer.Public())
	policy.ProjectTasks.Groups.SetMemberRoles("repo-channel", maintainer.Public(), []string{"maintainer"})
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	head := strings.Repeat("b", 40)
	root := nostr.Event{Kind: projecttask.KindPullRequest, CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"a", coordinate.String()}, {"p", repository.PubKey.Hex()}, {"subject", "Release"},
		{"c", head}, {"clone", "https://relay.example/git/repo.git"}, {"target-branch", "release/1.0"},
	}}
	require.NoError(t, root.Sign(author))
	require.NoError(t, store.SaveEvent(root))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), root))
	approval := nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Now(), Content: "lgtm", Tags: nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"t", "approval"}, {"c", head},
	}}
	require.NoError(t, approval.Sign(maintainer))
	require.NoError(t, store.SaveEvent(approval))
	body, _ := json.Marshal(map[string]string{"expected_head": head, "method": "merge"})
	target := "https://relay.example/api/forge/pull-requests/" + root.ID.Hex() + "/merge"
	request := signedForgeRequest(t, http.MethodPost, target, body, maintainer)
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, request)
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Len(t, merger.requests, 1)
	require.Equal(t, "refs/heads/release/1.0", merger.requests[0].TargetRef)
}

func TestRelayMergeTreatsMissingTargetAsLiveDefault(t *testing.T) {
	headSHA := strings.Repeat("a", 40)
	backend := &recordingGitBackend{
		refsJSON: `{"head":{"name":"refs/heads/trunk","sha":"` + headSHA + `"},"refs":[{"name":"refs/heads/trunk","sha":"` + headSHA + `"}]}`,
	}
	policy, store := createGitForgeTestPolicy(t, backend)
	merger := &fakeRelayMerger{result: forge.CollaborationResult{State: "merged", MergedCommit: strings.Repeat("c", 40)}}
	policy.RelayMerger = merger
	owner := nostr.Generate()
	author := nostr.Generate()
	maintainer := nostr.Generate()
	repository := readyHostedRepository(t, policy, store, owner, "notes", "repo-channel")
	grantGitGroup(policy, "repo-channel", author.Public())
	grantGitGroup(policy, "repo-channel", maintainer.Public())
	policy.ProjectTasks.Groups.SetMemberRoles("repo-channel", maintainer.Public(), []string{"maintainer"})
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	head := strings.Repeat("b", 40)
	root := nostr.Event{Kind: projecttask.KindPullRequest, CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"a", coordinate.String()}, {"p", repository.PubKey.Hex()}, {"subject", "Log review"},
		{"c", head}, {"clone", "https://relay.example/git/repo.git"},
	}}
	require.NoError(t, root.Sign(author))
	require.NoError(t, store.SaveEvent(root))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), root))
	approval := nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Now(), Content: "lgtm", Tags: nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"t", "approval"}, {"c", head},
	}}
	require.NoError(t, approval.Sign(maintainer))
	require.NoError(t, store.SaveEvent(approval))
	body, _ := json.Marshal(map[string]string{"expected_head": head, "method": "merge"})
	target := "https://relay.example/api/forge/pull-requests/" + root.ID.Hex() + "/merge"
	request := signedForgeRequest(t, http.MethodPost, target, body, maintainer)
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, request)
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Len(t, merger.requests, 1)
	require.Equal(t, "refs/heads/trunk", merger.requests[0].TargetRef)
	require.Empty(t, merger.requests[0].ReviewRef)
}

func TestRelayMergePassesLogReviewRef(t *testing.T) {
	author := nostr.Generate()
	merger := enqueueHostedMerge(t, author, logReviewBranch(author.Public(), strings.Repeat("ab", 16)))
	require.Len(t, merger.requests, 1)
	ref, ok := gitforge.LogReviewRef(logReviewBranch(author.Public(), strings.Repeat("ab", 16)), author.Public())
	require.True(t, ok)
	require.Equal(t, ref, merger.requests[0].ReviewRef)
	require.Equal(t, []string{ref + "@" + strings.Repeat("b", 40)}, merger.published)
}

func TestRelayMergeRejectsForeignReviewRef(t *testing.T) {
	author := nostr.Generate()
	merger, err := enqueueHostedMergeResult(t, author, logReviewBranch(nostr.Generate().Public(), strings.Repeat("cd", 16)), "")
	var forgeErr *forge.Error
	require.ErrorAs(t, err, &forgeErr)
	require.Equal(t, "invalid_merge_request", forgeErr.Code)
	require.Empty(t, merger.requests)
}

func logReviewBranch(author nostr.PubKey, changeID string) string {
	return "review/" + nip19.EncodeNpub(author) + "/" + changeID
}

func enqueueHostedMerge(t *testing.T, author nostr.SecretKey, branch string) *fakeRelayMerger {
	t.Helper()
	merger, err := enqueueHostedMergeResult(t, author, branch, "")
	require.NoError(t, err)
	return merger
}

func TestRelayMergeLogReviewKeepsMainWhenBranchListOmitsHead(t *testing.T) {
	author := nostr.Generate()
	headSHA := strings.Repeat("a", 40)
	mainSHA := strings.Repeat("d", 40)
	merger, err := enqueueHostedMergeOn(t, author, logReviewBranch(author.Public(), strings.Repeat("ab", 16)), "main", &recordingGitBackend{
		refsBySubpath: map[string]string{
			"refs":          `{"head":{"name":"main","sha":"` + mainSHA + `"}}`,
			"refs/branches": `{"refs":[{"name":"community-visible","sha":"` + headSHA + `"},{"name":"main","sha":"` + mainSHA + `"}],"more":false}`,
			"refs/tags":     `{"refs":[]}`,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "refs/heads/main", merger.requests[0].TargetRef)
}

func TestRelayMergeLogReviewUsesLiveDefaultForUnprotectedTarget(t *testing.T) {
	author := nostr.Generate()
	merger, err := enqueueHostedMergeResult(t, author, logReviewBranch(author.Public(), strings.Repeat("ab", 16)), "main")
	require.NoError(t, err)
	require.Len(t, merger.requests, 1)
	require.Equal(t, "refs/heads/trunk", merger.requests[0].TargetRef)
}

func enqueueHostedMergeResult(t *testing.T, author nostr.SecretKey, branch, targetBranch string) (*fakeRelayMerger, error) {
	t.Helper()
	headSHA := strings.Repeat("a", 40)
	return enqueueHostedMergeOn(t, author, branch, targetBranch, &recordingGitBackend{
		refsJSON: `{"head":{"name":"refs/heads/trunk","sha":"` + headSHA + `"},"refs":[{"name":"refs/heads/trunk","sha":"` + headSHA + `"}]}`,
	})
}

func enqueueHostedMergeOn(t *testing.T, author nostr.SecretKey, branch, targetBranch string, backend *recordingGitBackend) (*fakeRelayMerger, error) {
	t.Helper()
	policy, store := createGitForgeTestPolicy(t, backend)
	merger := &fakeRelayMerger{result: forge.CollaborationResult{State: "merged", MergedCommit: strings.Repeat("c", 40)}}
	policy.RelayMerger = merger
	owner := nostr.Generate()
	maintainer := nostr.Generate()
	repository := readyHostedRepository(t, policy, store, owner, "notes", "repo-channel")
	grantGitGroup(policy, "repo-channel", author.Public())
	grantGitGroup(policy, "repo-channel", maintainer.Public())
	policy.ProjectTasks.Groups.SetMemberRoles("repo-channel", maintainer.Public(), []string{"maintainer"})
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	head := strings.Repeat("b", 40)
	tags := nostr.Tags{
		{"a", coordinate.String()}, {"p", repository.PubKey.Hex()}, {"subject", "Log review"},
		{"c", head}, {"clone", "https://relay.example/git/repo.git"},
	}
	if branch != "" {
		tags = append(tags, nostr.Tag{"branch-name", branch})
	}
	if targetBranch != "" {
		tags = append(tags, nostr.Tag{"target-branch", targetBranch})
	}
	root := nostr.Event{Kind: projecttask.KindPullRequest, CreatedAt: nostr.Now(), Tags: tags}
	require.NoError(t, root.Sign(author))
	require.NoError(t, store.SaveEvent(root))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), root))
	require.NoError(t, policy.Remember(context.Background(), root))
	approval := nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Now(), Content: "lgtm", Tags: nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"t", "approval"}, {"c", head},
	}}
	require.NoError(t, approval.Sign(maintainer))
	require.NoError(t, store.SaveEvent(approval))
	body, _ := json.Marshal(map[string]string{"expected_head": head, "method": "merge"})
	target := "https://relay.example/api/forge/pull-requests/" + root.ID.Hex() + "/merge"
	request := signedForgeRequest(t, http.MethodPost, target, body, maintainer)
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, request)
	require.Equal(t, http.StatusAccepted, recorder.Code)
	if err := policy.ProcessForgeOutboxOnce(context.Background()); err != nil {
		return merger, err
	}
	return merger, policy.ProcessForgeOutboxOnce(context.Background())
}
