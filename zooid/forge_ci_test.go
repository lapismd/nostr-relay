package zooid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"zooid/internal/forge"
	"zooid/internal/projecttask"
)

func configureGitHubCITest(t *testing.T) (*GitForgePolicy, *EventStore, *fakeGitHubProvider, nostr.SecretKey, nostr.Event) {
	t.Helper()
	policy, store, provider, actor, repository, _ := configureForgeCollaborationTest(t)
	require.NoError(t, policy.Registry.RegisterCI(provider))
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	integration, found, err := policy.repositoryIntegration(context.Background(), coordinate)
	require.NoError(t, err)
	require.True(t, found)
	integration.CIBackend = forge.BackendGitHub
	integration.CIConnectionID = "connection"
	integration.CIExternalRepositoryID = "42"
	integration.CIExternalRepository = "lapis/notes"
	integration.CIWorkflow = "ci.yml"
	require.NoError(t, policy.setExplicitIntegration(context.Background(), integration, repository.ID.Hex()))
	return policy, store, provider, actor, repository
}

func TestForgeCIStartsExactCommitAndPublishesTerminalActivity(t *testing.T) {
	policy, store, provider, actor, repository := configureGitHubCITest(t)
	commit := strings.Repeat("a", 40)
	provider.ciRun = forge.CIRun{
		ProviderRunID: "7001", Commit: commit, Status: "queued",
		URL: "https://github.com/lapis/notes/actions/runs/7001", UpdatedAt: time.Now().UTC(),
	}
	target := "https://relay.example/api/forge/repositories/" + repository.PubKey.Hex() + "/notes/ci"
	body := []byte(`{"commit":"` + commit + `","inputs":{"suite":"unit"}}`)
	request := signedForgeRequest(t, http.MethodPost, target, body, actor)
	request.Header.Set("Idempotency-Key", "ci-run-1")
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, request)
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	var accepted map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &accepted))
	operationID := accepted["operation_id"].(string)

	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Len(t, provider.ciStarts, 1)
	require.Equal(t, commit, provider.ciStarts[0].Commit)
	require.Equal(t, "ci.yml", provider.ciStarts[0].Workflow)
	require.Equal(t, "refs/heads/lapis-ci/"+operationID, provider.ciStarts[0].MirrorRef)
	run, found, err := policy.loadForgeCIRun(context.Background(), operationID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "queued", run.Status)

	completedAt := time.Now().UTC().Truncate(time.Second)
	provider.ciStatus = forge.CIRun{
		ProviderRunID: "7001", Commit: commit, Status: "success",
		URL: "https://github.com/lapis/notes/actions/runs/7001", UpdatedAt: completedAt,
	}
	require.NoError(t, policy.ProcessForgeCIOnce(context.Background()))
	require.Equal(t, []string{"refs/heads/lapis-ci/" + operationID}, provider.ciCleaned)
	events := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindTextNote}, Tags: nostr.TagMap{"a": {"30617:" + repository.PubKey.Hex() + ":notes"}, "t": {"ci"}, "c": {commit}}})
	require.Len(t, events, 1)
	require.Equal(t, "success", events[0].Tags.Find("status")[1])
	require.Equal(t, "7001", events[0].Tags.Find("external-run-id")[1])
	require.Equal(t, nostr.Timestamp(completedAt.Unix()), events[0].CreatedAt)
	run, found, err = policy.loadForgeCIRun(context.Background(), operationID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "complete", run.CleanupState)
	require.Equal(t, events[0].ID.Hex(), run.ActivityEventID)
}

func TestWalGitCIUsesOneWayGitHubMirrorBeforeDispatch(t *testing.T) {
	policy, store := createGitForgeTestPolicy(t, nil)
	provider := &fakeGitHubProvider{}
	source := &fakeWalGitImporter{fakeGitHubProvider: &fakeGitHubProvider{}}
	require.NoError(t, policy.Registry.RegisterGit(source))
	require.NoError(t, policy.Registry.RegisterCI(provider))
	owner := nostr.Generate()
	canonical := "https://relay.example/git/" + owner.Public().Hex() + "/notes"
	repository := gitForgeRepositoryEvent(t, owner, "notes", nostr.Tag{"clone", canonical})
	_, err := store.ReplaceEvent(repository)
	require.NoError(t, err)
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), repository))
	require.NoError(t, policy.Remember(context.Background(), repository))
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	require.NoError(t, policy.setExplicitIntegration(context.Background(), forge.Integration{
		Coordinate: coordinate, AnnouncementID: repository.ID.Hex(), ConfigurationSource: forge.ConfigurationExplicit,
		GitBackend: forge.BackendWalGit, CollaborationBackend: forge.BackendRelay,
		CIBackend: forge.BackendGitHub, CIConnectionID: "ci-connection",
		CIExternalRepositoryID: "84", CIExternalRepository: "lapis/ci-mirror", CIWorkflow: "ci.yml",
		HealthState: "ready",
	}, repository.ID.Hex()))
	actor := store.Config.secret
	grantGitGroup(policy, "channel", actor.Public())
	commit := strings.Repeat("b", 40)
	provider.ciRun = forge.CIRun{ProviderRunID: "8001", Commit: commit, Status: "queued", UpdatedAt: time.Now().UTC()}
	target := "https://relay.example/api/forge/repositories/" + repository.PubKey.Hex() + "/notes/ci"
	body := []byte(`{"commit":"` + commit + `"}`)
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodPost, target, body, actor))
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Len(t, source.mirrored, 1)
	require.Equal(t, commit, source.mirrored[0].Commit)
	require.Equal(t, "https://github.com/lapis/ci-mirror.git", source.targets[0].CloneURL)
	require.Len(t, provider.ciStarts, 1)
	var mirrorRows int
	require.NoError(t, GetDb().QueryRow(policy.Events.Schema.Render(`
		SELECT COUNT(*) FROM {{.Name}}__forge_mirror_refs WHERE owner = $1 AND repo = 'notes' AND source_oid = $2`),
		repository.PubKey.Hex(), commit).Scan(&mirrorRows))
	require.Equal(t, 1, mirrorRows)
}

func TestForgeCICancelUsesProviderRun(t *testing.T) {
	policy, _, provider, actor, repository := configureGitHubCITest(t)
	commit := strings.Repeat("c", 40)
	provider.ciRun = forge.CIRun{ProviderRunID: "9001", Commit: commit, Status: "in_progress", UpdatedAt: time.Now().UTC()}
	target := "https://relay.example/api/forge/repositories/" + repository.PubKey.Hex() + "/notes/ci"
	body := []byte(`{"commit":"` + commit + `"}`)
	request := signedForgeRequest(t, http.MethodPost, target, body, actor)
	request.Header.Set("Idempotency-Key", "cancel-me")
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, request)
	require.Equal(t, http.StatusAccepted, recorder.Code)
	var accepted map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &accepted))
	operationID := accepted["operation_id"].(string)
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	cancelURL := "https://relay.example/api/forge/operations/" + operationID + "/cancel"
	recorder = httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodPost, cancelURL, []byte{}, actor))
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	require.Equal(t, []string{"9001"}, provider.ciCancelled)
}
