package zooid

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"zooid/internal/forge"
	"zooid/internal/githubforge"
	"zooid/internal/projecttask"
)

func configureWebhookTest(t *testing.T) (*GitForgePolicy, *EventStore, fakeCredentialStore, nostr.Event) {
	t.Helper()
	policy, store := createGitForgeTestPolicy(t, &recordingGitBackend{})
	store.Config.GitHub.Enabled = true
	store.Config.GitHub.MaxWebhookBytes = 1 << 20
	bridge := nostr.Generate()
	credentials := fakeCredentialStore{
		"env:APP": []byte("unused-in-webhook-test"), "env:WEBHOOK": []byte("webhook-secret"),
		"env:BRIDGE": []byte(bridge.Hex()),
	}
	connection := githubforge.Connection{
		ID: "connection", Host: "github.com", AppID: "123", InstallationID: 99,
		CredentialRef: "env:APP", WebhookSecretRef: "env:WEBHOOK",
		BridgeCredentialRef: "env:BRIDGE", BridgePubkey: bridge.Public().Hex(), Status: "active",
	}
	require.NoError(t, policy.saveGitHubConnection(context.Background(), connection))
	backend, err := githubforge.New(policy, credentials, nil)
	require.NoError(t, err)
	require.NoError(t, policy.Registry.RegisterGit(backend))
	require.NoError(t, policy.Registry.RegisterCollaboration(backend))
	policy.GitHub = backend
	policy.Credentials = credentials
	policy.EventSource = backend
	owner := nostr.Generate()
	repository := gitForgeRepositoryEvent(t, owner, "notes",
		nostr.Tag{"clone", "https://github.com/lapis/notes.git"},
		nostr.Tag{"web", "https://github.com/lapis/notes"})
	require.NoError(t, store.SaveEvent(repository))
	require.NoError(t, policy.Remember(context.Background(), repository))
	require.NoError(t, policy.setExplicitIntegration(context.Background(), forge.Integration{
		Coordinate:     projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"},
		AnnouncementID: repository.ID.Hex(), ConfigurationSource: forge.ConfigurationExplicit,
		GitBackend: forge.BackendGitHub, CollaborationBackend: forge.BackendGitHub,
		ConnectionID: "connection", ExternalRepositoryID: "42", ExternalRepository: "lapis/notes",
		HealthState: "ready",
	}, repository.ID.Hex()))
	return policy, store, credentials, repository
}

func githubWebhookRequest(body []byte, delivery, event, secret string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "https://relay.example/webhooks/github", bytes.NewReader(body))
	request.Header.Set("X-GitHub-Delivery", delivery)
	request.Header.Set("X-GitHub-Event", event)
	request.Header.Set("X-Hub-Signature-256", githubforge.WebhookSignature(secret, body))
	return request
}

