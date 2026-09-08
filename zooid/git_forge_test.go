package zooid

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

type fakeGitBackend struct {
	mu      sync.Mutex
	ensured []projecttask.RepositoryCoordinate
	err     error
}

func (backend *fakeGitBackend) EnsureRepository(_ context.Context, coordinate projecttask.RepositoryCoordinate) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.ensured = append(backend.ensured, coordinate)
	return backend.err
}

func (*fakeGitBackend) ProxyGitHTTP(http.ResponseWriter, *http.Request, gitforge.Repository, string, string) {
}
func (*fakeGitBackend) ProxyRepositoryAPI(http.ResponseWriter, *http.Request, gitforge.Repository, string, string) {
}
func (*fakeGitBackend) Resolve(context.Context, gitforge.Repository, string, string) (string, error) {
	return "", nil
}
func (*fakeGitBackend) Compare(context.Context, gitforge.Repository, gitforge.CompareRequest) (gitforge.CompareResult, error) {
	return gitforge.CompareResult{}, nil
}

func createGitForgeTestPolicy(t *testing.T, backend gitforge.Backend) (*GitForgePolicy, *EventStore) {
	t.Helper()
	store := createTestEventStore()
	store.Config.Info.Pubkey = store.Config.secret.Public().Hex()
	store.Config.Policy.Open = true
	store.Config.Groups.Enabled = true
	store.Config.Git = GitConfig{
		Enabled: true, PublicURL: "https://relay.example", BackendURL: "http://walgit:8080",
		ReconcileInterval: "1h", MaxDiffBytes: 8 << 20, MaxDiffFiles: 2000,
	}
	require.NoError(t, store.Init())
	management := &ManagementStore{Config: store.Config, Events: store}
	groups := &GroupStore{Config: store.Config, Events: store, Management: management, cachesWarmed: true}
	projects := NewProjectTaskPolicy(store, groups)
	return NewGitForgePolicy(context.Background(), store.Config, store, groups, projects, backend), store
}

func gitForgeRepositoryEvent(t *testing.T, secret nostr.SecretKey, id string, extra ...nostr.Tag) nostr.Event {
	t.Helper()
	event := nostr.Event{
		Kind: nostr.KindRepositoryAnnouncement, CreatedAt: nostr.Now(),
		Tags: nostr.Tags{{"d", id}, {projecttask.TagBuzzChannel, "channel"}},
	}
	event.Tags = append(event.Tags, extra...)
	require.NoError(t, event.Sign(secret))
	return event
}

