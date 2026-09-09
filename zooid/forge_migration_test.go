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

type fakeWalGitImporter struct {
	*fakeGitHubProvider
	requests        []forge.ImportRequest
	objectRequests  []forge.ImportObjectRequest
	missingObjects  map[string]bool
	resolveRequests []string
	replications    []forge.GitReplicationTarget
	mirrored        []forge.CIRequest
	targets         []forge.CIMirrorTarget
}

func (backend *fakeWalGitImporter) MirrorCommit(_ context.Context, repository forge.Repository, commit string, target forge.CIMirrorTarget, mirrorRef string) error {
	backend.mirrored = append(backend.mirrored, forge.CIRequest{Repository: repository, Commit: commit, MirrorRef: mirrorRef})
	backend.targets = append(backend.targets, target)
	return nil
}

func (backend *fakeWalGitImporter) Name() string { return forge.BackendWalGit }

func (*fakeWalGitImporter) AuthorityCapabilities(context.Context, forge.Repository) (forge.GitAuthorityCapabilities, error) {
	return forge.GitAuthorityCapabilities{Clone: forge.CloneDescriptor{Available: true, Auth: forge.CloneAuthNostr}, SmartHTTP: forge.SmartHTTPCapabilities{Read: true, Write: true}, RefEvents: true, Provision: true, Import: true}, nil
}
func (*fakeWalGitImporter) ContentCapabilities(context.Context, forge.Repository) (forge.ContentCapabilities, error) {
	return forge.ContentCapabilities{Browse: true, Compare: true}, nil
}

func (*fakeWalGitImporter) CloneDescriptor(context.Context, forge.Repository) (forge.CloneDescriptor, error) {
	return forge.CloneDescriptor{Available: true, Auth: forge.CloneAuthNostr}, nil
}

func (backend *fakeWalGitImporter) ImportRepository(_ context.Context, _ forge.Repository, request forge.ImportRequest) (forge.ImportResult, error) {
	backend.requests = append(backend.requests, request)
	refs := make(map[string]string, len(request.ExpectedRefs))
	for name, oid := range request.ExpectedRefs {
		refs[name] = oid
	}
	return forge.ImportResult{Refs: refs}, nil
}

func (backend *fakeWalGitImporter) Resolve(_ context.Context, _ forge.Repository, request forge.RevisionRequest) (forge.Revision, error) {
	backend.resolveRequests = append(backend.resolveRequests, request.Revision)
	if backend.missingObjects[request.Revision] {
		return forge.Revision{}, &forge.Error{Status: http.StatusNotFound, Code: "git_object_not_found"}
	}
	return forge.Revision{Ref: request.Revision, SHA: request.Revision, Kind: "commit"}, nil
}

func (backend *fakeWalGitImporter) ImportObject(_ context.Context, _ forge.Repository, request forge.ImportObjectRequest) error {
	backend.objectRequests = append(backend.objectRequests, request)
	delete(backend.missingObjects, request.ObjectOID)
	return nil
}

func (backend *fakeWalGitImporter) ReplicateRepository(_ context.Context, _ forge.Repository, target forge.GitReplicationTarget, _, _ bool) error {
	backend.replications = append(backend.replications, target)
	return nil
}

func configureForgeMigrationTest(t *testing.T, snapshots []map[string]string) (*GitForgePolicy, *EventStore, *fakeGitHubProvider, *fakeWalGitImporter, nostr.SecretKey, nostr.Event) {
	t.Helper()
	policy, store := createGitForgeTestPolicy(t, nil)
	provider := &fakeGitHubProvider{
		refSnapshots: snapshots,
		cloneURL:     "https://github.com/lapis/notes.git", cloneUsername: "x-access-token", clonePassword: "opaque-installation-token",
	}
	provider.repository.ID = 42
	provider.repository.FullName = "lapis/notes"
	provider.repository.CloneURL = "https://github.com/lapis/notes.git"
	provider.repository.HTMLURL = "https://github.com/lapis/notes"
	target := &fakeWalGitImporter{fakeGitHubProvider: &fakeGitHubProvider{}}
	require.NoError(t, policy.Registry.RegisterGit(provider))
	require.NoError(t, policy.Registry.RegisterCollaboration(provider))
	require.NoError(t, policy.Registry.RegisterMirror(provider))
	require.NoError(t, policy.Registry.RegisterGitReplicationTarget(provider))
	require.NoError(t, policy.Registry.RegisterGit(target))
	policy.GitHub = provider

	owner := nostr.Generate()
	repository := gitForgeRepositoryEvent(t, owner, "notes",
		nostr.Tag{"clone", "https://github.com/lapis/notes.git"},
		nostr.Tag{"web", "https://github.com/lapis/notes"})
	_, err := store.ReplaceEvent(repository)
	require.NoError(t, err)
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), repository))
	require.NoError(t, policy.Remember(context.Background(), repository))
	require.NoError(t, policy.setExplicitIntegration(context.Background(), forge.Integration{
		Coordinate:     projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"},
		AnnouncementID: repository.ID.Hex(), ConfigurationSource: forge.ConfigurationExplicit,
		GitBackend: forge.BackendGitHub, CollaborationBackend: forge.BackendGitHub,
		ConnectionID: "connection", ExternalRepositoryID: "42", ExternalRepository: "lapis/notes",
		HealthState: "ready",
	}, repository.ID.Hex()))
	return policy, store, provider, target, owner, repository
}

