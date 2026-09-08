//go:build github_integration

package zooid

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"zooid/internal/forge"
	"zooid/internal/githubforge"
	"zooid/internal/projecttask"
)

const fakeGitHubImage = "lapis-fake-github:forge-integration"

type fakeGitHubTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (transport fakeGitHubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = transport.target.Scheme
	clone.URL.Host = transport.target.Host
	clone.Host = transport.target.Host
	return transport.inner.RoundTrip(clone)
}

func TestGitHubForgeDockerLane(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	buildFakeGitHubImage(t)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: fakeGitHubImage, ExposedPorts: []string{"8080/tcp"},
			WaitingFor: wait.ForHTTP("/health").WithPort("8080/tcp").WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	endpoint, err := container.PortEndpoint(ctx, "8080/tcp", "http")
	require.NoError(t, err)
	fakeURL, err := url.Parse(endpoint)
	require.NoError(t, err)

	policy, store := createGitForgeTestPolicy(t, nil)
	store.Config.GitHub.Enabled = true
	store.Config.GitHub.MaxWebhookBytes = 1 << 20
	bridge := nostr.Generate()
	credentials := fakeCredentialStore{
		"env:FAKE_GITHUB_APP_KEY": fakeGitHubPrivateKey(t),
		"env:FAKE_GITHUB_WEBHOOK": []byte("fake-webhook-secret"),
		"env:FAKE_GITHUB_BRIDGE":  []byte(bridge.Hex()),
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: fakeGitHubTransport{target: fakeURL, inner: http.DefaultTransport}}
	backend, err := githubforge.New(policy, credentials, client)
	require.NoError(t, err)
	require.NoError(t, policy.Registry.RegisterGit(backend))
	require.NoError(t, policy.Registry.RegisterCollaboration(backend))
	require.NoError(t, policy.Registry.RegisterMirror(backend))
	require.NoError(t, policy.Registry.RegisterCI(backend))
	policy.GitHub = backend
	policy.Credentials = credentials
	policy.EventSource = backend

	admin := store.Config.secret
	connectionBody, err := json.Marshal(map[string]any{
		"app_id": "123", "installation_id": 99,
		"credential_ref":        "env:FAKE_GITHUB_APP_KEY",
		"webhook_secret_ref":    "env:FAKE_GITHUB_WEBHOOK",
		"bridge_credential_ref": "env:FAKE_GITHUB_BRIDGE",
		"bridge_pubkey":         bridge.Public().Hex(),
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

	owner := nostr.Generate()
	repository := gitForgeRepositoryEvent(t, owner, "notes",
		nostr.Tag{"clone", "https://github.com/lapis/notes.git"},
		nostr.Tag{"web", "https://github.com/lapis/notes"})
	require.NoError(t, store.SaveEvent(repository))
	require.NoError(t, policy.Remember(ctx, repository))
	grantGitGroup(policy, "channel", admin.Public(), "private")
	attachmentBody, err := json.Marshal(map[string]any{
		"expected_announcement_id": repository.ID.Hex(),
		"git_backend":              "github", "collaboration_backend": "github",
		"connection_id": connectionID, "external_repository_id": "42",
		"ci_backend": "github", "ci_connection_id": connectionID,
		"ci_external_repository_id": "42", "ci_workflow": "ci.yml",
	})
	require.NoError(t, err)
	repositoryBase := "https://relay.example/api/forge/repositories/" + repository.PubKey.Hex() + "/notes"
	recorder = httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodPut, repositoryBase+"/integration", attachmentBody, admin))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"clone":{"available":true,"url":"https://github.com/lapis/notes.git","auth":"provider"`)
	require.Contains(t, recorder.Body.String(), `"ci":true`)

	capabilities := httptest.NewRecorder()
	policy.ServeForgeAPI(capabilities, signedForgeRequest(t, http.MethodGet, repositoryBase+"/capabilities", nil, admin))
	require.Equal(t, http.StatusOK, capabilities.Code, capabilities.Body.String())
	require.Contains(t, capabilities.Body.String(), `"maximum_changed_files":300`)

	browseURL := "https://relay.example/api/git/" + repository.PubKey.Hex() + "/notes/refs"
	browseRequest := httptest.NewRequest(http.MethodGet, browseURL, nil)
	signedGitRequest(t, browseRequest, admin, http.MethodGet, browseURL)
	browse := httptest.NewRecorder()
	policy.ServeRepositoryAPI(browse, browseRequest)
	require.Equal(t, http.StatusOK, browse.Code, browse.Body.String())
	require.Contains(t, browse.Body.String(), `"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`)

	issueURL := repositoryBase + "/issues"
	issueRequest := signedForgeRequest(t, http.MethodPost, issueURL, []byte(`{"title":"Docker issue","body":"Created through the durable provider command"}`), admin)
	issueRequest.Header.Set("Idempotency-Key", "fake-github-issue")
	recorder = httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, issueRequest)
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	require.NoError(t, policy.ProcessForgeOutboxOnce(ctx))
	roots := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindIssue}, Tags: nostr.TagMap{"a": {"30617:" + repository.PubKey.Hex() + ":notes"}}})
	require.Len(t, roots, 1)
	require.Equal(t, bridge.Public(), roots[0].PubKey)
	require.Equal(t, "8001", roots[0].Tags.Find("external-id")[1])

	webhookBody, err := json.Marshal(map[string]any{
		"action": "opened", "installation": map[string]any{"id": 99},
		"repository": map[string]any{"id": 42},
		"issue": map[string]any{
			"id": 8001, "number": 12, "title": "Docker issue", "body": "Echo",
			"state": "open", "html_url": "https://github.com/lapis/notes/issues/12",
			"user": map[string]any{"id": 55, "login": "lapis-bot"},
		},
	})
	require.NoError(t, err)
	webhook := httptest.NewRecorder()
	policy.ServeGitHubWebhook(webhook, githubWebhookRequest(webhookBody, "docker-echo", "issues", "fake-webhook-secret"))
	require.Equal(t, http.StatusAccepted, webhook.Code, webhook.Body.String())
	require.NoError(t, policy.ProcessGitHubWebhookOnce(ctx))
	require.Len(t, collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindIssue}, Tags: nostr.TagMap{"a": {"30617:" + repository.PubKey.Hex() + ":notes"}}}), 1)

	ciURL := repositoryBase + "/ci"
	ciRequest := signedForgeRequest(t, http.MethodPost, ciURL, []byte(`{"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), admin)
	ciRequest.Header.Set("Idempotency-Key", "fake-github-ci")
	recorder = httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, ciRequest)
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	require.NoError(t, policy.ProcessForgeOutboxOnce(ctx))
	require.NoError(t, policy.ProcessForgeCIOnce(ctx))
	ciEvents := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindTextNote}, Tags: nostr.TagMap{
		"a": {"30617:" + repository.PubKey.Hex() + ":notes"}, "t": {"ci"}, "c": {strings.Repeat("a", 40)},
	}})
	require.Len(t, ciEvents, 1)
	require.Equal(t, "success", ciEvents[0].Tags.Find("status")[1])

	anchor := gitForgeRepositoryEvent(t, owner, "project-tasks", nostr.Tag{projecttask.TagProjectTaskAnchor, "true"})
	require.NoError(t, store.SaveEvent(anchor))
	require.NoError(t, policy.Remember(ctx, anchor))
	freshRegistry := forge.NewRegistry()
	require.NoError(t, freshRegistry.RegisterGit(backend))
	require.NoError(t, freshRegistry.RegisterCollaboration(backend))
	require.NoError(t, freshRegistry.RegisterMirror(backend))
	require.NoError(t, freshRegistry.RegisterCI(backend))
	restarted := NewGitForgePolicyWithRegistry(ctx, store.Config, store, policy.Groups, policy.ProjectTasks, freshRegistry)
	restarted.GitHub, restarted.Credentials, restarted.EventSource = backend, credentials, backend
	t.Cleanup(restarted.Close)
	resolved, found, err := restarted.ResolveIntegration(ctx, projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, forge.BackendGitHub, resolved.Repository.Integration.GitBackend)
	require.True(t, resolved.Capabilities.CI)
	anchorResolved, found, err := restarted.ResolveIntegration(ctx, projecttask.RepositoryCoordinate{Owner: anchor.PubKey, ID: "project-tasks"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, forge.BackendNone, anchorResolved.Repository.Integration.GitBackend)
	require.Equal(t, forge.BackendRelay, anchorResolved.Repository.Integration.CollaborationBackend)

	operationRequest := signedForgeRequest(t, http.MethodGet, repositoryBase+"/capabilities", nil, admin)
	operationRecorder := httptest.NewRecorder()
	restarted.ServeForgeAPI(operationRecorder, operationRequest)
	require.Equal(t, http.StatusOK, operationRecorder.Code, operationRecorder.Body.String())
	responseBody, err := io.ReadAll(operationRecorder.Result().Body)
	require.NoError(t, err)
	require.NotContains(t, string(responseBody), "fake-installation-token")
}

func buildFakeGitHubImage(t *testing.T) {
	t.Helper()
	command := exec.Command("docker", "build", "-t", fakeGitHubImage, ".")
	command.Dir = "../testdata/fakegithub"
	output, err := command.CombinedOutput()
	require.NoError(t, err, "build fake GitHub image:\n%s", output)
}

func fakeGitHubPrivateKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	encoded := x509.MarshalPKCS1PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: encoded})
}
