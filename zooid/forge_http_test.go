package zooid

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"zooid/internal/forge"
	"zooid/internal/githubforge"
)

type fakeGitHubProvider struct {
	repository       githubforge.RepositoryMetadata
	commands         []forge.CollaborationCommand
	result           forge.CollaborationResult
	refSnapshots     []map[string]string
	refSnapshotCalls int
	cloneURL         string
	cloneUsername    string
	clonePassword    string
	resolveRequests  []string
	ciStarts         []forge.CIRequest
	ciRun            forge.CIRun
	ciStatus         forge.CIRun
	ciCancelled      []string
	ciCleaned        []string
}

func (provider *fakeGitHubProvider) ValidateConnection(_ context.Context, connection githubforge.Connection) (githubforge.Connection, error) {
	connection.AccountID = "7"
	connection.AccountLogin = "lapis"
	connection.Host = "github.com"
	connection.Status = "active"
	return connection, nil
}
func (provider *fakeGitHubProvider) ListRepositories(context.Context, githubforge.Connection) ([]githubforge.RepositoryMetadata, error) {
	return []githubforge.RepositoryMetadata{provider.repository}, nil
}
func (provider *fakeGitHubProvider) RepositoryByID(context.Context, string, int64) (githubforge.RepositoryMetadata, error) {
	return provider.repository, nil
}
func (*fakeGitHubProvider) Name() string { return forge.BackendGitHub }
func (provider *fakeGitHubProvider) AuthorityCapabilities(_ context.Context, repository forge.Repository) (forge.GitAuthorityCapabilities, error) {
	return forge.GitAuthorityCapabilities{
		Clone:     forge.CloneDescriptor{Available: true, URL: "https://github.com/" + repository.Integration.ExternalRepository + ".git", Auth: forge.CloneAuthProvider, Provider: forge.BackendGitHub},
		RefEvents: true,
	}, nil
}
func (*fakeGitHubProvider) ContentCapabilities(context.Context, forge.Repository) (forge.ContentCapabilities, error) {
	return forge.ContentCapabilities{Browse: true, Compare: true, Limits: forge.ContentLimits{MaximumBlobBytes: 100 << 20, MaximumChangedFiles: 300, MaximumTreeEntries: 1000, MayTruncateCompare: true, MayTruncateTree: true}}, nil
}
func (*fakeGitHubProvider) Capabilities(_ context.Context, repository forge.Repository) (forge.Capabilities, error) {
	return forge.Capabilities{
		GitBackend: forge.BackendGitHub,
		Clone:      forge.CloneDescriptor{Available: true, URL: "https://github.com/" + repository.Integration.ExternalRepository + ".git", Auth: forge.CloneAuthProvider, Provider: forge.BackendGitHub},
		Browse:     true, Compare: true, RefEvents: true,
		Issues: true, PullRequests: true, Reviews: true, Merge: true,
		Limits: forge.Limits{MaximumBlobBytes: 100 << 20, MaximumChangedFiles: 300, MaximumTreeEntries: 1000, MayTruncateCompare: true, MayTruncateTree: true},
	}, nil
}
func (provider *fakeGitHubProvider) Execute(_ context.Context, command forge.CollaborationCommand) (forge.CollaborationResult, error) {
	provider.commands = append(provider.commands, command)
	return provider.result, nil
}
func (*fakeGitHubProvider) CloneDescriptor(context.Context, forge.Repository) (forge.CloneDescriptor, error) {
	return forge.CloneDescriptor{Available: true, Auth: forge.CloneAuthProvider}, nil
}
func (*fakeGitHubProvider) DefaultBranch(context.Context, forge.Repository, string) (string, error) {
	return "main", nil
}
func (*fakeGitHubProvider) Refs(context.Context, forge.Repository, forge.RefsRequest) (forge.RefPage, error) {
	return forge.RefPage{}, nil
}
func (provider *fakeGitHubProvider) Resolve(_ context.Context, _ forge.Repository, request forge.RevisionRequest) (forge.Revision, error) {
	provider.resolveRequests = append(provider.resolveRequests, request.Revision)
	return forge.Revision{Ref: request.Revision, SHA: request.Revision, Kind: "commit"}, nil
}
func (*fakeGitHubProvider) Tree(context.Context, forge.Repository, forge.RevisionRequest) (forge.Tree, error) {
	return forge.Tree{}, nil
}
func (*fakeGitHubProvider) Blob(context.Context, forge.Repository, forge.RevisionRequest) (forge.Blob, error) {
	return forge.Blob{}, nil
}
func (*fakeGitHubProvider) Commits(context.Context, forge.Repository, forge.CommitsRequest) (forge.CommitPage, error) {
	return forge.CommitPage{}, nil
}
func (*fakeGitHubProvider) Commit(context.Context, forge.Repository, forge.RevisionRequest) (forge.CommitDetail, error) {
	return forge.CommitDetail{}, nil
}
func (*fakeGitHubProvider) Compare(context.Context, forge.Repository, forge.CompareRequest) (forge.Diff, error) {
	return forge.Diff{}, nil
}
func (provider *fakeGitHubProvider) SnapshotRefs(context.Context, forge.Repository) (map[string]string, error) {
	if len(provider.refSnapshots) == 0 {
		return map[string]string{}, nil
	}
	index := provider.refSnapshotCalls
	if index >= len(provider.refSnapshots) {
		index = len(provider.refSnapshots) - 1
	}
	provider.refSnapshotCalls++
	return provider.refSnapshots[index], nil
}
func (provider *fakeGitHubProvider) CloneSource(context.Context, forge.Repository) (string, string, string, error) {
	return provider.cloneURL, provider.cloneUsername, provider.clonePassword, nil
}
func (provider *fakeGitHubProvider) Project(_ context.Context, projection forge.MirrorProjection) (forge.CollaborationResult, error) {
	provider.commands = append(provider.commands, forge.CollaborationCommand{
		OperationID: projection.OperationID, Kind: projection.Kind,
		Actor: projection.Event.PubKey, Repository: projection.Repository,
		RootEventID: projection.RootEventID, ExpectedHead: projection.ExpectedHead,
		CorrelationMarker: projection.CorrelationMarker, Payload: projection.Payload,
	})
	return provider.result, nil
}
func (provider *fakeGitHubProvider) Start(_ context.Context, request forge.CIRequest) (forge.CIRun, error) {
	provider.ciStarts = append(provider.ciStarts, request)
	return provider.ciRun, nil
}
func (provider *fakeGitHubProvider) Status(context.Context, forge.Repository, string) (forge.CIRun, error) {
	return provider.ciStatus, nil
}
func (provider *fakeGitHubProvider) Discover(_ context.Context, request forge.CIRequest) (forge.CIRun, error) {
	provider.ciStarts = append(provider.ciStarts, request)
	return provider.ciStatus, nil
}
func (provider *fakeGitHubProvider) Cancel(_ context.Context, _ forge.Repository, runID string) error {
	provider.ciCancelled = append(provider.ciCancelled, runID)
	return nil
}
func (provider *fakeGitHubProvider) Cleanup(_ context.Context, _ forge.Repository, ref string) error {
	provider.ciCleaned = append(provider.ciCleaned, ref)
	return nil
}
func (*fakeGitHubProvider) MirrorTarget(context.Context, forge.Repository) (forge.CIMirrorTarget, error) {
	return forge.CIMirrorTarget{CloneURL: "https://github.com/lapis/ci-mirror.git", Username: "x-access-token", Password: "opaque-ci-token"}, nil
}
func (*fakeGitHubProvider) GitReplicationTarget(context.Context, forge.Repository) (forge.GitReplicationTarget, error) {
	return forge.GitReplicationTarget{CloneURL: "https://github.com/lapis/notes.git", Username: "x-access-token", Password: "opaque-replication-token"}, nil
}

