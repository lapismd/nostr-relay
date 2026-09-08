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
	repository githubforge.RepositoryMetadata
	commands   []forge.CollaborationCommand
	result     forge.CollaborationResult
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
func (*fakeGitHubProvider) Capabilities(_ context.Context, repository forge.Repository) (forge.Capabilities, error) {
	return forge.Capabilities{
		GitBackend: forge.BackendGitHub,
		Clone:      forge.CloneDescriptor{Available: true, URL: "https://github.com/" + repository.Integration.ExternalRepository + ".git", Auth: forge.CloneAuthProvider, Provider: forge.BackendGitHub},
		Browse: true, Compare: true, RefEvents: true,
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
func (*fakeGitHubProvider) Refs(context.Context, forge.Repository, forge.RefsRequest) (forge.Response, error) {
	return forge.Response{}, nil
}
func (*fakeGitHubProvider) Resolve(context.Context, forge.Repository, forge.RevisionRequest) (string, error) {
	return "", nil
}
func (*fakeGitHubProvider) Tree(context.Context, forge.Repository, forge.RevisionRequest) (forge.Response, error) {
	return forge.Response{}, nil
}
func (*fakeGitHubProvider) Blob(context.Context, forge.Repository, forge.RevisionRequest) (forge.Response, error) {
	return forge.Response{}, nil
}
func (*fakeGitHubProvider) Commits(context.Context, forge.Repository, forge.CommitsRequest) (forge.Response, error) {
	return forge.Response{}, nil
}
func (*fakeGitHubProvider) Commit(context.Context, forge.Repository, forge.RevisionRequest) (forge.Response, error) {
	return forge.Response{}, nil
}
func (*fakeGitHubProvider) Compare(context.Context, forge.Repository, forge.CompareRequest) (forge.CompareResult, error) {
	return forge.CompareResult{}, nil
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
		"external_repository_id": "42",
	})
	require.NoError(t, err)
	attachmentURL := "https://relay.example/api/forge/repositories/" + announcement.PubKey.Hex() + "/notes/integration"
	recorder = httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodPut, attachmentURL, attachmentBody, admin))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"auth":"provider"`)
	require.Contains(t, recorder.Body.String(), `"smart_http":{"read":false,"write":false}`)

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
