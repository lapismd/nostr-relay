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
)

func configureGitReplicaTest(t *testing.T, snapshots []map[string]string) (*GitForgePolicy, *EventStore, *fakeGitHubProvider, *fakeWalGitImporter, nostr.SecretKey, nostr.Event) {
	t.Helper()
	policy, store, source, target, owner, repository := configureForgeMigrationTest(t, snapshots)
	coordinate := repositoryCoordinate(t, repository)
	integration := forge.Integration{
		Coordinate: coordinate, AnnouncementID: repository.ID.Hex(),
		ConfigurationSource: forge.ConfigurationExplicit,
		GitBackend:          forge.BackendGitHub, GitAuthorityBackend: forge.BackendGitHub,
		ContentBackend: forge.BackendWalGit, CollaborationBackend: forge.BackendRelay,
		ConnectionID: "connection", ExternalRepositoryID: "42", ExternalRepository: "lapis/notes",
		HealthState: "provisioning",
		Replica: &forge.ReplicaConfig{
			Enabled: true, AuthorityBackend: forge.BackendGitHub, ReplicaBackend: forge.BackendWalGit,
			Direction: replicaDirectionAuthorityToReplica, SyncBranches: true, SyncTags: true,
			Mode: forge.ReplicaModeContinuous, ReadPolicy: forge.ReplicaReadFallback,
		},
	}
	require.NoError(t, policy.setExplicitIntegration(context.Background(), integration, repository.ID.Hex()))
	require.NoError(t, policy.configureGitReplica(context.Background(), integration))
	return policy, store, source, target, owner, repository
}

func TestGitReplicaProvisionForceDeleteDedupAndReconcile(t *testing.T) {
	a := strings.Repeat("a", 40)
	b := strings.Repeat("b", 40)
	c := strings.Repeat("c", 40)
	d := strings.Repeat("d", 40)
	initial := map[string]string{"refs/heads/main": a, "refs/tags/v1": b, "refs/pull/1/head": c}
	changed := map[string]string{"refs/heads/main": c, "refs/heads/topic": d, "refs/tags/v1": d}
	final := map[string]string{"refs/heads/main": c, "refs/tags/v1": d}
	policy, _, source, target, _, repository := configureGitReplicaTest(t, []map[string]string{initial, initial, changed, changed, final, final, final, final})

	require.NoError(t, policy.ProcessGitReplicaOnce(context.Background()))
	require.Len(t, target.requests, 1)
	require.Equal(t, map[string]string{"refs/heads/main": a, "refs/tags/v1": b}, target.requests[0].ExpectedRefs)
	require.Equal(t, replicaPrincipal("connection"), target.requests[0].DestinationPrincipal)
	require.True(t, target.requests[0].SyncBranches)
	require.True(t, target.requests[0].SyncTags)
	replica, found, err := policy.loadGitReplica(context.Background(), repositoryCoordinate(t, repository))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "current", replica.State)

	delivery := githubWebhookDelivery{Provider: forge.BackendGitHub, DeliveryID: "push-force", ConnectionID: "connection", ExternalRepositoryID: "42"}
	payload := githubWebhookPayload{Ref: "refs/heads/main", Before: a, After: c, Forced: true}
	require.NoError(t, policy.recordGitHubPush(context.Background(), delivery, payload))
	require.NoError(t, policy.recordGitHubPush(context.Background(), delivery, payload))
	var jobs int
	require.NoError(t, GetDb().QueryRow(policy.Events.Schema.Render(`SELECT COUNT(*) FROM {{.Name}}__git_replica_jobs WHERE source_delivery_id = 'push-force'`)).Scan(&jobs))
	require.Equal(t, 1, jobs, "GitHub webhook redelivery must enqueue one sync")
	require.NoError(t, policy.ProcessGitReplicaOnce(context.Background()))
	require.Equal(t, changed, target.requests[1].ExpectedRefs)

	deleteDelivery := githubWebhookDelivery{Provider: forge.BackendGitHub, DeliveryID: "push-delete", ConnectionID: "connection", ExternalRepositoryID: "42"}
	require.NoError(t, policy.recordGitHubPush(context.Background(), deleteDelivery, githubWebhookPayload{
		Ref: "refs/heads/topic", Before: d, After: strings.Repeat("0", 40), Deleted: true,
	}))
	require.NoError(t, policy.ProcessGitReplicaOnce(context.Background()))
	require.Equal(t, final, target.requests[2].ExpectedRefs)

	// An older webhook cannot roll the replica backwards: each durable job
	// snapshots current authority instead of trusting the delivery's target.
	require.NoError(t, policy.recordGitHubPush(context.Background(), githubWebhookDelivery{
		Provider: forge.BackendGitHub, DeliveryID: "push-late", ConnectionID: "connection", ExternalRepositoryID: "42",
	}, githubWebhookPayload{Ref: "refs/heads/main", Before: b, After: a}))
	require.NoError(t, policy.ProcessGitReplicaOnce(context.Background()))
	require.Equal(t, final, target.requests[3].ExpectedRefs)
	refs, err := policy.gitReplicaRefs(context.Background(), repositoryCoordinate(t, repository))
	require.NoError(t, err)
	require.Equal(t, final, refs)

	require.NoError(t, policy.ReconcileGitReplicasOnce(context.Background()))
	require.NoError(t, policy.ProcessGitReplicaOnce(context.Background()))
	replica, found, err = policy.loadGitReplica(context.Background(), repositoryCoordinate(t, repository))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "current", replica.State)
	require.True(t, replica.LastReconcileAt.Valid)
	require.GreaterOrEqual(t, source.refSnapshotCalls, 8)
}