type fakeCredentialStore map[string][]byte

func (credentials fakeCredentialStore) Load(reference string) ([]byte, error) {
	return credentials[reference], nil
}

func signedForgeRequest(t *testing.T, method, target string, body []byte, secret nostr.SecretKey) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	tags := nostr.Tags{{"u", target}, {"method", method}}
	if method != http.MethodGet && method != http.MethodHead {
		digest := sha256.Sum256(body)
		tags = append(tags, nostr.Tag{"payload", hex.EncodeToString(digest[:])})
	}
	event := nostr.Event{Kind: nostr.KindHTTPAuth, CreatedAt: nostr.Now(), Tags: tags}
	require.NoError(t, event.Sign(secret))
	raw, err := event.MarshalJSON()
	require.NoError(t, err)
	request.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(raw))
	return request
}

func TestForgeGitHubConnectionAttachCapabilitiesAndHealth(t *testing.T) {
	policy, store := createGitForgeTestPolicy(t, &recordingGitBackend{})
	admin := store.Config.secret
	bridge := nostr.Generate()
	provider := &fakeGitHubProvider{}
	provider.repository.ID = 42
	provider.repository.FullName = "lapis/notes"
	provider.repository.CloneURL = "https://github.com/lapis/notes.git"
	provider.repository.HTMLURL = "https://github.com/lapis/notes"
	provider.repository.DefaultBranch = "main"
	policy.GitHub = provider
	policy.Credentials = fakeCredentialStore{
		"env:APP_KEY": []byte("app-key"), "env:WEBHOOK": []byte("webhook-secret"),
		"env:BRIDGE": []byte(bridge.Hex()),
	}
	require.NoError(t, policy.Registry.RegisterGit(provider))
	require.NoError(t, policy.Registry.RegisterCI(provider))

	connectionBody, err := json.Marshal(map[string]any{
		"app_id": "123", "installation_id": 99, "credential_ref": "env:APP_KEY",
		"webhook_secret_ref": "env:WEBHOOK", "bridge_credential_ref": "env:BRIDGE",
		"bridge_pubkey": bridge.Public().Hex(),
	})
	require.NoError(t, err)
	connectionURL := "https://relay.example/api/forge/connections/github"
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodPost, connectionURL, connectionBody, admin))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	var connection map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &connection))
	connectionID, ok := connection["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, connectionID)
	require.NotContains(t, recorder.Body.String(), "APP_KEY")
	require.NotContains(t, recorder.Body.String(), bridge.Hex())

	owner := nostr.Generate()
	announcement := gitForgeRepositoryEvent(t, owner, "notes",
		nostr.Tag{"clone", "https://github.com/lapis/notes.git"},
		nostr.Tag{"web", "https://github.com/lapis/notes"})
	require.NoError(t, store.SaveEvent(announcement))
	require.NoError(t, policy.Remember(context.Background(), announcement))
	grantGitGroup(policy, "channel", admin.Public())

	attachmentBody, err := json.Marshal(map[string]any{
		"expected_announcement_id": announcement.ID.Hex(), "git_backend": "github",
		"collaboration_backend": "relay", "connection_id": connectionID,
		"external_repository_id": "42", "ci_backend": "github",
		"ci_connection_id": connectionID, "ci_external_repository_id": "42", "ci_workflow": "ci.yml",
	})
	require.NoError(t, err)
	attachmentURL := "https://relay.example/api/forge/repositories/" + announcement.PubKey.Hex() + "/notes/integration"
	recorder = httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodPut, attachmentURL, attachmentBody, admin))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"auth":"provider"`)
	require.Contains(t, recorder.Body.String(), `"smart_http":{"read":false,"write":false}`)
	require.Contains(t, recorder.Body.String(), `"ci":true`)

	capabilitiesURL := "https://relay.example/api/forge/repositories/" + announcement.PubKey.Hex() + "/notes/capabilities"
	recorder = httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodGet, capabilitiesURL, nil, admin))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"git_backend":"github"`)

	healthURL := "https://relay.example/api/forge/repositories/" + announcement.PubKey.Hex() + "/notes/health"
	recorder = httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodGet, healthURL, nil, admin))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"state":"ready"`)
}

func TestForgeConnectionRegistrationRequiresRelayAdmin(t *testing.T) {
	policy, _ := createGitForgeTestPolicy(t, &recordingGitBackend{})
	policy.GitHub = &fakeGitHubProvider{}
	policy.Credentials = fakeCredentialStore{}
	body := []byte(`{"app_id":"1","installation_id":1}`)
	target := "https://relay.example/api/forge/connections/github"
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodPost, target, body, nostr.Generate()))
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestGitHubAnnouncementMatchRequiresCloneAndWeb(t *testing.T) {
	event := nostr.Event{Tags: nostr.Tags{{"clone", "https://github.com/Lapis/Notes.git"}, {"web", "https://github.com/lapis/notes/"}}}
	metadata := githubforge.RepositoryMetadata{CloneURL: "https://github.com/lapis/notes.git", HTMLURL: "https://github.com/lapis/notes"}
	require.True(t, githubAnnouncementMatches(event, metadata))
	event.Tags = event.Tags[:1]
	require.False(t, githubAnnouncementMatches(event, metadata))
	require.Empty(t, normalizeGitHubURL("https://github.example/lapis/notes"))
	require.Empty(t, normalizeGitHubURL("https://github.com/lapis/notes/extra"))
}
