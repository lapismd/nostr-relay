package gitforge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"zooid/internal/projecttask"
)

func testRepository(t *testing.T) Repository {
	t.Helper()
	owner := nostr.MustSecretKeyFromHex(strings.Repeat("4", 64)).Public()
	return Repository{Coordinate: projecttask.RepositoryCoordinate{Owner: owner, ID: "notes"}, Class: RepositoryHosted}
}

func TestWalGitEnsureRepositoryUsesServiceIdentity(t *testing.T) {
	repository := testRepository(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPut, r.Method)
		require.Equal(t, "/"+repository.Coordinate.Owner.Hex()+"/notes/api", r.URL.Path)
		require.Equal(t, "Bearer service-secret", r.Header.Get("Authorization"))
		require.Equal(t, repository.Coordinate.Owner.Hex(), r.Header.Get("X-Walgit-Principal"))
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	backend, err := NewWalGitBackend(server.URL, "service-secret")
	require.NoError(t, err)
	require.NoError(t, backend.EnsureRepository(context.Background(), repository.Coordinate))
}

func TestWalGitGitProxyRewritesPathAndStripsUntrustedHeaders(t *testing.T) {
	repository := testRepository(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/"+repository.Coordinate.Owner.Hex()+"/notes.git/git-upload-pack", r.URL.Path)
		require.Equal(t, "service=git-upload-pack", r.URL.RawQuery)
		require.Equal(t, "Bearer service-secret", r.Header.Get("Authorization"))
		require.Equal(t, "actor", r.Header.Get("X-Walgit-Principal"))
		require.Empty(t, r.Header.Get("X-Walgit-Evil"))
		_, _ = io.WriteString(w, "pack")
	}))
	defer server.Close()
	backend, err := NewWalGitBackend(server.URL, "service-secret")
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, "https://relay.example/git/o/r/git-upload-pack?service=git-upload-pack", strings.NewReader("request"))
	request.Header.Set("Authorization", "Nostr public-token")
	request.Header.Set("X-Walgit-Evil", "spoofed")
	recorder := httptest.NewRecorder()
	backend.ProxyGitHTTP(recorder, request, repository, "git-upload-pack", "actor")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "pack", recorder.Body.String())
}

func TestWalGitRepositoryAPIUsesPrivateCaching(t *testing.T) {
	repository := testRepository(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/"+repository.Coordinate.Owner.Hex()+"/notes/api/tree/main", r.URL.Path)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("ETag", `"tree"`)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	backend, err := NewWalGitBackend(server.URL, "service-secret")
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	backend.ProxyRepositoryAPI(recorder, httptest.NewRequest(http.MethodGet, "https://relay.example/api/git/o/r/tree/main", nil), repository, "tree/main", "actor")
	require.Contains(t, recorder.Header().Get("Cache-Control"), "private")
	require.NotContains(t, recorder.Header().Get("Cache-Control"), "public")
	require.Equal(t, `"tree"`, recorder.Header().Get("ETag"))
}

func TestWalGitCompareUsesImmutableOIDsAndBounds(t *testing.T) {
	repository := testRepository(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/"+repository.Coordinate.Owner.Hex()+"/notes/api/compare", r.URL.Path)
		require.Equal(t, strings.Repeat("a", 40), r.URL.Query().Get("base"))
		require.Equal(t, strings.Repeat("b", 40), r.URL.Query().Get("head"))
		require.Equal(t, "merge-base", r.URL.Query().Get("mode"))
		require.Equal(t, "10", r.URL.Query().Get("max_files"))
		require.Equal(t, "1024", r.URL.Query().Get("max_patch_bytes"))
		require.Equal(t, "reviewer", r.Header.Get("X-Walgit-Principal"))
		_ = json.NewEncoder(w).Encode(CompareResult{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40), Patch: "diff"})
	}))
	defer server.Close()
	backend, err := NewWalGitBackend(server.URL, "service-secret")
	require.NoError(t, err)

	result, err := backend.Compare(context.Background(), repository, CompareRequest{
		Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40), UseMergeBase: true,
		MaxFiles: 10, MaxPatchBytes: 1024, Principal: "reviewer",
	})
	require.NoError(t, err)
	require.Equal(t, "diff", result.Patch)
}

func TestWalGitResolveReturnsFullObjectID(t *testing.T) {
	repository := testRepository(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/"+repository.Coordinate.Owner.Hex()+"/notes/api/resolve/feature/topic", r.URL.Path)
		require.Equal(t, "reviewer", r.Header.Get("X-Walgit-Principal"))
		_ = json.NewEncoder(w).Encode(map[string]string{"ref": "feature/topic", "sha": strings.Repeat("A", 40)})
	}))
	defer server.Close()
	backend, err := NewWalGitBackend(server.URL, "service-secret")
	require.NoError(t, err)

	sha, err := backend.Resolve(context.Background(), repository, "feature/topic", "reviewer")
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("a", 40), sha)
}

func TestNewWalGitBackendRequiresInternalURLAndToken(t *testing.T) {
	_, err := NewWalGitBackend("walgit:8080", "token")
	require.Error(t, err)
	_, err = NewWalGitBackend("http://walgit:8080", "")
	require.ErrorContains(t, err, "WALGIT_RELAY_TOKEN")
}