func TestGitForgeReconcilesHostedRepositoryIdempotently(t *testing.T) {
	backend := &fakeGitBackend{}
	policy, store := createGitForgeTestPolicy(t, backend)
	event := gitForgeRepositoryEvent(t, nostr.Generate(), "notes")
	require.NoError(t, store.SaveEvent(event))
	require.NoError(t, policy.Remember(context.Background(), event))

	require.NoError(t, policy.ReconcileOnce(context.Background()))
	state, found, err := policy.intentState(context.Background(), projecttask.RepositoryCoordinate{Owner: event.PubKey, ID: "notes"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "ready", state)
	require.Len(t, backend.ensured, 1)
	integration, found, err := policy.repositoryIntegration(context.Background(), projecttask.RepositoryCoordinate{Owner: event.PubKey, ID: "notes"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "walgit", integration.GitBackend)
	require.Equal(t, "relay", integration.CollaborationBackend)
	require.Equal(t, "legacy-default", integration.ConfigurationSource)
	require.Equal(t, "ready", integration.HealthState)

	require.NoError(t, policy.ReconcileOnce(context.Background()))
	require.Len(t, backend.ensured, 1)
}

func TestLegacyIntegrationTracksClassificationUntilExplicitlyConfigured(t *testing.T) {
	backend := &fakeGitBackend{}
	policy, store := createGitForgeTestPolicy(t, backend)
	secret := nostr.Generate()
	hosted := gitForgeRepositoryEvent(t, secret, "notes")
	require.NoError(t, store.SaveEvent(hosted))
	require.NoError(t, policy.Remember(context.Background(), hosted))

	coordinate := projecttask.RepositoryCoordinate{Owner: hosted.PubKey, ID: "notes"}
	integration, found, err := policy.repositoryIntegration(context.Background(), coordinate)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "walgit", integration.GitBackend)

	_, err = GetDb().Exec(policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations
		SET configuration_source = 'explicit', git_backend = 'github',
		    connection_id = 'connection', external_repository_id = '42'
		WHERE owner = $1 AND repo = $2`), hosted.PubKey.Hex(), "notes")
	require.NoError(t, err)

	external := gitForgeRepositoryEvent(t, secret, "notes", nostr.Tag{"clone", "https://github.com/lapis/notes.git"})
	external.CreatedAt = hosted.CreatedAt + 1
	require.NoError(t, external.Sign(secret))
	require.NoError(t, store.SaveEvent(external))
	require.NoError(t, policy.Remember(context.Background(), external))
	integration, found, err = policy.repositoryIntegration(context.Background(), coordinate)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "explicit", integration.ConfigurationSource)
	require.Equal(t, "github", integration.GitBackend)
	require.Equal(t, "42", integration.ExternalRepositoryID)
}

func TestGitForgeAcceptsFailureAndSchedulesRetry(t *testing.T) {
	backend := &fakeGitBackend{err: errors.New("sidecar unavailable\nAuthorization: secret")}
	policy, store := createGitForgeTestPolicy(t, backend)
	event := gitForgeRepositoryEvent(t, nostr.Generate(), "notes")
	require.NoError(t, store.SaveEvent(event))
	require.NoError(t, policy.Remember(context.Background(), event))
	require.NoError(t, policy.ReconcileOnce(context.Background()))

	var state, lastError string
	var attempts int
	err := GetDb().QueryRow(policy.Events.Schema.Render(`
		SELECT reconcile_state, attempt_count, last_error FROM {{.Name}}__git_repository_intents
		WHERE owner = $1 AND repo = $2`), event.PubKey.Hex(), "notes").Scan(&state, &attempts, &lastError)
	require.NoError(t, err)
	require.Equal(t, "retry", state)
	require.Equal(t, 1, attempts)
	require.NotContains(t, lastError, "\n")
}

func TestGitForgeDoesNotProvisionExternalOrTaskAnchor(t *testing.T) {
	backend := &fakeGitBackend{}
	policy, store := createGitForgeTestPolicy(t, backend)
	secret := nostr.Generate()
	events := []nostr.Event{
		gitForgeRepositoryEvent(t, secret, "external", nostr.Tag{"clone", "https://github.com/acme/external.git"}),
		gitForgeRepositoryEvent(t, secret, "anchor", nostr.Tag{projecttask.TagProjectTaskAnchor, "true"}),
	}
	for _, event := range events {
		require.NoError(t, store.SaveEvent(event))
		require.NoError(t, policy.Remember(context.Background(), event))
	}
	require.NoError(t, policy.ReconcileOnce(context.Background()))
	require.Empty(t, backend.ensured)
	for _, event := range events {
		state, found, err := policy.intentState(context.Background(), projecttask.RepositoryCoordinate{Owner: event.PubKey, ID: event.Tags.GetD()})
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "inactive", state)
		integration, integrationFound, integrationErr := policy.repositoryIntegration(context.Background(), projecttask.RepositoryCoordinate{Owner: event.PubKey, ID: event.Tags.GetD()})
		require.NoError(t, integrationErr)
		require.True(t, integrationFound)
		require.Equal(t, "ready", integration.HealthState)
		if event.Tags.GetD() == "external" {
			require.Equal(t, "unmanaged", integration.GitBackend)
		} else {
			require.Equal(t, "none", integration.GitBackend)
		}
	}
}

func TestGitForgeHostedValidationIsConfigurationDependent(t *testing.T) {
	backend := &fakeGitBackend{}
	policy, _ := createGitForgeTestPolicy(t, backend)
	invalid := gitForgeRepositoryEvent(t, nostr.Generate(), "not/a/walgit/name")
	handled, reason := policy.CheckWrite(invalid)
	require.True(t, handled)
	require.Contains(t, reason, "ASCII")

	external := invalid
	external.Tags = append(invalid.Tags.CloneDeep(), nostr.Tag{"clone", "https://example.com/repo.git"})
	external.ID = nostr.ZeroID
	require.NoError(t, external.Sign(nostr.Generate()))
	_, reason = policy.CheckWrite(external)
	require.Empty(t, reason)
}

func TestGitForgeTombstoneMakesIntentInactiveWithoutDeletingStorage(t *testing.T) {
	backend := &fakeGitBackend{}
	policy, store := createGitForgeTestPolicy(t, backend)
	secret := nostr.Generate()
	event := gitForgeRepositoryEvent(t, secret, "notes")
	require.NoError(t, store.SaveEvent(event))
	require.NoError(t, policy.Remember(context.Background(), event))
	tombstone := nostr.Event{Kind: nostr.KindDeletion, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"a", projecttask.RepositoryCoordinate{Owner: event.PubKey, ID: "notes"}.String()}}}
	require.NoError(t, tombstone.Sign(secret))
	require.NoError(t, store.SaveEvent(tombstone))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), tombstone))
	require.NoError(t, policy.Remember(context.Background(), tombstone))

	state, found, err := policy.intentState(context.Background(), projecttask.RepositoryCoordinate{Owner: event.PubKey, ID: "notes"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "inactive", state)
	integration, integrationFound, integrationErr := policy.repositoryIntegration(context.Background(), projecttask.RepositoryCoordinate{Owner: event.PubKey, ID: "notes"})
	require.NoError(t, integrationErr)
	require.True(t, integrationFound)
	require.Equal(t, "inactive", integration.HealthState)
	require.Empty(t, backend.ensured)

	// Startup rebuilds desired state from signed events. An HTTP-published
	// tombstone leaves the target row present, so the visible NIP-09 projection
	// must prevent that old announcement from becoming pending again.
	require.NoError(t, policy.rebuildIntents(context.Background()))
	state, found, err = policy.intentState(context.Background(), projecttask.RepositoryCoordinate{Owner: event.PubKey, ID: "notes"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "inactive", state)
}

func TestSanitizeGitErrorBoundsAndFlattens(t *testing.T) {
	message := sanitizeGitError(errors.New(strings.Repeat("x", 600) + "\nnext"))
	require.Len(t, message, 512)
	require.NotContains(t, message, "\n")
}