func TestOnDemandReplicaRecordsDriftWithoutContinuousSync(t *testing.T) {
	sha := strings.Repeat("a", 40)
	next := strings.Repeat("b", 40)
	refs := map[string]string{"refs/heads/main": sha}
	policy, _, _, _, _, repository := configureGitReplicaTest(t, []map[string]string{refs, refs})
	_, err := GetDb().Exec(policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations SET replica_mode = 'on-demand'
		WHERE owner = $1 AND repo = $2`), repository.PubKey.Hex(), "notes")
	require.NoError(t, err)
	require.NoError(t, policy.ProcessGitReplicaOnce(context.Background()))

	delivery := githubWebhookDelivery{Provider: forge.BackendGitHub, DeliveryID: "push-on-demand", ConnectionID: "connection", ExternalRepositoryID: "42"}
	require.NoError(t, policy.recordGitHubPush(context.Background(), delivery, githubWebhookPayload{
		Ref: "refs/heads/main", Before: sha, After: next,
	}))
	replica, found, err := policy.loadGitReplica(context.Background(), repositoryCoordinate(t, repository))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "stale", replica.State)

	var jobs int
	require.NoError(t, GetDb().QueryRow(policy.Events.Schema.Render(`
		SELECT COUNT(*) FROM {{.Name}}__git_replica_jobs
		WHERE source_delivery_id = 'push-on-demand'`)).Scan(&jobs))
	require.Zero(t, jobs, "on-demand mode waits for a content read to enqueue synchronization")
}

func TestForgeAPIAttachesGitHubAuthorityWithPrivateWalGitContent(t *testing.T) {
	sha := strings.Repeat("a", 40)
	policy, store := createGitForgeTestPolicy(t, nil)
	provider := &fakeGitHubProvider{
		refSnapshots: []map[string]string{{"refs/heads/main": sha}, {"refs/heads/main": sha}},
		cloneURL:     "https://github.com/lapis/notes.git", cloneUsername: "x-access-token", clonePassword: "opaque-token",
	}
	provider.repository.ID = 42
	provider.repository.FullName = "lapis/notes"
	provider.repository.CloneURL = "https://github.com/lapis/notes.git"
	provider.repository.HTMLURL = "https://github.com/lapis/notes"
	target := &fakeWalGitImporter{fakeGitHubProvider: &fakeGitHubProvider{}}
	require.NoError(t, policy.Registry.RegisterGit(provider))
	require.NoError(t, policy.Registry.RegisterGit(target))
	policy.GitHub = provider
	owner := nostr.Generate()
	repository := gitForgeRepositoryEvent(t, owner, "notes",
		nostr.Tag{"clone", provider.repository.CloneURL}, nostr.Tag{"web", provider.repository.HTMLURL})
	require.NoError(t, store.SaveEvent(repository))
	require.NoError(t, policy.Remember(context.Background(), repository))

	body, err := json.Marshal(map[string]any{
		"expected_announcement_id": repository.ID.Hex(),
		"git_authority_backend":    forge.BackendGitHub,
		"content_backend":          forge.BackendWalGit,
		"collaboration_backend":    forge.BackendRelay,
		"connection_id":            "connection", "external_repository_id": "42",
		"replica": map[string]any{"enabled": true, "mode": "continuous", "read_policy": "fallback"},
	})
	require.NoError(t, err)
	targetURL := "https://relay.example/api/forge/repositories/" + repository.PubKey.Hex() + "/notes/integration"
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodPut, targetURL, body, store.Config.secret))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"git_authority_backend":"github"`)
	require.Contains(t, recorder.Body.String(), `"content_backend":"walgit"`)
	require.Contains(t, recorder.Body.String(), `"local_replica":true`)
	require.Contains(t, recorder.Body.String(), `"url":"https://github.com/lapis/notes.git"`)
	require.NotContains(t, recorder.Body.String(), "/git/"+repository.PubKey.Hex())
	require.NoError(t, policy.ProcessGitReplicaOnce(context.Background()))
	require.Len(t, target.requests, 1)
}

