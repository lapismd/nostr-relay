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
	"zooid/internal/githubforge"
	"zooid/internal/projecttask"
)

func configureForgeCollaborationTest(t *testing.T) (*GitForgePolicy, *EventStore, *fakeGitHubProvider, nostr.SecretKey, nostr.Event, string) {
	t.Helper()
	policy, store := createGitForgeTestPolicy(t, &recordingGitBackend{})
	store.Config.GitHub.Enabled = true
	bridge := nostr.Generate()
	provider := &fakeGitHubProvider{result: forge.CollaborationResult{
		ExternalID: "8001", ExternalNumber: 12, ExternalURL: "https://github.com/lapis/notes/issues/12",
		ExternalAuthor: "lapis-bot", ExternalAuthorID: "55", State: "open",
	}}
	provider.repository.ID = 42
	provider.repository.FullName = "lapis/notes"
	provider.repository.CloneURL = "https://github.com/lapis/notes.git"
	provider.repository.HTMLURL = "https://github.com/lapis/notes"
	require.NoError(t, policy.Registry.RegisterGit(provider))
	require.NoError(t, policy.Registry.RegisterCollaboration(provider))
	policy.GitHub = provider
	policy.Credentials = fakeCredentialStore{"env:BRIDGE": []byte(bridge.Hex())}
	connection := githubforge.Connection{
		ID: "connection", Host: "github.com", AppID: "123", InstallationID: 99,
		CredentialRef: "env:APP", WebhookSecretRef: "env:WEBHOOK",
		BridgeCredentialRef: "env:BRIDGE", BridgePubkey: bridge.Public().Hex(), Status: "active",
	}
	require.NoError(t, policy.saveGitHubConnection(context.Background(), connection))
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
	actor := store.Config.secret
	grantGitGroup(policy, "channel", actor.Public())
	return policy, store, provider, actor, repository, "connection"
}

func TestProviderCommandIsDurableIdempotentAndProjectsBridgeRoot(t *testing.T) {
	policy, store, provider, actor, repository, _ := configureForgeCollaborationTest(t)
	policy.UserCredentialMode = func(_ context.Context, pubkey nostr.PubKey) (string, error) {
		require.Equal(t, actor.Public(), pubkey)
		return forge.CredentialUser, nil
	}
	target := "https://relay.example/api/forge/repositories/" + repository.PubKey.Hex() + "/notes/issues"
	body := []byte(`{"title":"Provider issue","body":"Track this"}`)
	request := signedForgeRequest(t, http.MethodPost, target, body, actor)
	request.Header.Set("Idempotency-Key", "issue-12")
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, request)
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	var accepted map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &accepted))
	operationID := accepted["operation_id"].(string)

	retryRequest := signedForgeRequest(t, http.MethodPost, target, body, actor)
	retryRequest.Header.Set("Idempotency-Key", "issue-12")
	retryRecorder := httptest.NewRecorder()
	policy.ServeForgeAPI(retryRecorder, retryRequest)
	require.Equal(t, http.StatusAccepted, retryRecorder.Code)
	var retried map[string]any
	require.NoError(t, json.Unmarshal(retryRecorder.Body.Bytes(), &retried))
	require.Equal(t, operationID, retried["operation_id"])

	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Len(t, provider.commands, 1)
	require.Equal(t, forge.CredentialUser, provider.commands[0].CredentialMode)
	require.Contains(t, string(provider.commands[0].Payload), "Provider issue")

	var root nostr.Event
	for event := range store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{nostr.KindIssue}, Tags: nostr.TagMap{"a": {"30617:" + repository.PubKey.Hex() + ":notes"}}}, 10) {
		root = event
		break
	}
	require.NotEqual(t, nostr.ZeroID, root.ID)
	require.Equal(t, "Provider issue", root.Tags.Find("subject")[1])
	require.Equal(t, "github", root.Tags.Find("external-provider")[1])
	require.Equal(t, "8001", root.Tags.Find("external-id")[1])
	bridge, err := nostr.SecretKeyFromHex(string(policy.Credentials.(fakeCredentialStore)["env:BRIDGE"]))
	require.NoError(t, err)
	require.Equal(t, bridge.Public(), root.PubKey)

	operation, found, err := policy.loadForgeOperation(context.Background(), operationID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, forgeOperationComplete, operation.State)
	require.Equal(t, forge.CredentialUser, operation.CredentialMode)
	mapping, found, err := policy.loadMappingByNostrRoot(context.Background(), forge.BackendGitHub, "connection", root.ID.Hex())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "provider", mapping.Authority)
}

