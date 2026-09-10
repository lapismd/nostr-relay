package presence

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestParseStatusMatchesBuzzCompatibilityRules(t *testing.T) {
	if got := ParseStatus(`{"status":"online"}`); got != "online" {
		t.Fatalf("legacy JSON status = %q", got)
	}
	unicodeStatus := strings.Repeat("a", 127) + "é"
	got := ParseStatus(unicodeStatus)
	if len(got) != 127 || !utf8.ValidString(got) {
		t.Fatalf("status truncation produced %d invalid bytes: %q", len(got), got)
	}
	longJSON := `{"status":"` + strings.Repeat("x", 129) + `"}`
	if got := ParseStatus(longJSON); len(got) != 129 {
		t.Fatalf("legacy JSON status was unexpectedly truncated to %d bytes", len(got))
	}
}
