package zooid

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

type recordingGitBackend struct {
	mu                sync.Mutex
	gitCalls          []string
	apiCalls          []string
	principal         string
	resolveRevision   string
	resolveRevisions  []string
	resolveResult     string
	comparison        gitforge.CompareResult
	comparisonRequest gitforge.CompareRequest
}

func (*recordingGitBackend) EnsureRepository(context.Context, projecttask.RepositoryCoordinate) error {
	return nil
}

func (backend *recordingGitBackend) ProxyGitHTTP(w http.ResponseWriter, _ *http.Request, _ gitforge.Repository, subpath, principal string) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.gitCalls = append(backend.gitCalls, subpath)
	backend.principal = principal
	w.WriteHeader(http.StatusOK)
}

func (backend *recordingGitBackend) ProxyRepositoryAPI(w http.ResponseWriter, _ *http.Request, _ gitforge.Repository, subpath, principal string) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.apiCalls = append(backend.apiCalls, subpath)
	backend.principal = principal
	w.WriteHeader(http.StatusOK)
}

func (backend *recordingGitBackend) Resolve(_ context.Context, _ gitforge.Repository, revision, principal string) (string, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.resolveRevision = revision
	backend.resolveRevisions = append(backend.resolveRevisions, revision)
	backend.principal = principal
	return backend.resolveResult, nil
}

func (backend *recordingGitBackend) Compare(_ context.Context, _ gitforge.Repository, request gitforge.CompareRequest) (gitforge.CompareResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.comparisonRequest = request
	return backend.comparison, nil
}

func signedGitRequest(t *testing.T, request *http.Request, secret nostr.SecretKey, method, target string) {
	t.Helper()
	event := nostr.Event{
		Kind: nostr.KindHTTPAuth, CreatedAt: nostr.Now(),
		Tags: nostr.Tags{{"u", target}, {"method", method}},
	}
	require.NoError(t, event.Sign(secret))
	raw, err := event.MarshalJSON()
	require.NoError(t, err)
	request.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(raw))
}

func grantGitGroup(policy *GitForgePolicy, channel string, member nostr.PubKey, flags ...string) {
	tags := nostr.Tags{{"d", channel}}
	metadata := &groupMetaCache{event: nostr.Event{Kind: nostr.KindSimpleGroupMetadata, Tags: tags}, found: true}
	for _, flag := range flags {
		metadata.event.Tags = append(metadata.event.Tags, nostr.Tag{flag})
		switch flag {
		case "private":
			metadata.private = true
		case "hidden":
			metadata.hidden = true
		case "write-restricted":
			metadata.writeRestricted = true
		}
	}
	policy.Groups.metadataCache.Store(channel, metadata)
	policy.Groups.membershipFullyLoaded.Store(channel, struct{}{})
	members := policy.Groups.getOrCreateMemberSet(channel)
	members.mu.Lock()
	members.members[member] = struct{}{}
	members.mu.Unlock()
}

