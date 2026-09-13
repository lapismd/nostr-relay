package zooid

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"zooid/internal/presence"
)

func TestPostgresPresenceSharedNodesAndExpiry(t *testing.T) {
	events := createTestEventStore()
	if err := events.Init(); err != nil {
		t.Fatal(err)
	}
	first, err := presence.NewPostgres(GetDb(), Env("DATABASE_URL"), events.Schema.Name)
	if err != nil {
		t.Fatal(err)
	}
	second, err := presence.NewPostgres(GetDb(), Env("DATABASE_URL"), events.Schema.Name)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	notifications := make(chan string, 20)
	finished := make(chan error, 1)
	go func() { finished <- second.Watch(ctx, func(key string) { notifications <- key }) }()
	select {
	case key := <-notifications:
		if key != "" {
			t.Fatal(key)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener not ready")
	}
	key, phone, browser := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().Unix()
	_, err = first.Apply(ctx, presence.Update{PubKey: key, Connection: phone, Status: "online", EventID: "1", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-notifications:
		if got != key {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("other node did not receive committed update")
	}
	_, err = second.Apply(ctx, presence.Update{PubKey: key, Connection: browser, Status: "away", EventID: "2", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.RemoveConnection(ctx, browser); err != nil {
		t.Fatal(err)
	}
	s, err := second.Snapshot(ctx, []string{key})
	if err != nil || s[0].Status != "online" {
		t.Fatal(s, err)
	}
	_, err = second.Apply(ctx, presence.Update{PubKey: key, Connection: browser, Status: "offline", Mode: "offline", EventID: "3", CreatedAt: now, Sequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	s, err = first.Snapshot(ctx, []string{key})
	if err != nil || s[0].Status != "offline" || s[0].Mode != "offline" {
		t.Fatal(s, err)
	}
	_, err = first.Apply(ctx, presence.Update{PubKey: key, Connection: phone, Status: "online", Mode: "auto", EventID: "4", CreatedAt: now, Sequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	s, err = second.Snapshot(ctx, []string{key})
	if err != nil || s[0].Status != "online" || s[0].Mode != "auto" {
		t.Fatal(s, err)
	}
	// Simulate a crashed node's stale lease without running cleanup.
	_, err = GetDb().ExecContext(ctx, "UPDATE "+events.Schema.Prefix("presence_leases")+" SET lease=jsonb_set(lease,'{ExpiresAt}',to_jsonb($1::bigint)) WHERE pubkey=$2", now-1, key)
	if err != nil {
		t.Fatal(err)
	}
	s, err = second.Snapshot(ctx, []string{key})
	if err != nil || s[0].Status != "offline" {
		t.Fatal(s, err)
	}
	// Replaying an accepted envelope cannot revive an expired session.
	replayed, err := first.Apply(ctx, presence.Update{PubKey: key, Connection: phone, Status: "online", Mode: "auto", EventID: "4", CreatedAt: now, Sequence: 2})
	if err != nil || replayed.Status != "offline" {
		t.Fatal(replayed, err)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not stop")
	}
}
