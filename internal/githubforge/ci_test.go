package githubforge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"zooid/internal/forge"
)

func TestGitHubCIUsesExactTemporaryRefAndDiscoversRun(t *testing.T) {
	commit := strings.Repeat("a", 40)
	dispatched := false
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/99/access_tokens" {
			var request struct {
				RepositoryIDs []int64           `json:"repository_ids"`
				Permissions   map[string]string `json:"permissions"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			require.Equal(t, []int64{42}, request.RepositoryIDs)
			require.Equal(t, "write", request.Permissions["contents"])
			require.Equal(t, "write", request.Permissions["actions"])
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "opaque", "expires_at": time.Now().Add(time.Hour).UTC()})
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/git/ref/heads/lapis-ci/ci_123":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/lapis/notes/git/refs":
			var request map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			require.Equal(t, "refs/heads/lapis-ci/ci_123", request["ref"])
			require.Equal(t, commit, request["sha"])
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"ref": request["ref"], "object": map[string]string{"sha": commit}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/actions/workflows/ci.yml/runs":
			runs := []map[string]any{}
			if dispatched {
				runs = append(runs, map[string]any{
					"id": 7001, "head_sha": commit, "head_branch": "lapis-ci/ci_123",
					"status": "queued", "html_url": "https://github.com/lapis/notes/actions/runs/7001",
					"updated_at": time.Now().UTC(),
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": runs})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/lapis/notes/actions/workflows/ci.yml/dispatches":
			var request map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			require.Equal(t, "lapis-ci/ci_123", request["ref"])
			dispatched = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/actions/runs/7001":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 7001, "head_sha": commit, "head_branch": "lapis-ci/ci_123",
				"status": "completed", "conclusion": "success",
				"html_url": "https://github.com/lapis/notes/actions/runs/7001", "updated_at": time.Now().UTC(),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/lapis/notes/actions/runs/7001/cancel":
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/lapis/notes/git/refs/heads/lapis-ci/ci_123":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	repository.Integration.CIBackend = forge.BackendGitHub
	repository.Integration.CIConnectionID = "connection"
	repository.Integration.CIExternalRepositoryID = "42"
	repository.Integration.CIExternalRepository = "lapis/notes"

	request := forge.CIRequest{OperationID: "ci_123", Repository: repository, Commit: commit, Workflow: "ci.yml"}
	run, err := backend.Start(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, "queued", run.Status)
	require.Equal(t, "7001", run.ProviderRunID)
	require.Equal(t, "refs/heads/lapis-ci/ci_123", run.MirrorRef)
	run, err = backend.Discover(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, "7001", run.ProviderRunID)
	require.Equal(t, commit, run.Commit)
	run, err = backend.Status(context.Background(), repository, "7001")
	require.NoError(t, err)
	require.Equal(t, "success", run.Status)
	require.NoError(t, backend.Cancel(context.Background(), repository, "7001"))
	require.NoError(t, backend.Cleanup(context.Background(), repository, "refs/heads/lapis-ci/ci_123"))
}

func TestGitHubCIRejectsConflictingTemporaryRef(t *testing.T) {
	commit := strings.Repeat("a", 40)
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(t, w, r) {
			return
		}
		if r.URL.Path == "/repos/lapis/notes/git/ref/heads/lapis-ci/ci_123" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ref": "refs/heads/lapis-ci/ci_123", "object": map[string]string{"sha": strings.Repeat("b", 40)}})
			return
		}
		http.NotFound(w, r)
	})
	repository.Integration.CIBackend = forge.BackendGitHub
	repository.Integration.CIConnectionID = "connection"
	repository.Integration.CIExternalRepositoryID = "42"
	repository.Integration.CIExternalRepository = "lapis/notes"
	_, err := backend.Start(context.Background(), forge.CIRequest{OperationID: "ci_123", Repository: repository, Commit: commit, Workflow: "ci.yml"})
	var backendError *forge.Error
	require.ErrorAs(t, err, &backendError)
	require.Equal(t, "github_ci_ref_conflict", backendError.Code)
}
