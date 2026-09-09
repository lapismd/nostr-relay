package githubforge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"zooid/internal/forge"
)

type staticConnectionLoader struct{ connection Connection }

func (loader staticConnectionLoader) LoadGitHubConnection(_ context.Context, id string) (Connection, bool, error) {
	return loader.connection, id == loader.connection.ID, nil
}

func (loader staticConnectionLoader) GitHubWebhookConnections(context.Context) ([]Connection, error) {
	return []Connection{loader.connection}, nil
}

func testGitHubBackend(t *testing.T, handler http.HandlerFunc) (*Backend, forge.Repository) {
	t.Helper()
	key := testPrivateKey(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	require.NoError(t, err)
	connection := Connection{
		ID: "connection", Host: "github.com", AppID: "123", InstallationID: 99,
		CredentialRef: "env:APP_KEY", Status: "active",
	}
	backend, err := newBackend(staticConnectionLoader{connection}, mapCredentials{"env:APP_KEY": key}, server.Client(), base, true)
	require.NoError(t, err)
	repository := forge.Repository{
		CloneURL: "https://github.com/lapis/notes.git",
		Integration: forge.Integration{
			GitBackend: forge.BackendGitHub, ConnectionID: "connection",
			ExternalRepositoryID: "42", ExternalRepository: "lapis/notes",
		},
	}
	return backend, repository
}

func tokenResponse(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	if r.URL.Path != "/app/installations/99/access_tokens" {
		return false
	}
	require.Equal(t, APIVersion, r.Header.Get("X-GitHub-Api-Version"))
	var request struct {
		RepositoryIDs []int64 `json:"repository_ids"`
	}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
	require.Equal(t, []int64{42}, request.RepositoryIDs)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_opaque_token_value", "expires_at": time.Now().Add(time.Hour).UTC()})
	return true
}

func TestGitHubCapabilitiesAreProviderNativeAndBounded(t *testing.T) {
	backend, repository := testGitHubBackend(t, func(http.ResponseWriter, *http.Request) {})
	capabilities, err := backend.Capabilities(context.Background(), repository)
	require.NoError(t, err)
	require.Equal(t, forge.CloneAuthProvider, capabilities.Clone.Auth)
	require.Equal(t, "https://github.com/lapis/notes.git", capabilities.Clone.URL)
	require.False(t, capabilities.SmartHTTP.Read)
	require.True(t, capabilities.Browse)
	require.True(t, capabilities.Compare)
	require.Equal(t, 300, capabilities.Limits.MaximumChangedFiles)
	require.True(t, capabilities.Limits.MayTruncateCompare)
}

func TestGitHubReplicationTargetUsesRepositoryNarrowedContentsWriteToken(t *testing.T) {
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/app/installations/99/access_tokens", r.URL.Path)
		var request struct {
			RepositoryIDs []int64           `json:"repository_ids"`
			Permissions   map[string]string `json:"permissions"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, []int64{42}, request.RepositoryIDs)
		require.Equal(t, map[string]string{"metadata": "read", "contents": "write"}, request.Permissions)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "opaque-replication-token", "expires_at": time.Now().Add(time.Hour).UTC()})
	})
	target, err := backend.GitReplicationTarget(context.Background(), repository)
	require.NoError(t, err)
	require.Equal(t, "https://github.com/lapis/notes.git", target.CloneURL)
	require.Equal(t, "x-access-token", target.Username)
	require.Equal(t, "opaque-replication-token", target.Password)
}

func TestGitHubRefsTranslateToWalGitV1(t *testing.T) {
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(t, w, r) {
			return
		}
		require.Equal(t, "Bearer ghs_opaque_token_value", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repositories/42":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "full_name": "lapis/notes", "default_branch": "main"})
		case "/repos/lapis/notes/git/ref/heads/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"ref": "refs/heads/main", "object": map[string]string{"sha": strings.Repeat("a", 40)}})
		default:
			http.NotFound(w, r)
		}
	})
	response, err := backend.Refs(context.Background(), repository, forge.RefsRequest{})
	require.NoError(t, err)
	require.NotNil(t, response.Head)
	require.Equal(t, "refs/heads/main", response.Head.Name)
	require.Equal(t, strings.Repeat("a", 40), response.Head.SHA)
}

func TestGitHubRepositoryReadsAreCoalescedCachedAndInvalidated(t *testing.T) {
	var repositoryReads atomic.Int32
	firstReadStarted := make(chan struct{})
	releaseFirstRead := make(chan struct{})
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(t, w, r) {
			return
		}
		switch r.URL.Path {
		case "/repositories/42":
			if repositoryReads.Add(1) == 1 {
				close(firstReadStarted)
				<-releaseFirstRead
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "full_name": "lapis/notes", "default_branch": "main"})
		case "/repos/lapis/notes/issues":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	})
	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	backend.reads.now = func() time.Time { return now }

	const callers = 8
	var wait sync.WaitGroup
	errors := make(chan error, callers)
	wait.Add(callers)
	for range callers {
		go func() {
			defer wait.Done()
			_, err := backend.Repository(context.Background(), repository)
			errors <- err
		}()
	}
	<-firstReadStarted
	close(releaseFirstRead)
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), repositoryReads.Load())

	_, err := backend.Repository(context.Background(), repository)
	require.NoError(t, err)
	require.Equal(t, int32(1), repositoryReads.Load())

	now = now.Add(githubReadCacheTTL + time.Millisecond)
	_, err = backend.Repository(context.Background(), repository)
	require.NoError(t, err)
	require.Equal(t, int32(2), repositoryReads.Load())

	response, err := backend.doRepository(context.Background(), repository, http.MethodPost, "/repos/lapis/notes/issues", strings.NewReader(`{}`), collaborationPermissions)
	require.NoError(t, err)
	response.Body.Close()
	_, err = backend.Repository(context.Background(), repository)
	require.NoError(t, err)
	require.Equal(t, int32(3), repositoryReads.Load())
}

func TestGitHubRepositoryMissingReadsUseShortNegativeCache(t *testing.T) {
	var reads atomic.Int32
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(t, w, r) {
			return
		}
		if r.URL.Path == "/repos/lapis/notes/commits/missing" {
			reads.Add(1)
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	})

	for range 2 {
		_, err := backend.Resolve(context.Background(), repository, forge.RevisionRequest{Revision: "missing"})
		require.ErrorContains(t, err, "github_object_not_found")
	}
	require.Equal(t, int32(1), reads.Load())
}

func TestGitHubReconcileSnapshotsReviewCommentReactions(t *testing.T) {
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(t, w, r) {
			return
		}
		switch r.URL.Path {
		case "/repositories/42":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "full_name": "lapis/notes", "default_branch": "main"})
		case "/repos/lapis/notes/issues", "/repos/lapis/notes/pulls":
			_ = json.NewEncoder(w).Encode([]any{})
		case "/repos/lapis/notes/pulls/comments":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 44, "reactions": map[string]any{"total_count": 1}}})
		case "/repos/lapis/notes/pulls/comments/44/reactions":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": 77, "content": "heart", "created_at": "2026-09-09T10:00:00Z",
				"user": map[string]any{"id": 55, "login": "octocat"},
			}})
		default:
			http.NotFound(w, r)
		}
	})

	result, err := backend.Reconcile(context.Background(), repository, forge.ProviderImportRequest{
		UpdatedSince: time.Now().Add(-10 * time.Minute), MaximumRoots: 10, MaximumComments: 10,
	})
	require.NoError(t, err)
	require.Len(t, result.Events, 1)
	event := result.Events[0]
	require.Equal(t, "pull_request_review_comment_reactions", event.EventType)
	require.Equal(t, "synchronize", event.Action)
	require.Equal(t, "44", event.ExternalObjectID)
	var payload struct {
		Comment   struct{ ID int64 } `json:"comment"`
		Reactions []struct {
			ID      int64  `json:"id"`
			Content string `json:"content"`
		} `json:"reactions"`
	}
	require.NoError(t, json.Unmarshal(event.Payload, &payload))
	require.Equal(t, int64(44), payload.Comment.ID)
	require.Equal(t, int64(77), payload.Reactions[0].ID)
	require.Equal(t, "heart", payload.Reactions[0].Content)
}

func TestGitHubBlobAndCompareUseNormalizedV1Shapes(t *testing.T) {
	head := strings.Repeat("b", 40)
	base := strings.Repeat("a", 40)
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(t, w, r) {
			return
		}
		switch {
		case r.URL.Path == "/repos/lapis/notes/commits/"+head:
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": head})
		case r.URL.Path == "/repos/lapis/notes/contents/README.md":
			require.Equal(t, head, r.URL.Query().Get("ref"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "README.md", "path": "README.md", "type": "file", "size": 8,
				"sha": strings.Repeat("c", 40), "encoding": "base64", "content": "IyBOb3Rlcw==",
			})
		case strings.HasPrefix(r.URL.Path, "/repos/lapis/notes/compare/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"base_commit": map[string]string{"sha": base}, "head_commit": map[string]string{"sha": head},
				"merge_base_commit": map[string]string{"sha": base},
				"files":             []map[string]any{{"filename": "README.md", "status": "modified", "additions": 1, "deletions": 0, "changes": 1, "patch": "@@ -1 +1 @@"}},
			})
		default:
			http.NotFound(w, r)
		}
	})

	response, err := backend.Blob(context.Background(), repository, forge.RevisionRequest{Revision: head + "/README.md"})
	require.NoError(t, err)
	require.Equal(t, "# Notes", response.Contents)
	require.Equal(t, githubBrowserCacheControl, response.CacheControl)

	comparison, err := backend.Compare(context.Background(), repository, forge.CompareRequest{
		Base: base, Head: head, MaxFiles: 10, MaxPatchBytes: 1 << 20,
	})
	require.NoError(t, err)
	require.Equal(t, base, comparison.Base)
	require.Equal(t, head, comparison.Head)
	require.Len(t, comparison.Files, 1)
	require.Contains(t, comparison.Patch, "diff --git")
	require.Equal(t, 300, comparison.CapabilityLimits.MaximumChangedFiles)
}

func TestGitHubBlobTooLargeIsAUsableMetadataResult(t *testing.T) {
	head := strings.Repeat("b", 40)
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(t, w, r) {
			return
		}
		switch {
		case r.URL.Path == "/repos/lapis/notes/commits/"+head:
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": head})
		case r.URL.Path == "/repos/lapis/notes/contents/archive.bin":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "archive.bin", "path": "archive.bin", "type": "file", "size": maxBrowserBlob + 1, "sha": strings.Repeat("c", 40)})
		default:
			http.NotFound(w, r)
		}
	})
	blob, err := backend.Blob(context.Background(), repository, forge.RevisionRequest{Revision: head + "/archive.bin"})
	require.NoError(t, err)
	require.True(t, blob.TooLarge)
	require.Empty(t, blob.Contents)
	require.Equal(t, "browser_blob_bytes", blob.LimitExceeded.Resource)
	require.Equal(t, int64(maxBrowserBlob), blob.LimitExceeded.Limit)
}

func TestGitHubBackendRejectsArbitraryAPIBase(t *testing.T) {
	base, err := url.Parse("https://github.example/api")
	require.NoError(t, err)
	_, err = newBackend(staticConnectionLoader{}, mapCredentials{}, http.DefaultClient, base, false)
	require.ErrorContains(t, err, APIBaseURL)
}