func TestGitHubWebhookIsVerifiedDeduplicatedAndProcessedAsynchronously(t *testing.T) {
	policy, store, _, repository := configureWebhookTest(t)
	body, err := json.Marshal(map[string]any{
		"action": "opened", "installation": map[string]any{"id": 99},
		"repository": map[string]any{"id": 42, "full_name": "lapis/notes", "clone_url": "https://github.com/lapis/notes.git", "html_url": "https://github.com/lapis/notes"},
		"issue":      map[string]any{"id": 9001, "number": 17, "title": "Webhook issue", "body": "Imported", "state": "open", "html_url": "https://github.com/lapis/notes/issues/17", "user": map[string]any{"id": 71, "login": "octocat"}},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	policy.ServeGitHubWebhook(recorder, githubWebhookRequest(body, "delivery-1", "issues", "webhook-secret"))
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"queued":true`)
	require.Empty(t, collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindIssue}}), "the request handler must not project synchronously")

	duplicate := httptest.NewRecorder()
	policy.ServeGitHubWebhook(duplicate, githubWebhookRequest(body, "delivery-1", "issues", "webhook-secret"))
	require.Equal(t, http.StatusAccepted, duplicate.Code)
	require.Contains(t, duplicate.Body.String(), `"duplicate":true`)

	require.NoError(t, policy.ProcessGitHubWebhookOnce(context.Background()))
	roots := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindIssue}, Tags: nostr.TagMap{"a": {"30617:" + repository.PubKey.Hex() + ":notes"}}})
	require.Len(t, roots, 1)
	require.Equal(t, "Webhook issue", roots[0].Tags.Find("subject")[1])
	require.Equal(t, "octocat", roots[0].Tags.Find("external-author")[1])
	require.Equal(t, "9001", roots[0].Tags.Find("external-id")[1])

	var state string
	require.NoError(t, GetDb().QueryRow(policy.Events.Schema.Render(`
		SELECT processing_state FROM {{.Name}}__forge_webhook_deliveries
		WHERE provider = 'github' AND delivery_id = 'delivery-1'`)).Scan(&state))
	require.Equal(t, "complete", state)
}

func TestGitHubExternalReviewCannotBecomeNativeApproval(t *testing.T) {
	policy, store, _, repository := configureWebhookTest(t)
	head := strings.Repeat("a", 40)
	pullBody, err := json.Marshal(map[string]any{
		"action": "opened", "installation": map[string]any{"id": 99},
		"repository": map[string]any{"id": 42},
		"pull_request": map[string]any{
			"id": 9100, "number": 18, "title": "Webhook PR", "body": "Review it", "state": "open", "draft": false,
			"html_url": "https://github.com/lapis/notes/pull/18", "user": map[string]any{"id": 72, "login": "author"},
			"head": map[string]any{"sha": head, "ref": "feature"}, "base": map[string]any{"ref": "main"},
		},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	policy.ServeGitHubWebhook(recorder, githubWebhookRequest(pullBody, "delivery-pr", "pull_request", "webhook-secret"))
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.NoError(t, policy.ProcessGitHubWebhookOnce(context.Background()))
	roots := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{projecttask.KindPullRequest}, Tags: nostr.TagMap{"a": {"30617:" + repository.PubKey.Hex() + ":notes"}}})
	require.Len(t, roots, 1)

	reviewBody, err := json.Marshal(map[string]any{
		"action": "submitted", "installation": map[string]any{"id": 99}, "repository": map[string]any{"id": 42},
		"pull_request": map[string]any{"id": 9100},
		"review":       map[string]any{"id": 9200, "body": "Looks good", "state": "approved", "commit_id": head, "html_url": "https://github.com/lapis/notes/pull/18#review", "user": map[string]any{"id": 73, "login": "reviewer"}},
	})
	require.NoError(t, err)
	recorder = httptest.NewRecorder()
	policy.ServeGitHubWebhook(recorder, githubWebhookRequest(reviewBody, "delivery-review", "pull_request_review", "webhook-secret"))
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.NoError(t, policy.ProcessGitHubWebhookOnce(context.Background()))
	notes := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindTextNote}, Tags: nostr.TagMap{"e": {roots[0].ID.Hex()}, "t": {"external-review"}}})
	require.Len(t, notes, 1)
	require.Equal(t, "approved", notes[0].Tags.Find("external-decision")[1])
	require.NotContains(t, forgeTagValues(notes[0], "t"), "approval")
	require.NotContains(t, forgeTagValues(notes[0], "t"), "changes-requested")

	state, err := projecttask.ReducePullRequest(roots[0], repository, notes)
	require.NoError(t, err)
	require.Empty(t, state.Approvals, "stock Buzz reduction must not trust an external-review observation")
}

func TestGitHubWebhookRejectsInvalidSignatureBodyLimitAndDeliveryConflict(t *testing.T) {
	policy, _, _, _ := configureWebhookTest(t)
	body := []byte(`{"installation":{"id":99}}`)
	request := githubWebhookRequest(body, "bad-signature", "installation", "wrong")
	recorder := httptest.NewRecorder()
	policy.ServeGitHubWebhook(recorder, request)
	require.Equal(t, http.StatusUnauthorized, recorder.Code)

	policy.Config.GitHub.MaxWebhookBytes = 8
	recorder = httptest.NewRecorder()
	policy.ServeGitHubWebhook(recorder, githubWebhookRequest(body, "too-large", "installation", "webhook-secret"))
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	policy.Config.GitHub.MaxWebhookBytes = 1 << 20

	recorder = httptest.NewRecorder()
	policy.ServeGitHubWebhook(recorder, githubWebhookRequest(body, "conflict", "installation", "webhook-secret"))
	require.Equal(t, http.StatusAccepted, recorder.Code)
	changed := []byte(`{"action":"deleted","installation":{"id":99}}`)
	recorder = httptest.NewRecorder()
	policy.ServeGitHubWebhook(recorder, githubWebhookRequest(changed, "conflict", "installation", "webhook-secret"))
	require.Equal(t, http.StatusConflict, recorder.Code)
}