func TestGitHubReplicaCannotBeClonedOrReadAroundNIP29(t *testing.T) {
	sha := strings.Repeat("a", 40)
	refs := map[string]string{"refs/heads/main": sha}
	policy, _, _, _, owner, repository := configureGitReplicaTest(t, []map[string]string{refs, refs})
	grantGitGroup(policy, "channel", owner.Public(), "private")
	require.NoError(t, policy.ProcessGitReplicaOnce(context.Background()))

	gitURL := "https://relay.example/git/" + repository.PubKey.Hex() + "/notes.git/info/refs?service=git-receive-pack"
	gitRequest := httptest.NewRequest(http.MethodGet, gitURL, nil)
	signedGitRequest(t, gitRequest, owner, http.MethodGet, "https://relay.example/git/"+repository.PubKey.Hex()+"/notes.git")
	recorder := httptest.NewRecorder()
	policy.ServeGitHTTP(recorder, gitRequest)
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Contains(t, recorder.Body.String(), "provider_git_required")

	readURL := "https://relay.example/api/git/" + repository.PubKey.Hex() + "/notes/resolve/" + sha
	readRequest := httptest.NewRequest(http.MethodGet, readURL, nil)
	signedGitRequest(t, readRequest, nostr.Generate(), http.MethodGet, readURL)
	recorder = httptest.NewRecorder()
	policy.ServeRepositoryAPI(recorder, readRequest)
	require.Equal(t, http.StatusNotFound, recorder.Code, "private replica content remains behind NIP-29")
}

func TestGitReplicaReadRoutingFallbackStaleAndObjectPin(t *testing.T) {
	sha := strings.Repeat("a", 40)
	missing := strings.Repeat("b", 40)
	refs := map[string]string{"refs/heads/main": sha}
	policy, _, source, target, _, repository := configureGitReplicaTest(t, []map[string]string{refs, refs})
	require.NoError(t, policy.ProcessGitReplicaOnce(context.Background()))
	resolved, found, err := policy.ResolveIntegration(context.Background(), repositoryCoordinate(t, repository))
	require.NoError(t, err)
	require.True(t, found)

	result, err := policy.repositoryAPIResult(context.Background(), resolved, "resolve/"+sha, "reader", nil)
	require.NoError(t, err)
	require.Equal(t, sha, result.(forge.Revision).SHA)
	require.Equal(t, []string{sha}, target.resolveRequests)
	require.Empty(t, source.resolveRequests)

	require.NoError(t, policy.setGitReplicaState(context.Background(), resolved.Repository.Coordinate, "stale", "", false))
	resolved, _, err = policy.ResolveIntegration(context.Background(), resolved.Repository.Coordinate)
	require.NoError(t, err)
	result, err = policy.repositoryAPIResult(context.Background(), resolved, "resolve/main", "reader", nil)
	require.NoError(t, err)
	require.Equal(t, "main", result.(forge.Revision).SHA)
	require.Equal(t, []string{"main"}, source.resolveRequests)

	if target.missingObjects == nil {
		target.missingObjects = make(map[string]bool)
	}
	target.missingObjects[missing] = true
	result, err = policy.repositoryAPIResult(context.Background(), resolved, "resolve/"+missing, "reader", nil)
	require.NoError(t, err)
	require.Equal(t, missing, result.(forge.Revision).SHA)
	require.Len(t, target.objectRequests, 1)
	require.Equal(t, missing, target.objectRequests[0].ObjectOID)
	require.Equal(t, replicaPrincipal("connection"), target.objectRequests[0].DestinationPrincipal)
	require.NotContains(t, source.resolveRequests, missing, "a pinned SHA is served locally after import")

	resolved.Repository.Integration.Replica.ReadPolicy = forge.ReplicaReadStale
	resolved.Repository.Integration.Replica.WaitMilliseconds = 0
	result, err = policy.repositoryAPIResult(context.Background(), resolved, "resolve/main", "reader", nil)
	require.NoError(t, err)
	require.Contains(t, result.(forge.Revision).Incomplete, "stale-replica")

	resolved.Repository.Integration.Replica.ReadPolicy = forge.ReplicaReadWait
	resolved.Repository.Integration.Replica.WaitMilliseconds = 5
	_, err = policy.repositoryAPIResult(context.Background(), resolved, "resolve/main", "reader", nil)
	var forgeError *forge.Error
	require.ErrorAs(t, err, &forgeError)
	require.Equal(t, "replica_stale", forgeError.Code)
}

