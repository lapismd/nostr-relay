package zooid

import (
	"bytes"
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

func TestGitHubPullRequestReviewRequestAndMergeRemainVisible(t *testing.T) {
	policy, store, _, repository := configureWebhookTest(t)
	base := strings.Repeat("9", 40)
	head := strings.Repeat("a", 40)
	mergeCommit := strings.Repeat("b", 40)
	process := func(action string, payload map[string]any) {
		t.Helper()
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		require.NoError(t, policy.processGitHubWebhook(context.Background(), githubWebhookDelivery{
			Provider: forge.BackendGitHub, ConnectionID: "connection", ExternalRepositoryID: "42",
			EventType: "pull_request", Action: action, ExternalObjectID: "9100",
			Payload: body, ReceivedAt: time.Now().UTC(),
		}))
	}
	pull := map[string]any{
		"id": 9100, "number": 18, "title": "Webhook PR", "body": "Review it", "state": "open",
		"html_url": "https://github.com/lapis/notes/pull/18", "user": map[string]any{"id": 72, "login": "author"},
		"head": map[string]any{"sha": head, "ref": "feature"}, "base": map[string]any{"sha": base, "ref": "main"},
	}
	process("opened", map[string]any{"repository": map[string]any{"id": 42}, "pull_request": pull})
	process("review_requested", map[string]any{
		"repository": map[string]any{"id": 42}, "pull_request": pull,
		"sender":             map[string]any{"id": 73, "login": "maintainer"},
		"requested_reviewer": map[string]any{"id": 74, "login": "reviewer"},
	})
	pull["state"] = "closed"
	pull["merged"] = true
	pull["merge_commit_sha"] = mergeCommit
	process("closed", map[string]any{"repository": map[string]any{"id": 42}, "pull_request": pull})

	roots := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{projecttask.KindPullRequest}})
	require.Len(t, roots, 1)
	require.Equal(t, base, roots[0].Tags.Find("base-commit")[1])
	requests := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindTextNote}, Tags: nostr.TagMap{"e": {roots[0].ID.Hex()}, "t": {"review-request"}}})
	require.Len(t, requests, 1)
	require.Equal(t, "maintainer", requests[0].Tags.Find("external-author")[1])
	require.Equal(t, "reviewer", requests[0].Tags.Find("external-reviewer")[1])
	merged := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindStatusApplied}, Tags: nostr.TagMap{"e": {roots[0].ID.Hex()}}})
	require.Len(t, merged, 1)
	require.Equal(t, mergeCommit, merged[0].Tags.Find("merge-commit")[1])

	state, err := projecttask.ReducePullRequest(roots[0], repository, append(requests, merged...))
	require.NoError(t, err)
	require.Equal(t, base, state.BaseCommit)
	require.Equal(t, projecttask.PullRequestMerged, state.Lifecycle)
}

