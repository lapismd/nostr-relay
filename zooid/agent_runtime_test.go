package zooid

import (
	"context"
	"encoding/base64"
	"fiatjaf.com/nostr"
	"testing"
	"time"
	"zooid/internal/khatru"
)

func TestRuntimeSnapshotOwnerIsolationAndEnvelope(t *testing.T) {
	owner, agent, outsider := nostr.Generate().Public(), nostr.Generate().Public(), nostr.Generate().Public()
	config := &Config{}
	management := &ManagementStore{Config: config, cachesWarmed: true}
	management.relayMembers.Store(owner, struct{}{})
	policy := NewAgentPolicy(config, management, &GroupStore{Config: config, Management: management})
	policy.rememberOwner(agent, owner)
	for _, kind := range []nostr.Kind{30180, 30181} {
		author, recipient := agent, owner
		if kind == 30181 {
			author, recipient = owner, agent
		}
		event := nostr.Event{Kind: kind, PubKey: author, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", recipient.Hex()}, {"d", recipient.Hex()}, {"agent", agent.Hex()}}, Content: base64.StdEncoding.EncodeToString(append([]byte{2}, make([]byte, 98)...))}
		if reason := policy.CheckWrite(context.Background(), event); reason != "" {
			t.Fatal(reason)
		}
		if !policy.CanRead(owner, event) || !policy.CanRead(agent, event) || policy.CanRead(outsider, event) {
			t.Fatal("runtime owner isolation failed")
		}
		event.Tags = append(event.Tags, nostr.Tag{"p", outsider.Hex()})
		if policy.CheckWrite(context.Background(), event) == "" {
			t.Fatal("duplicate recipient accepted")
		}
	}
}

func TestOwnerProgressBypassesEveryStorageHook(t *testing.T) {
	relay := khatru.NewRelay()
	relay.StoreEvent = func(context.Context, nostr.Event) error { t.Fatal("progress stored"); return nil }
	relay.ReplaceEvent = func(context.Context, nostr.Event) error { t.Fatal("progress replaced"); return nil }
	relay.OnEventSaved = func(context.Context, nostr.Event) { t.Fatal("progress entered saved/audit hooks") }
	event := nostr.Event{Kind: 24200, CreatedAt: nostr.Timestamp(time.Now().Unix())}
	if skip, err := relay.AddEvent(context.Background(), event); skip || err != nil {
		t.Fatalf("%v %v", skip, err)
	}
}

func TestRuntimeStorageReplacementAndSearchExclusion(t *testing.T) {
	store := createTestEventStore()
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	owner, agent := nostr.Generate(), nostr.Generate()
	content := base64.StdEncoding.EncodeToString(append([]byte{2}, make([]byte, 98)...))
	for _, kind := range []nostr.Kind{30180, 30181} {
		author, recipient := agent, owner.Public()
		if kind == 30181 {
			author, recipient = owner, agent.Public()
		}
		var latest nostr.Event
		for _, stamp := range []nostr.Timestamp{100, 102, 101} {
			event := nostr.Event{Kind: kind, CreatedAt: stamp, Content: content, Tags: nostr.Tags{{"p", recipient.Hex()}, {"d", recipient.Hex()}, {"agent", agent.Public().Hex()}}}
			event.Sign(author)
			if err := store.StoreEvent(event); err != nil {
				t.Fatal(err)
			}
			if stamp == 102 {
				latest = event
			}
		}
		count := 0
		for event := range store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{kind}}, 0) {
			count++
			if event.ID != latest.ID {
				t.Fatal("old head retained")
			}
		}
		if count != 1 {
			t.Fatalf("retained %d heads", count)
		}
		var indexed bool
		if err := GetDb().QueryRow(store.Schema.Render(`SELECT search_vector IS NOT NULL FROM {{.Name}}__events WHERE id=$1`), latest.ID.Hex()).Scan(&indexed); err != nil {
			t.Fatal(err)
		}
		if indexed {
			t.Fatal("runtime ciphertext entered search index")
		}
		if err := (&Instance{}).validateCountVisibility(context.Background(), nostr.Filter{Kinds: []nostr.Kind{kind}}); err == nil {
			t.Fatal("unsafe runtime count accepted")
		}
	}
}