func TestGitHubToWalGitMigrationRequiresVerifiedImportAndOwnerAnnouncement(t *testing.T) {
	refs := map[string]string{
		"refs/heads/main": strings.Repeat("a", 40),
		"refs/tags/v1":    strings.Repeat("b", 40),
	}
	policy, store, source, target, owner, repository := configureForgeMigrationTest(t, []map[string]string{refs, refs})
	admin := store.Config.secret
	url := "https://relay.example/api/forge/repositories/" + repository.PubKey.Hex() + "/notes/migration"
	body := []byte(`{"expected_announcement_id":"` + repository.ID.Hex() + `","acknowledge_provider_write_freeze":true,"keep_github_collaboration":true,"retain_github_mirror":true}`)
	request := signedForgeRequest(t, http.MethodPost, url, body, admin)
	request.Header.Set("Idempotency-Key", "migration-1")
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, request)
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	var accepted map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &accepted))
	operationID := accepted["operation_id"].(string)

	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Equal(t, 2, source.refSnapshotCalls)
	require.Len(t, target.requests, 1)
	require.Equal(t, refs, target.requests[0].ExpectedRefs)
	require.Equal(t, "https://github.com/lapis/notes.git", target.requests[0].SourceCloneURL)
	require.Equal(t, "x-access-token", target.requests[0].SourceUsername)
	require.Equal(t, "opaque-installation-token", target.requests[0].SourcePassword)
	migration, found, err := policy.loadForgeMigration(context.Background(), operationID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "awaiting-owner-announcement", migration.State)
	beforeActivation, _, err := policy.repositoryIntegration(context.Background(), migration.Coordinate)
	require.NoError(t, err)
	require.Equal(t, forge.BackendGitHub, beforeActivation.GitBackend)
	require.Equal(t, "migrating", beforeActivation.HealthState)

	canonical := "https://relay.example/git/" + owner.Public().Hex() + "/notes"
	hosted := gitForgeRepositoryEvent(t, owner, "notes", nostr.Tag{"clone", canonical})
	hosted.CreatedAt = repository.CreatedAt + 1
	require.NoError(t, hosted.Sign(owner))
	_, err = store.ReplaceEvent(hosted)
	require.NoError(t, err)
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), hosted))
	require.NoError(t, policy.Remember(context.Background(), hosted))

	activateURL := url + "/activate"
	activateBody := []byte(`{"expected_announcement_id":"` + hosted.ID.Hex() + `"}`)
	recorder = httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodPost, activateURL, activateBody, admin))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	integration, found, err := policy.repositoryIntegration(context.Background(), migration.Coordinate)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, hosted.ID.Hex(), integration.AnnouncementID)
	require.Equal(t, forge.BackendWalGit, integration.GitBackend)
	require.Equal(t, forge.BackendGitHub, integration.CollaborationBackend)
	require.Empty(t, integration.MirrorBackend)
	require.Equal(t, forge.BackendGitHub, integration.GitReplicationTargetBackend)
	require.Equal(t, "ready", integration.HealthState)
	require.NoError(t, policy.ProcessGitReplicationOnce(context.Background()))
	require.Len(t, target.replications, 1)
	require.Equal(t, "https://github.com/lapis/notes.git", target.replications[0].CloneURL)
	require.Equal(t, "opaque-replication-token", target.replications[0].Password)
	refEvent := normalizedRefEvent{
		Coordinate: migration.Coordinate, Action: "update", RefName: "refs/heads/main",
		Old: strings.Repeat("a", 40), New: strings.Repeat("c", 40),
		Pusher: "nostr:" + owner.Public().Hex(), Sequence: "10",
	}
	require.NoError(t, policy.storeWalGitEvents(context.Background(), []normalizedRefEvent{refEvent}))
	require.NoError(t, policy.ProcessGitReplicationOnce(context.Background()))
	require.Len(t, target.replications, 2, "native WalGit refs drive the distinct outbound Git target")
	migration, found, err = policy.loadForgeMigration(context.Background(), operationID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "complete", migration.State)
	require.Equal(t, hosted.ID.Hex(), migration.TargetAnnouncementID)
}