func TestCorrelatedGitHubReviewAcknowledgesSignedRelayReviewWithoutEcho(t *testing.T) {
	policy, store, _, repository := configureWebhookTest(t)
	head := strings.Repeat("a", 40)
	process := func(delivery githubWebhookDelivery) {
		t.Helper()
		delivery.Provider = forge.BackendGitHub
		delivery.ConnectionID = "connection"
		delivery.ExternalRepositoryID = "42"
		delivery.ReceivedAt = time.Now().UTC()
		require.NoError(t, policy.processGitHubWebhook(context.Background(), delivery))
	}
	pullPayload, err := json.Marshal(map[string]any{
		"repository": map[string]any{"id": 42},
		"pull_request": map[string]any{
			"id": 9100, "number": 18, "title": "Webhook PR", "body": "Review it", "state": "open",
			"html_url": "https://github.com/lapis/notes/pull/18", "user": map[string]any{"id": 72, "login": "author"},
			"head": map[string]any{"sha": head, "ref": "feature"}, "base": map[string]any{"ref": "main"},
		},
	})
	require.NoError(t, err)
	process(githubWebhookDelivery{EventType: "pull_request", Action: "opened", ExternalObjectID: "9100", Payload: pullPayload})
	roots := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{projecttask.KindPullRequest}, Tags: nostr.TagMap{"a": {"30617:" + repository.PubKey.Hex() + ":notes"}}})
	require.Len(t, roots, 1)

	review := nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Now(), Content: "Looks good", Tags: nostr.Tags{
		{"e", roots[0].ID.Hex(), "", "root"}, {"a", "30617:" + repository.PubKey.Hex() + ":notes"},
		{"c", head}, {"t", "approval"},
	}}
	require.NoError(t, review.Sign(store.Config.secret))
	require.NoError(t, store.SaveEvent(review))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), review))
	require.NoError(t, policy.Remember(context.Background(), review))
	idempotency := "nostr:" + review.ID.Hex() + ":github:connection"
	operation, err := policy.loadForgeOperationByIdempotency(context.Background(), idempotency)
	require.NoError(t, err)
	require.Equal(t, "review", operation.Command)
	marker := forgeCorrelationMarker(idempotency)
	reviewPayload, err := json.Marshal(map[string]any{
		"repository": map[string]any{"id": 42}, "pull_request": map[string]any{"id": 9100},
		"review": map[string]any{
			"id": 9200, "body": "Looks good\n\n<!-- lapis:" + marker + " -->", "state": "approved",
			"commit_id": head, "html_url": "https://github.com/lapis/notes/pull/18#review", "user": map[string]any{"id": 73, "login": "lapis-bot"},
		},
	})
	require.NoError(t, err)
	process(githubWebhookDelivery{EventType: "pull_request_review", Action: "submitted", ExternalObjectID: "9200", Payload: reviewPayload})
	operation, err = policy.loadForgeOperationByIdempotency(context.Background(), idempotency)
	require.NoError(t, err)
	require.NotNil(t, operation.ProviderResult)
	require.Equal(t, "9200", operation.ProviderResult.ExternalID)
	external := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindTextNote}, Tags: nostr.TagMap{"e": {roots[0].ID.Hex()}, "t": {"external-review"}}})
	require.Empty(t, external)
}

