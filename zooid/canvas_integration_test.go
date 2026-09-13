//go:build integration

package zooid

import (
	"context"
	"fiatjaf.com/nostr"
	"github.com/coder/websocket"
	"testing"
)

func TestIntegration_ChannelCanvas(t *testing.T) {
	ctx := context.Background()
	relay := setupRelay(ctx, t, true)
	defer relay.Cleanup(ctx)
	admin := newNostrClient(ctx, t, relay.URI, adminSecret)
	defer admin.close()
	other := newNostrClient(ctx, t, relay.URI, nonAdminSecret)
	defer other.close()
	create := &nostr.Event{Kind: 9007, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"h", "canvas-project"}}, Content: `{"name":"Canvas project","private":true}`}
	if result := admin.sendEvent(ctx, t, create); result != "ok" {
		t.Fatal(result)
	}
	snapshot := &nostr.Event{Kind: 40100, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"h", "canvas-project"}}, Content: "# Existing Buzz canvas\n\nShared project notes."}
	if result := admin.sendEvent(ctx, t, snapshot); result != "ok" {
		t.Fatal(result)
	}
	filter := map[string]interface{}{"kinds": []int{40100}, "#h": []string{"canvas-project"}}
	if events := admin.subscribe(ctx, t, "canvas-head", filter); len(events) != 1 || events[0].Content != snapshot.Content {
		t.Fatal("canvas did not round trip")
	}
	if events := other.subscribe(ctx, t, "private-canvas", filter); len(events) != 0 {
		t.Fatal("private canvas disclosed to nonmember")
	}
	if err := admin.conn.Write(ctx, websocket.MessageText, []byte(`["CLOSE","canvas-head"]`)); err != nil {
		t.Fatal(err)
	}
	if err := other.conn.Write(ctx, websocket.MessageText, []byte(`["CLOSE","private-canvas"]`)); err != nil {
		t.Fatal(err)
	}
	invite := &nostr.Event{Kind: 9000, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"h", "canvas-project"}, {"p", nonAdminPubkey.Hex()}}}
	if result := admin.sendEvent(ctx, t, invite); result != "ok" {
		t.Fatal(result)
	}
	draft := &nostr.Event{Kind: 40100, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"h", "canvas-project"}}, Content: "Unauthorized draft"}
	if result := other.sendEvent(ctx, t, draft); result == "ok" {
		t.Fatal("ordinary member canvas write accepted")
	}
	clear := &nostr.Event{Kind: 40100, CreatedAt: nostr.Now() + 1, Tags: nostr.Tags{{"h", "canvas-project"}}, Content: ""}
	if result := admin.sendEvent(ctx, t, clear); result != "ok" {
		t.Fatal(result)
	}
	if events := admin.subscribe(ctx, t, "canvas-history", filter); len(events) != 2 {
		t.Fatal("canvas history was not retained")
	}
	chat := admin.subscribe(ctx, t, "chat", map[string]interface{}{"kinds": []int{9}, "#h": []string{"canvas-project"}})
	if len(chat) != 0 {
		t.Fatal("canvas appeared in chat")
	}
}
