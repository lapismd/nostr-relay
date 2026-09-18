package zooid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"zooid/internal/forge"
	"zooid/internal/projecttask"
)

type fakeRelayMerger struct {
	requests []forge.ProtectedRefMergeRequest
	result   forge.CollaborationResult
}

func (merger *fakeRelayMerger) MergeProtectedRef(_ context.Context, _ forge.Repository, request forge.ProtectedRefMergeRequest) (forge.CollaborationResult, error) {
	merger.requests = append(merger.requests, request)
	return merger.result, nil
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