func TestGitHubCommentReactionSnapshotsProjectAddsAndRemovals(t *testing.T) {
	policy, store, _, repository := configureWebhookTest(t)
	head := strings.Repeat("a", 40)
	process := func(delivery githubWebhookDelivery) {
		t.Helper()
		delivery.Provider = forge.BackendGitHub
		delivery.ConnectionID = "connection"
		delivery.ExternalRepositoryID = "42"
		delivery.ReceivedAt = time.Now().UTC()
		require.NoError(t, policy.processGitHubWebhook(context.Background(), delivery))
	}

	pullPayload, err := json.Marshal(map[string]any{
		"repository": map[string]any{"id": 42},
		"pull_request": map[string]any{
			"id": 9100, "number": 18, "title": "Webhook PR", "body": "Review it", "state": "open",
			"html_url": "https://github.com/lapis/notes/pull/18", "user": map[string]any{"id": 72, "login": "author"},
			"head": map[string]any{"sha": head, "ref": "feature"}, "base": map[string]any{"ref": "main"},
		},
	})
	require.NoError(t, err)
	process(githubWebhookDelivery{EventType: "pull_request", Action: "opened", ExternalObjectID: "9100", Payload: pullPayload})

	commentPayload, err := json.Marshal(map[string]any{
		"repository": map[string]any{"id": 42}, "pull_request": map[string]any{"id": 9100},
		"comment": map[string]any{
			"id": 9300, "body": "Please change this", "commit_id": head, "path": "README.md", "line": 2, "side": "RIGHT",
			"html_url": "https://github.com/lapis/notes/pull/18#discussion_r9300", "user": map[string]any{"id": 73, "login": "reviewer"},
		},
	})
	require.NoError(t, err)
	process(githubWebhookDelivery{EventType: "pull_request_review_comment", Action: "created", ExternalObjectID: "9300", Payload: commentPayload})

	reactionPayload, err := json.Marshal(map[string]any{
		"repository": map[string]any{"id": 42}, "comment": map[string]any{"id": 9300},
		"reactions": []map[string]any{{
			"id": 9400, "content": "heart", "created_at": "2026-09-09T10:00:00Z",
			"user": map[string]any{"id": 74, "login": "reactor"},
		}},
	})
	require.NoError(t, err)
	process(githubWebhookDelivery{EventType: "pull_request_review_comment_reactions", Action: "synchronize", ExternalObjectID: "9300", Payload: reactionPayload})

	reactions := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindReaction}})
	require.Len(t, reactions, 1)
	require.Equal(t, "❤️", reactions[0].Content)
	require.Equal(t, "9400", reactions[0].Tags.Find("external-id")[1])
	require.Equal(t, "reactor", reactions[0].Tags.Find("external-author")[1])

	emptyPayload, err := json.Marshal(map[string]any{
		"repository": map[string]any{"id": 42}, "comment": map[string]any{"id": 9300},
		"reactions": []any{},
	})
	require.NoError(t, err)
	process(githubWebhookDelivery{EventType: "pull_request_review_comment_reactions", Action: "synchronize", ExternalObjectID: "9300", Payload: emptyPayload})
	deletions := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindDeletion}, Tags: nostr.TagMap{"e": {reactions[0].ID.Hex()}}})
	require.Len(t, deletions, 1)

	conversationPayload, err := json.Marshal(map[string]any{
		"repository": map[string]any{"id": 42}, "issue": map[string]any{"id": 9100},
		"comment": map[string]any{
			"id": 9301, "body": "Conversation note",
			"html_url": "https://github.com/lapis/notes/pull/18#issuecomment-9301", "user": map[string]any{"id": 75, "login": "commenter"},
		},
	})
	require.NoError(t, err)
	process(githubWebhookDelivery{EventType: "issue_comment", Action: "created", ExternalObjectID: "9301", Payload: conversationPayload})
	conversationReactionPayload, err := json.Marshal(map[string]any{
		"repository": map[string]any{"id": 42}, "comment": map[string]any{"id": 9301},
		"reactions": []map[string]any{{
			"id": 9401, "content": "eyes", "created_at": "2026-09-09T10:02:00Z",
			"user": map[string]any{"id": 76, "login": "watcher"},
		}},
	})
	require.NoError(t, err)
	process(githubWebhookDelivery{EventType: "issue_comment_reactions", Action: "synchronize", ExternalObjectID: "9301", Payload: conversationReactionPayload})
	allReactions := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindReaction}})
	require.Len(t, allReactions, 2)
	var conversationReaction *nostr.Event
	for index := range allReactions {
		if tag := allReactions[index].Tags.Find("external-id"); len(tag) > 1 && tag[1] == "9401" {
			conversationReaction = &allReactions[index]
			break
		}
	}
	require.NotNil(t, conversationReaction)
	require.Equal(t, "👀", conversationReaction.Content)

	bridge, err := policy.bridgeSecretForConnection(context.Background(), "connection")
	require.NoError(t, err)
	require.Equal(t, bridge.Public(), deletions[0].PubKey)
	require.Equal(t, "30617:"+repository.PubKey.Hex()+":notes", reactions[0].Tags.Find("a")[1])
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