func TestProviderPullRequestRootPersistsImmutableBase(t *testing.T) {
	policy, store, provider, actor, repository, _ := configureForgeCollaborationTest(t)
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	provider.result = forge.CollaborationResult{
		ExternalID: "9100", ExternalNumber: 18, ExternalURL: "https://github.com/lapis/notes/pull/18",
		State: "open", HeadSHA: head, HeadRef: "feature/history", BaseSHA: base, BaseRef: "main",
	}
	target := "https://relay.example/api/forge/repositories/" + repository.PubKey.Hex() + "/notes/pull-requests"
	body := []byte(`{"title":"Historical diff","body":"Retain these objects","head":"feature/history","base":"main","head_sha":"` + head + `"}`)
	request := signedForgeRequest(t, http.MethodPost, target, body, actor)
	request.Header.Set("Idempotency-Key", "pull-18")
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, request)
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))

	roots := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{projecttask.KindPullRequest}})
	require.Len(t, roots, 1)
	require.Equal(t, head, roots[0].Tags.Find("c")[1])
	require.Equal(t, base, roots[0].Tags.Find("base-commit")[1])
	require.Equal(t, "main", roots[0].Tags.Find("target-branch")[1])

	state, err := projecttask.ReducePullRequest(roots[0], repository, nil)
	require.NoError(t, err)
	require.Equal(t, base, state.BaseCommit)
}

func TestProviderOriginStateIsCanonicalButRelayOriginIsObservation(t *testing.T) {
	policy, store, provider, actor, repository, connectionID := configureForgeCollaborationTest(t)
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	bridgeRaw := policy.Credentials.(fakeCredentialStore)["env:BRIDGE"]
	bridge, err := nostr.SecretKeyFromHex(string(bridgeRaw))
	require.NoError(t, err)
	root := nostr.Event{Kind: nostr.KindIssue, CreatedAt: nostr.Now(), Content: "Provider root", Tags: nostr.Tags{{"a", coordinate.String()}, {"p", repository.PubKey.Hex()}, {"subject", "Provider root"}}}
	require.NoError(t, root.Sign(bridge))
	require.NoError(t, store.SaveEvent(root))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), root))
	require.NoError(t, policy.saveForgeMapping(context.Background(), forgeObjectMapping{Provider: forge.BackendGitHub, ConnectionID: connectionID, Coordinate: coordinate, ObjectType: "issue", NostrEventID: root.ID.Hex(), NostrRootID: root.ID.Hex(), ExternalID: "8001", ExternalNumber: 12, Origin: "provider", Authority: "provider"}))

	provider.result = forge.CollaborationResult{ExternalID: "8001", ExternalNumber: 12, State: "closed"}
	target := "https://relay.example/api/forge/work-items/" + root.ID.Hex() + "/state"
	body := []byte(`{"state":"closed"}`)
	recorder := httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodPost, target, body, actor))
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	statuses := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindStatusClosed}, Tags: nostr.TagMap{"e": {root.ID.Hex()}}})
	require.Len(t, statuses, 1)
	require.Equal(t, bridge.Public(), statuses[0].PubKey)

	relayAuthor := nostr.Generate()
	relayRoot := nostr.Event{Kind: nostr.KindIssue, CreatedAt: nostr.Now(), Content: "Relay root", Tags: nostr.Tags{{"a", coordinate.String()}, {"subject", "Relay root"}}}
	require.NoError(t, relayRoot.Sign(relayAuthor))
	require.NoError(t, store.SaveEvent(relayRoot))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), relayRoot))
	require.NoError(t, policy.saveForgeMapping(context.Background(), forgeObjectMapping{Provider: forge.BackendGitHub, ConnectionID: connectionID, Coordinate: coordinate, ObjectType: "issue", NostrEventID: relayRoot.ID.Hex(), NostrRootID: relayRoot.ID.Hex(), ExternalID: "8002", ExternalNumber: 13, Origin: "relay", Authority: "relay"}))
	provider.result = forge.CollaborationResult{ExternalID: "8002", ExternalNumber: 13, State: "closed"}
	target = "https://relay.example/api/forge/work-items/" + relayRoot.ID.Hex() + "/state"
	recorder = httptest.NewRecorder()
	policy.ServeForgeAPI(recorder, signedForgeRequest(t, http.MethodPost, target, body, actor))
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Empty(t, collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindStatusClosed}, Tags: nostr.TagMap{"e": {relayRoot.ID.Hex()}}}))
	notes := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindTextNote}, Tags: nostr.TagMap{"e": {relayRoot.ID.Hex()}, "t": {"external-state"}}})
	require.Len(t, notes, 1)
	require.NotContains(t, forgeTagValues(notes[0], "t"), "approval")
}