func TestReplicaWalGitEventsAreMarkedAndCannotCreateLoops(t *testing.T) {
	sha := strings.Repeat("a", 40)
	policy, _, _, _, _, repository := configureGitReplicaTest(t, []map[string]string{{"refs/heads/main": sha}, {"refs/heads/main": sha}})
	require.NoError(t, policy.ProcessGitReplicaOnce(context.Background()))
	zero := strings.Repeat("0", 40)
	event := normalizedRefEvent{
		Coordinate: repositoryCoordinate(t, repository), Action: "create", RefName: "refs/heads/internal",
		Old: zero, New: sha, Pusher: replicaPrincipal("connection"), CorrelationID: "replica-sync", Sequence: "1",
	}
	require.NoError(t, policy.storeWalGitEvents(context.Background(), []normalizedRefEvent{event}))
	var replicaGenerated bool
	require.NoError(t, GetDb().QueryRow(policy.Events.Schema.Render(`
		SELECT replica_generated FROM {{.Name}}__walgit_ref_events
		WHERE owner = $1 AND repo = $2 AND sequence = 1`), repository.PubKey.Hex(), "notes").Scan(&replicaGenerated))
	require.True(t, replicaGenerated)
	_, _, found, err := policy.currentRef(context.Background(), event.Coordinate, event.RefName)
	require.NoError(t, err)
	require.False(t, found, "replica events do not enter the native authoritative ref projection")

	event.Sequence = "2"
	event.Pusher = "nostr:" + repository.PubKey.Hex()
	require.NoError(t, policy.storeWalGitEvents(context.Background(), []normalizedRefEvent{event}))
	var count int
	require.NoError(t, GetDb().QueryRow(policy.Events.Schema.Render(`SELECT COUNT(*) FROM {{.Name}}__walgit_ref_events WHERE owner = $1 AND repo = $2`), repository.PubKey.Hex(), "notes").Scan(&count))
	require.Equal(t, 1, count, "a normal principal cannot make a GitHub replica authoritative")
	var outbox, ciRuns int
	require.NoError(t, GetDb().QueryRow(policy.Events.Schema.Render(`SELECT COUNT(*) FROM {{.Name}}__forge_outbox WHERE owner = $1 AND repo = $2`), repository.PubKey.Hex(), "notes").Scan(&outbox))
	require.Zero(t, outbox)
	require.Zero(t, ciRuns)
}

func TestMigrationReusesCurrentReplica(t *testing.T) {
	sha := strings.Repeat("a", 40)
	refs := map[string]string{"refs/heads/main": sha}
	policy, store, source, target, _, repository := configureGitReplicaTest(t, []map[string]string{refs, refs, refs, refs})
	require.NoError(t, policy.ProcessGitReplicaOnce(context.Background()))
	require.Len(t, target.requests, 1)
	resolved, found, err := policy.ResolveIntegration(context.Background(), repositoryCoordinate(t, repository))
	require.NoError(t, err)
	require.True(t, found)
	operation, err := policy.enqueueForgeMigration(context.Background(), store.Config.secret.Public(), resolved.Repository, "reuse-replica", false, false)
	require.NoError(t, err)
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Len(t, target.requests, 1, "migration must reuse the verified replica instead of importing again")
	require.GreaterOrEqual(t, source.refSnapshotCalls, 4)
	migration, found, err := policy.loadForgeMigration(context.Background(), operation.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "awaiting-owner-announcement", migration.State)
}

var _ forge.RepositoryObjectImporter = (*fakeWalGitImporter)(nil)