func TestMirrorWebhookDoesNotImportRootsAndKeepsMappedLifecycleObservational(t *testing.T) {
	policy, store, _, repository := configureWebhookTest(t)
	backend := policy.GitHub.(*githubforge.Backend)
	require.NoError(t, policy.Registry.RegisterMirror(backend))
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	integration, found, err := policy.repositoryIntegration(context.Background(), coordinate)
	require.NoError(t, err)
	require.True(t, found)
	integration.CollaborationBackend = forge.BackendRelay
	integration.MirrorBackend = forge.BackendGitHub
	require.NoError(t, policy.setExplicitIntegration(context.Background(), integration, repository.ID.Hex()))

	body, err := json.Marshal(map[string]any{
		"action": "opened", "installation": map[string]any{"id": 99},
		"repository": map[string]any{"id": 42},
		"issue":      map[string]any{"id": 9001, "number": 17, "title": "Must not import", "state": "open", "user": map[string]any{"id": 71, "login": "octocat"}},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	policy.ServeGitHubWebhook(recorder, githubWebhookRequest(body, "mirror-unmapped", "issues", "webhook-secret"))
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.NoError(t, policy.ProcessGitHubWebhookOnce(context.Background()))
	require.Empty(t, collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindIssue}, Tags: nostr.TagMap{"a": {coordinate.String()}}}))

	root := nostr.Event{Kind: nostr.KindIssue, CreatedAt: nostr.Now(), Content: "Relay root", Tags: nostr.Tags{
		{"a", coordinate.String()}, {"p", repository.PubKey.Hex()}, {"subject", "Relay root"},
	}}
	require.NoError(t, root.Sign(nostr.Generate()))
	require.NoError(t, store.SaveEvent(root))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), root))
	require.NoError(t, policy.saveForgeMapping(context.Background(), forgeObjectMapping{
		Provider: forge.BackendGitHub, ConnectionID: "connection", Coordinate: coordinate,
		ObjectType: "issue", NostrEventID: root.ID.Hex(), NostrRootID: root.ID.Hex(),
		ExternalID: "9001", ExternalNumber: 17, Origin: "relay", Authority: "relay",
	}))
	closedBody, err := json.Marshal(map[string]any{
		"action": "closed", "installation": map[string]any{"id": 99},
		"repository": map[string]any{"id": 42},
		"issue":      map[string]any{"id": 9001, "number": 17, "title": "Relay root", "state": "closed", "user": map[string]any{"id": 71, "login": "octocat"}},
	})
	require.NoError(t, err)
	recorder = httptest.NewRecorder()
	policy.ServeGitHubWebhook(recorder, githubWebhookRequest(closedBody, "mirror-mapped", "issues", "webhook-secret"))
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.NoError(t, policy.ProcessGitHubWebhookOnce(context.Background()))
	notes := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindTextNote}, Tags: nostr.TagMap{"e": {root.ID.Hex()}, "t": {"external-state"}}})
	require.Len(t, notes, 1)
	statuses := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindStatusClosed}, Tags: nostr.TagMap{"e": {root.ID.Hex()}}})
	require.Empty(t, statuses)
}

func TestWorkflowRunWebhookCompletesExactCommitAndPublishesCIActivity(t *testing.T) {
	policy, store, _, repository := configureWebhookTest(t)
	ci := &fakeGitHubProvider{}
	require.NoError(t, policy.Registry.RegisterCI(ci))
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	integration, found, err := policy.repositoryIntegration(context.Background(), coordinate)
	require.NoError(t, err)
	require.True(t, found)
	integration.CIBackend = forge.BackendGitHub
	integration.CIConnectionID = "connection"
	integration.CIExternalRepositoryID = "84"
	integration.CIExternalRepository = "lapis/ci-mirror"
	integration.CIWorkflow = "ci.yml"
	require.NoError(t, policy.setExplicitIntegration(context.Background(), integration, repository.ID.Hex()))
	commit := strings.Repeat("d", 40)
	updatedAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, policy.saveForgeCIRun(context.Background(), forgeCIRun{
		OperationID: "ci_webhook", Provider: forge.BackendGitHub, ConnectionID: "connection",
		Coordinate: coordinate, Commit: commit, Workflow: "ci.yml",
		MirrorRef: "refs/heads/lapis-ci/ci_webhook", Status: "requested", CleanupState: "pending",
	}))
	body, err := json.Marshal(map[string]any{
		"action": "completed", "installation": map[string]any{"id": 99},
		"repository": map[string]any{"id": 84},
		"workflow_run": map[string]any{
			"id": 91001, "head_sha": commit, "head_branch": "lapis-ci/ci_webhook",
			"status": "completed", "conclusion": "failure",
			"html_url": "https://github.com/lapis/ci-mirror/actions/runs/91001", "updated_at": updatedAt,
		},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	policy.ServeGitHubWebhook(recorder, githubWebhookRequest(body, "ci-complete", "workflow_run", "webhook-secret"))
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	require.NoError(t, policy.ProcessGitHubWebhookOnce(context.Background()))
	require.Equal(t, []string{"refs/heads/lapis-ci/ci_webhook"}, ci.ciCleaned)
	run, found, err := policy.loadForgeCIRun(context.Background(), "ci_webhook")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "failure", run.Status)
	require.Equal(t, "91001", run.ProviderRunID)
	events := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindTextNote}, Tags: nostr.TagMap{"t": {"ci"}, "c": {commit}}})
	require.Len(t, events, 1)
	require.Equal(t, "failure", events[0].Tags.Find("status")[1])
	require.Equal(t, nostr.Timestamp(updatedAt.Unix()), events[0].CreatedAt)
}