func TestNativeNostrRootRemainsAuthoritativeAndOutboxRebuildsCrashGap(t *testing.T) {
	policy, store, provider, actor, repository, connectionID := configureForgeCollaborationTest(t)
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	root := nostr.Event{Kind: nostr.KindIssue, CreatedAt: nostr.Now(), Content: "Native body", Tags: nostr.Tags{{"a", coordinate.String()}, {"subject", "Native issue"}}}
	require.NoError(t, root.Sign(actor))
	require.NoError(t, store.SaveEvent(root))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), root))
	require.NoError(t, policy.Remember(context.Background(), root))

	// Simulate the event-store/outbox crash gap, then rebuild it from signed
	// relay history without changing the event or creating a second root.
	_, err := GetDb().Exec(policy.Events.Schema.Render(`DELETE FROM {{.Name}}__forge_outbox WHERE source_event_id = $1`), root.ID.Hex())
	require.NoError(t, err)
	require.NoError(t, policy.rebuildForgeOutbox(context.Background()))
	var queued int
	require.NoError(t, GetDb().QueryRow(policy.Events.Schema.Render(`SELECT COUNT(*) FROM {{.Name}}__forge_outbox WHERE source_event_id = $1`), root.ID.Hex()).Scan(&queued))
	require.Equal(t, 1, queued)

	provider.result = forge.CollaborationResult{ExternalID: "8300", ExternalNumber: 31, ExternalURL: "https://github.com/lapis/notes/issues/31", State: "open"}
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	roots := collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindIssue}, Tags: nostr.TagMap{"a": {coordinate.String()}}})
	require.Len(t, roots, 1)
	require.Equal(t, root.ID, roots[0].ID)
	mapping, found, err := policy.loadMappingByNostrRoot(context.Background(), forge.BackendGitHub, connectionID, root.ID.Hex())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "relay", mapping.Origin)
	require.Equal(t, "relay", mapping.Authority)
	require.Equal(t, "8300", mapping.ExternalID)
}

