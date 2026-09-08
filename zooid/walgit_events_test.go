package zooid

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"zooid/internal/projecttask"
)

func walGitWebhookRequest(t *testing.T, secret string, events []walGitRefEvent) *http.Request {
	t.Helper()
	body, err := json.Marshal(events)
	require.NoError(t, err)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	request := httptest.NewRequest(http.MethodPost, "https://relay.example/internal/walgit/events", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Walgit-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return request
}

func refWebhookEvent(owner nostr.PubKey, repo, action, ref, old, new, sequence string) walGitRefEvent {
	event := walGitRefEvent{
		Action: action, RefType: "branch", RefName: ref, Old: old, New: new,
		Pusher: "nostr:" + owner.Hex(), CorrelationID: "request-1", Repository: owner.Hex() + "/" + repo,
	}
	event.WalGit.SchemaVersion = 1
	event.WalGit.Sequence = sequence
	event.WalGit.EntryKind = "push"
	event.WalGit.RequestID = "request-1"
	return event
}

func TestWalGitWebhookDeduplicatesAndProjectsNewestRef(t *testing.T) {
	policy, store := createGitForgeTestPolicy(t, &fakeGitBackend{})
	policy.WebhookSecret = "webhook-secret"
	owner := nostr.Generate()
	repository := readyHostedRepository(t, policy, store, owner, "notes", "repo-channel")
	zero := strings.Repeat("0", 40)
	one := strings.Repeat("1", 40)
	two := strings.Repeat("2", 40)
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}

	create := refWebhookEvent(repository.PubKey, "notes", "create", "refs/heads/main", zero, one, "18446744073709551614")
	update := refWebhookEvent(repository.PubKey, "notes", "update", "refs/heads/main", one, two, "18446744073709551615")
	for _, batch := range [][]walGitRefEvent{{create}, {update}, {create}, {update}} {
		recorder := httptest.NewRecorder()
		policy.ServeWalGitEvents(recorder, walGitWebhookRequest(t, policy.WebhookSecret, batch))
		require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	}
	oid, sequence, found, err := policy.currentRef(context.Background(), coordinate, "refs/heads/main")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, two, oid)
	require.Equal(t, "18446744073709551615", sequence)

	var count int
	err = GetDb().QueryRow(policy.Events.Schema.Render(`SELECT COUNT(*) FROM {{.Name}}__walgit_ref_events WHERE owner = $1 AND repo = $2`), repository.PubKey.Hex(), "notes").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestWalGitWebhookDeleteUnknownRepositoryAndSignature(t *testing.T) {
	policy, store := createGitForgeTestPolicy(t, &fakeGitBackend{})
	policy.WebhookSecret = "webhook-secret"
	owner := nostr.Generate()
	repository := readyHostedRepository(t, policy, store, owner, "notes", "repo-channel")
	zero := strings.Repeat("0", 40)
	one := strings.Repeat("1", 40)
	coordinate := projecttask.RepositoryCoordinate{Owner: repository.PubKey, ID: "notes"}

	create := refWebhookEvent(repository.PubKey, "notes", "create", "refs/heads/main", zero, one, "1")
	deleteEvent := refWebhookEvent(repository.PubKey, "notes", "delete", "refs/heads/main", one, zero, "2")
	recorder := httptest.NewRecorder()
	policy.ServeWalGitEvents(recorder, walGitWebhookRequest(t, policy.WebhookSecret, []walGitRefEvent{create, deleteEvent}))
	require.Equal(t, http.StatusNoContent, recorder.Code)
	_, _, found, err := policy.currentRef(context.Background(), coordinate, "refs/heads/main")
	require.NoError(t, err)
	require.False(t, found)

	unknown := refWebhookEvent(repository.PubKey, "missing", "create", "refs/heads/main", zero, one, "1")
	recorder = httptest.NewRecorder()
	policy.ServeWalGitEvents(recorder, walGitWebhookRequest(t, policy.WebhookSecret, []walGitRefEvent{unknown}))
	require.Equal(t, http.StatusNoContent, recorder.Code)

	request := walGitWebhookRequest(t, "wrong-secret", []walGitRefEvent{create})
	recorder = httptest.NewRecorder()
	policy.ServeWalGitEvents(recorder, request)
	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestWalGitWebhookRejectsMalformedSequenceAndObjectIDs(t *testing.T) {
	policy, _ := createGitForgeTestPolicy(t, &fakeGitBackend{})
	policy.WebhookSecret = "webhook-secret"
	owner := nostr.Generate().Public()
	event := refWebhookEvent(owner, "notes", "create", "refs/heads/main", strings.Repeat("0", 40), "short", "01")
	recorder := httptest.NewRecorder()
	policy.ServeWalGitEvents(recorder, walGitWebhookRequest(t, policy.WebhookSecret, []walGitRefEvent{event}))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
