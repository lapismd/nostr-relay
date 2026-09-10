package presence

import (
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

func TestStoreSnapshotAndExpiry(t *testing.T) {
	store := NewStore()
	pubkey := nostr.MustPubKeyFromHex("c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5")
	event := nostr.Event{PubKey: pubkey, Kind: 20001, CreatedAt: 1000, Content: "online"}
	now := time.Unix(1000, 0)
	store.Update(event, "online", now)
	if got := store.Snapshot([]nostr.PubKey{pubkey}, now); len(got) != 1 {
		t.Fatalf("got %d presence events", len(got))
	}
	if got := store.Snapshot(nil, now.Add(TTL+time.Second)); len(got) != 0 {
		t.Fatalf("expired presence was returned: %v", got)
	}
	store.Update(event, "online", now)
	store.Update(event, "offline", now)
	if got := store.Snapshot(nil, now); len(got) != 0 {
		t.Fatalf("offline presence was returned: %v", got)
	}
}