func TestRelayPullRequestReviewsProjectAsNativeGitHubReviews(t *testing.T) {
	policy, store, provider, actor, repository, connectionID := configureForgeCollaborationTest(t)
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	head := strings.Repeat("a", 40)
	root := nostr.Event{Kind: projecttask.KindPullRequest, CreatedAt: nostr.Now(), Content: "Review body", Tags: nostr.Tags{
		{"a", coordinate.String()}, {"p", repository.PubKey.Hex()}, {"subject", "Native review"},
		{"c", head}, {"clone", "https://github.com/lapis/notes.git"},
	}}
	require.NoError(t, root.Sign(actor))
	require.NoError(t, store.SaveEvent(root))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), root))
	require.NoError(t, policy.saveForgeMapping(context.Background(), forgeObjectMapping{
		Provider: forge.BackendGitHub, ConnectionID: connectionID, Coordinate: coordinate,
		ObjectType: "pull_request", NostrEventID: root.ID.Hex(), NostrRootID: root.ID.Hex(),
		ExternalID: "9100", ExternalNumber: 18, Origin: "provider", Authority: "provider",
	}))

	review := nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Now(), Content: "Please add coverage", Tags: nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"c", head}, {"t", "changes-requested"},
	}}
	require.NoError(t, review.Sign(actor))
	require.NoError(t, store.SaveEvent(review))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), review))
	require.NoError(t, policy.Remember(context.Background(), review))
	provider.result = forge.CollaborationResult{ExternalID: "9200", ExternalURL: "https://github.com/lapis/notes/pull/18#review", State: "changes_requested"}
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Len(t, provider.commands, 1)
	require.Equal(t, "review", provider.commands[0].Kind)
	var reviewPayload map[string]any
	require.NoError(t, json.Unmarshal(provider.commands[0].Payload, &reviewPayload))
	require.Equal(t, "REQUEST_CHANGES", reviewPayload["event"])
	require.Equal(t, head, reviewPayload["commit_id"])
	require.EqualValues(t, 18, reviewPayload["external_number"])
	reviewMapping, found, err := policy.loadMappingByNostrEvent(context.Background(), forge.BackendGitHub, connectionID, review.ID.Hex(), "review")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "9200", reviewMapping.ExternalID)

	inline := nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Now(), Content: "Please clarify this line", Tags: nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"c", head},
		{"t", "inline-comment"}, {"file", "README.md"}, {"side", "new"}, {"line", "4"},
	}}
	require.NoError(t, inline.Sign(actor))
	require.NoError(t, store.SaveEvent(inline))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), inline))
	require.NoError(t, policy.Remember(context.Background(), inline))
	provider.result = forge.CollaborationResult{ExternalID: "9300", ExternalURL: "https://github.com/lapis/notes/pull/18#discussion_r9300"}
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Len(t, provider.commands, 2)
	require.Equal(t, "review_comment", provider.commands[1].Kind)
	var inlinePayload map[string]any
	require.NoError(t, json.Unmarshal(provider.commands[1].Payload, &inlinePayload))
	require.Equal(t, head, inlinePayload["commit_id"])
	require.Equal(t, "README.md", inlinePayload["path"])
	require.Equal(t, "RIGHT", inlinePayload["side"])
	require.EqualValues(t, 4, inlinePayload["line"])
	commentMapping, found, err := policy.loadMappingByNostrEvent(context.Background(), forge.BackendGitHub, connectionID, inline.ID.Hex(), "comment")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "9300", commentMapping.ExternalID)
}