func readyHostedRepository(t *testing.T, policy *GitForgePolicy, store *EventStore, owner nostr.SecretKey, id, channel string) nostr.Event {
	t.Helper()
	event := gitForgeRepositoryEvent(t, owner, id)
	event.Tags = event.Tags[:1]
	event.Tags = append(event.Tags, nostr.Tag{projecttask.TagBuzzChannel, channel})
	require.NoError(t, event.Sign(owner))
	require.NoError(t, store.SaveEvent(event))
	require.NoError(t, policy.Remember(context.Background(), event))
	_, err := GetDb().Exec(policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_repository_intents SET reconcile_state = 'ready'
		WHERE owner = $1 AND repo = $2`), event.PubKey.Hex(), id)
	require.NoError(t, err)
	return event
}

func TestGitHTTPUsesReusableBuzzCredentialAndNIP29Membership(t *testing.T) {
	backend := &recordingGitBackend{}
	policy, store := createGitForgeTestPolicy(t, backend)
	owner := nostr.Generate()
	member := nostr.Generate()
	repository := readyHostedRepository(t, policy, store, owner, "notes", "repo-channel")
	grantGitGroup(policy, "repo-channel", member.Public())
	root := "https://relay.example/git/" + repository.PubKey.Hex() + "/notes.git"

	request := httptest.NewRequest(http.MethodPost, root+"/git-receive-pack", strings.NewReader("pack"))
	signedGitRequest(t, request, member, http.MethodGet, root)
	recorder := httptest.NewRecorder()
	policy.ServeGitHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []string{"git-receive-pack"}, backend.gitCalls)
	require.Equal(t, member.Public().Hex(), backend.principal)

	outsider := nostr.Generate()
	request = httptest.NewRequest(http.MethodGet, root+"/info/refs?service=git-upload-pack", nil)
	signedGitRequest(t, request, outsider, http.MethodGet, root)
	recorder = httptest.NewRecorder()
	policy.ServeGitHTTP(recorder, request)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Len(t, backend.gitCalls, 1)
}

func TestGitHTTPDirectsExternalRepositoriesToProviderGit(t *testing.T) {
	backend := &recordingGitBackend{}
	policy, store := createGitForgeTestPolicy(t, backend)
	owner := nostr.Generate()
	member := nostr.Generate()
	repository := gitForgeRepositoryEvent(t, owner, "notes", nostr.Tag{"clone", "https://github.com/lapis/notes.git"})
	repository.Tags[1] = nostr.Tag{projecttask.TagBuzzChannel, "repo-channel"}
	require.NoError(t, repository.Sign(owner))
	require.NoError(t, store.SaveEvent(repository))
	require.NoError(t, policy.Remember(context.Background(), repository))
	grantGitGroup(policy, "repo-channel", member.Public())
	root := "https://relay.example/git/" + repository.PubKey.Hex() + "/notes.git"

	request := httptest.NewRequest(http.MethodGet, root+"/info/refs?service=git-upload-pack", nil)
	signedGitRequest(t, request, member, http.MethodGet, root)
	recorder := httptest.NewRecorder()
	policy.ServeGitHTTP(recorder, request)
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.JSONEq(t, `{"error":"provider_git_required"}`, recorder.Body.String())
	require.Empty(t, backend.gitCalls)
}

func TestRepositoryAPIAuthorizesBeforeProxyAndRejectsAdminSurface(t *testing.T) {
	backend := &recordingGitBackend{}
	policy, store := createGitForgeTestPolicy(t, backend)
	owner := nostr.Generate()
	member := nostr.Generate()
	repository := readyHostedRepository(t, policy, store, owner, "notes", "private-repo")
	grantGitGroup(policy, "private-repo", member.Public(), "private")
	target := "https://relay.example/api/git/" + repository.PubKey.Hex() + "/notes/tree/main"

	request := httptest.NewRequest(http.MethodGet, target, nil)
	signedGitRequest(t, request, member, http.MethodGet, target)
	recorder := httptest.NewRecorder()
	policy.ServeRepositoryAPI(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []string{"tree/main"}, backend.apiCalls)
	require.Contains(t, recorder.Body.String(), `"capability_limits"`)

	adminTarget := "https://relay.example/api/git/" + repository.PubKey.Hex() + "/notes/settings"
	request = httptest.NewRequest(http.MethodGet, adminTarget, nil)
	signedGitRequest(t, request, member, http.MethodGet, adminTarget)
	recorder = httptest.NewRecorder()
	policy.ServeRepositoryAPI(recorder, request)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Len(t, backend.apiCalls, 1)

	outsider := nostr.Generate()
	request = httptest.NewRequest(http.MethodGet, target, nil)
	signedGitRequest(t, request, outsider, http.MethodGet, target)
	recorder = httptest.NewRecorder()
	policy.ServeRepositoryAPI(recorder, request)
	require.Equal(t, http.StatusNotFound, recorder.Code, "private repositories do not disclose existence")
	require.Len(t, backend.apiCalls, 1)
}

func TestPullRequestDiffUsesReducerAndTargetBranchFallback(t *testing.T) {
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	backend := &recordingGitBackend{
		resolveResult: base,
		comparison: gitforge.CompareResult{
			Base: base, Head: head, MergeBase: strings.Repeat("c", 40),
			Files: []gitforge.CompareFile{{Path: "README.md", Status: "modified", Additions: 1}}, Patch: "diff --git",
		},
	}
	policy, store := createGitForgeTestPolicy(t, backend)
	owner := nostr.Generate()
	author := nostr.Generate()
	repository := readyHostedRepository(t, policy, store, owner, "notes", "repo-channel")
	grantGitGroup(policy, "repo-channel", author.Public())
	grantGitGroup(policy, "origin-channel", author.Public(), "private")

	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	root := nostr.Event{
		Kind: projecttask.KindPullRequest, CreatedAt: nostr.Now(), Content: "Review this change",
		Tags: nostr.Tags{
			{"a", coordinate.String()}, {"p", repository.PubKey.Hex()}, {"subject", "Change notes"},
			{"c", head}, {"clone", "https://relay.example/git/repo.git"}, {"branch-name", "feature/notes"},
			{"target-branch", "main"}, {"h", "origin-channel"},
		},
	}
	// PR clone locators are independent NIP-34 data and use the canonical public route.
	root.Tags[4] = nostr.Tag{"clone", "https://relay.example/git/" + repository.PubKey.Hex() + "/notes.git"}
	require.NoError(t, root.Sign(author))
	require.NoError(t, store.SaveEvent(root))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), root))

	target := "https://relay.example/api/pull-requests/" + root.ID.Hex() + "/diff"
	request := httptest.NewRequest(http.MethodGet, target, nil)
	signedGitRequest(t, request, author, http.MethodGet, target)
	recorder := httptest.NewRecorder()
	policy.ServePullRequestAPI(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, []string{"main", base, head}, backend.resolveRevisions)
	require.Equal(t, base, backend.comparisonRequest.Base)
	require.Equal(t, head, backend.comparisonRequest.Head)
	require.True(t, backend.comparisonRequest.UseMergeBase)

	body, err := io.ReadAll(recorder.Result().Body)
	require.NoError(t, err)
	var response pullRequestDiffResponse
	require.NoError(t, json.Unmarshal(body, &response))
	require.Equal(t, "target-branch", response.BaseSource)
	require.Equal(t, "diff --git", response.Patch)
	require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
}

func TestPullRequestDiffUsesPersistedBaseCommitAfterMerge(t *testing.T) {
	base := strings.Repeat("d", 40)
	head := strings.Repeat("e", 40)
	merge := strings.Repeat("f", 40)
	backend := &recordingGitBackend{
		resolveResult: head,
		comparison: gitforge.CompareResult{
			Base: base, Head: head, MergeBase: strings.Repeat("c", 40),
			Files: []gitforge.CompareFile{{Path: "docs/history.md", Status: "added", Additions: 3}}, Patch: "diff --git",
		},
	}
	policy, store := createGitForgeTestPolicy(t, backend)
	owner := nostr.Generate()
	author := nostr.Generate()
	repository := readyHostedRepository(t, policy, store, owner, "notes", "repo-channel")
	grantGitGroup(policy, "repo-channel", author.Public())

	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	root := nostr.Event{Kind: projecttask.KindPullRequest, CreatedAt: nostr.Now(), Content: "Historical review", Tags: nostr.Tags{
		{"a", coordinate.String()}, {"p", repository.PubKey.Hex()}, {"subject", "Retain this diff"},
		{"c", head}, {"base-commit", base},
		{"clone", "https://relay.example/git/" + repository.PubKey.Hex() + "/notes.git"}, {"target-branch", "main"},
	}}
	require.NoError(t, root.Sign(author))
	require.NoError(t, store.SaveEvent(root))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), root))
	merged := nostr.Event{Kind: nostr.KindStatusApplied, CreatedAt: root.CreatedAt + 1, Tags: nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"merge-commit", merge},
	}}
	require.NoError(t, merged.Sign(author))
	require.NoError(t, store.SaveEvent(merged))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), merged))

	target := "https://relay.example/api/pull-requests/" + root.ID.Hex() + "/diff"
	request := httptest.NewRequest(http.MethodGet, target, nil)
	signedGitRequest(t, request, author, http.MethodGet, target)
	recorder := httptest.NewRecorder()
	policy.ServePullRequestAPI(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, []string{base, head}, backend.resolveRevisions, "the live target branch must not determine a historical diff")
	require.Equal(t, base, backend.comparisonRequest.Base)
	require.Equal(t, head, backend.comparisonRequest.Head)
	require.True(t, backend.comparisonRequest.UseMergeBase)

	var response pullRequestDiffResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "base-commit", response.BaseSource)
	require.Equal(t, projecttask.PullRequestMerged, response.Lifecycle)
	require.Equal(t, "docs/history.md", response.Files[0].Path)
}

func TestPullRequestDiffRequiresEveryDerivedScope(t *testing.T) {
	backend := &recordingGitBackend{resolveResult: strings.Repeat("a", 40)}
	policy, store := createGitForgeTestPolicy(t, backend)
	owner := nostr.Generate()
	author := nostr.Generate()
	repository := readyHostedRepository(t, policy, store, owner, "notes", "repo-channel")
	grantGitGroup(policy, "repo-channel", author.Public())
	policy.Groups.metadataCache.Store("origin-channel", &groupMetaCache{
		event: nostr.Event{Kind: nostr.KindSimpleGroupMetadata, Tags: nostr.Tags{{"d", "origin-channel"}, {"private"}}},
		found: true, private: true,
	})
	policy.Groups.membershipFullyLoaded.Store("origin-channel", struct{}{})
	policy.Groups.getOrCreateMemberSet("origin-channel")

	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	root := nostr.Event{Kind: projecttask.KindPullRequest, CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"a", coordinate.String()}, {"p", repository.PubKey.Hex()}, {"subject", "Private origin"},
		{"c", strings.Repeat("b", 40)}, {"clone", "https://relay.example/git/repo.git"}, {"h", "origin-channel"},
	}}
	require.NoError(t, root.Sign(author))
	require.NoError(t, store.SaveEvent(root))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), root))
	target := "https://relay.example/api/pull-requests/" + root.ID.Hex() + "/diff"
	request := httptest.NewRequest(http.MethodGet, target, nil)
	signedGitRequest(t, request, author, http.MethodGet, target)
	recorder := httptest.NewRecorder()
	policy.ServePullRequestAPI(recorder, request)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Empty(t, backend.comparisonRequest.Head)
}