func TestGitHubToWalGitMigrationFailsClosedWhenSourceRefsChange(t *testing.T) {
	before := map[string]string{"refs/heads/main": strings.Repeat("a", 40)}
	after := map[string]string{"refs/heads/main": strings.Repeat("b", 40)}
	policy, store, _, target, _, repository := configureForgeMigrationTest(t, []map[string]string{before, after})
	resolved, found, err := policy.ResolveIntegration(context.Background(), repositoryCoordinate(t, repository))
	require.NoError(t, err)
	require.True(t, found)
	operation, err := policy.enqueueForgeMigration(context.Background(), store.Config.secret.Public(), resolved.Repository, "migration-ref-change", false, false)
	require.NoError(t, err)
	err = policy.ProcessForgeOutboxOnce(context.Background())
	require.Error(t, err)
	require.Len(t, target.requests, 1, "the destination import is verified before the second source snapshot")
	loadedOperation, found, loadErr := policy.loadForgeOperation(context.Background(), operation.ID)
	require.NoError(t, loadErr)
	require.True(t, found)
	require.Equal(t, forgeOperationFailed, loadedOperation.State)
	require.Equal(t, "source_refs_changed", loadedOperation.Failure)
	migration, found, loadErr := policy.loadForgeMigration(context.Background(), operation.ID)
	require.NoError(t, loadErr)
	require.True(t, found)
	require.Equal(t, "failed", migration.State)
	integration, _, loadErr := policy.repositoryIntegration(context.Background(), migration.Coordinate)
	require.NoError(t, loadErr)
	require.Equal(t, forge.BackendGitHub, integration.GitBackend)
	require.Equal(t, "degraded", integration.HealthState)
}

func TestForgeMigrationActivationRejectsStaleProviderAnnouncement(t *testing.T) {
	refs := map[string]string{"refs/heads/main": strings.Repeat("a", 40)}
	policy, store, _, _, _, repository := configureForgeMigrationTest(t, []map[string]string{refs, refs})
	resolved, found, err := policy.ResolveIntegration(context.Background(), repositoryCoordinate(t, repository))
	require.NoError(t, err)
	require.True(t, found)
	_, err = policy.enqueueForgeMigration(context.Background(), store.Config.secret.Public(), resolved.Repository, "migration-stale", false, false)
	require.NoError(t, err)
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	_, err = policy.activateForgeMigration(context.Background(), forge.Repository{
		Coordinate: repositoryCoordinate(t, repository), Announcement: repository,
		Classification: "external",
	}, repository.ID.Hex())
	var backendError *forge.Error
	require.ErrorAs(t, err, &backendError)
	require.Equal(t, "canonical_walgit_announcement_required", backendError.Code)
}

func TestRetainedGitMirrorDoesNotEnableCollaborationProjection(t *testing.T) {
	refs := map[string]string{"refs/heads/main": strings.Repeat("a", 40)}
	policy, store, provider, _, owner, repository := configureForgeMigrationTest(t, []map[string]string{refs, refs})
	resolved, found, err := policy.ResolveIntegration(context.Background(), repositoryCoordinate(t, repository))
	require.NoError(t, err)
	require.True(t, found)
	operation, err := policy.enqueueForgeMigration(context.Background(), store.Config.secret.Public(), resolved.Repository, "migration-mirror", false, true)
	require.NoError(t, err)
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))

	canonical := "https://relay.example/git/" + owner.Public().Hex() + "/notes"
	hosted := gitForgeRepositoryEvent(t, owner, "notes", nostr.Tag{"clone", canonical})
	hosted.CreatedAt = repository.CreatedAt + 1
	require.NoError(t, hosted.Sign(owner))
	_, err = store.ReplaceEvent(hosted)
	require.NoError(t, err)
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), hosted))
	require.NoError(t, policy.Remember(context.Background(), hosted))
	resolved, found, err = policy.ResolveIntegration(context.Background(), repositoryCoordinate(t, hosted))
	require.NoError(t, err)
	require.True(t, found)
	_, err = policy.activateForgeMigration(context.Background(), resolved.Repository, hosted.ID.Hex())
	require.NoError(t, err)

	coordinate := repositoryCoordinate(t, hosted)
	issue := nostr.Event{Kind: nostr.KindIssue, CreatedAt: nostr.Now(), Content: "Mirror this", Tags: nostr.Tags{
		{"a", coordinate.String()}, {"p", owner.Public().Hex()}, {"subject", "Relay-owned issue"},
	}}
	require.NoError(t, issue.Sign(owner))
	require.NoError(t, store.SaveEvent(issue))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), issue))
	require.NoError(t, policy.Remember(context.Background(), issue))
	provider.result = forge.CollaborationResult{ExternalID: "mirror-91", ExternalNumber: 91, ExternalURL: "https://github.com/lapis/notes/issues/91"}
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Empty(t, provider.commands)
	mapping, found, err := policy.loadMappingByNostrRoot(context.Background(), forge.BackendGitHub, "connection", issue.ID.Hex())
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, mapping.NostrEventID)
	migration, found, err := policy.loadForgeMigration(context.Background(), operation.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "complete", migration.State)
}

func repositoryCoordinate(t *testing.T, event nostr.Event) projecttask.RepositoryCoordinate {
	t.Helper()
	coordinate, err := projecttask.ParseRepositoryCoordinate("30617:" + event.PubKey.Hex() + ":" + event.Tags.GetD())
	require.NoError(t, err)
	return coordinate
}