func TestRelayReviewCommentReactionProjectsToGitHubAndDeletionRemovesIt(t *testing.T) {
	policy, store, provider, actor, repository, connectionID := configureForgeCollaborationTest(t)
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}
	root := nostr.Event{Kind: projecttask.KindPullRequest, CreatedAt: nostr.Now(), Content: "Review body", Tags: nostr.Tags{
		{"a", coordinate.String()}, {"p", repository.PubKey.Hex()}, {"subject", "Review reaction"},
		{"c", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, {"clone", "https://github.com/lapis/notes.git"},
	}}
	require.NoError(t, root.Sign(actor))
	require.NoError(t, store.SaveEvent(root))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), root))
	require.NoError(t, policy.saveForgeMapping(context.Background(), forgeObjectMapping{
		Provider: forge.BackendGitHub, ConnectionID: connectionID, Coordinate: coordinate,
		ObjectType: "pull_request", NostrEventID: root.ID.Hex(), NostrRootID: root.ID.Hex(),
		ExternalID: "9100", ExternalNumber: 18, Origin: "provider", Authority: "provider",
	}))
	bridge, err := policy.bridgeSecretForConnection(context.Background(), connectionID)
	require.NoError(t, err)
	comment := nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Now(), Content: "Please change this", Tags: nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"p", root.PubKey.Hex()},
		{"t", "external-review"}, {"path", "README.md"},
	}}
	require.NoError(t, comment.Sign(bridge))
	require.NoError(t, store.SaveEvent(comment))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), comment))
	require.NoError(t, policy.saveForgeMapping(context.Background(), forgeObjectMapping{
		Provider: forge.BackendGitHub, ConnectionID: connectionID, Coordinate: coordinate,
		ObjectType: "activity", NostrEventID: comment.ID.Hex(), NostrRootID: root.ID.Hex(),
		ExternalID: "9300", Origin: "provider", Authority: "observation",
	}))

	reaction := nostr.Event{Kind: nostr.KindReaction, CreatedAt: nostr.Now(), Content: "❤️", Tags: nostr.Tags{
		{"e", comment.ID.Hex()}, {"p", comment.PubKey.Hex()}, {"k", "1"}, {"a", coordinate.String()},
	}}
	require.NoError(t, reaction.Sign(actor))
	require.NoError(t, store.SaveEvent(reaction))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), reaction))
	require.NoError(t, policy.Remember(context.Background(), reaction))
	provider.result = forge.CollaborationResult{ExternalID: "9400", ExternalAuthor: "lapis-bot"}
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Len(t, provider.commands, 1)
	require.Equal(t, "reaction_add", provider.commands[0].Kind)
	var addPayload map[string]any
	require.NoError(t, json.Unmarshal(provider.commands[0].Payload, &addPayload))
	require.Equal(t, "9300", addPayload["external_comment_id"])
	require.Equal(t, "pull_request_review_comment", addPayload["comment_type"])
	require.Equal(t, "heart", addPayload["content"])

	snapshot := githubWebhookPayload{}
	snapshot.Comment.ID = 9300
	snapshot.Reactions = []githubWebhookReaction{{
		ID: 9400, Content: "heart", CreatedAt: time.Now(),
		User: githubWebhookUser{ID: 74, Login: "lapis-bot"},
	}}
	require.NoError(t, policy.syncExternalCommentReactions(
		context.Background(),
		forge.Repository{Coordinate: coordinate, Announcement: repository},
		githubWebhookDelivery{ConnectionID: connectionID, ReceivedAt: time.Now()},
		snapshot,
	))
	require.Len(t, collectForgeEvents(store, nostr.Filter{Kinds: []nostr.Kind{nostr.KindReaction}}), 1)

	deletion := nostr.Event{Kind: nostr.KindDeletion, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"e", reaction.ID.Hex()}}}
	require.NoError(t, deletion.Sign(actor))
	require.NoError(t, store.SaveEvent(deletion))
	require.NoError(t, policy.ProjectTasks.Remember(context.Background(), deletion))
	require.NoError(t, policy.Remember(context.Background(), deletion))
	require.NoError(t, policy.ProcessForgeOutboxOnce(context.Background()))
	require.Len(t, provider.commands, 2)
	require.Equal(t, "reaction_delete", provider.commands[1].Kind)
	var deletePayload map[string]any
	require.NoError(t, json.Unmarshal(provider.commands[1].Payload, &deletePayload))
	require.Equal(t, "9300", deletePayload["external_comment_id"])
	require.Equal(t, "9400", deletePayload["external_reaction_id"])
}

func collectForgeEvents(store *EventStore, filter nostr.Filter) []nostr.Event {
	events := make([]nostr.Event, 0)
	for event := range store.QueryEvents(filter, 100) {
		events = append(events, event)
	}
	return events
}

func forgeTagValues(event nostr.Event, name string) []string {
	values := make([]string, 0)
	for _, tag := range event.Tags {
		if len(tag) > 1 && tag[0] == name {
			values = append(values, tag[1])
		}
	}
	return values
}
