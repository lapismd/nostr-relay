package presence

import (
	"context"
	"testing"
	"time"
)

func TestSessionsPreferencesExpiryAndReplay(t *testing.T) {
	store := NewMemory()
	now := int64(1700000000)
	store.Now = func() time.Time { return time.Unix(now, 0) }
	ctx := context.Background()
	update := func(connection, status, mode, id string) Snapshot {
		t.Helper()
		s, err := store.Apply(ctx, Update{PubKey: "alice", Connection: connection, Status: status, Mode: mode, EventID: id, CreatedAt: now, Sequence: now * 1000})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	update("phone", "online", "", "1")
	now++
	if s := update("browser", "away", "", "2"); s.Status != "online" {
		t.Fatal(s)
	}
	if err := store.RemoveConnection(ctx, "browser"); err != nil {
		t.Fatal(err)
	}
	s, _ := store.Snapshot(ctx, []string{"alice"})
	if s[0].Status != "online" {
		t.Fatal(s)
	}
	now++
	if s := update("browser", "offline", "offline", "3"); s.Status != "offline" {
		t.Fatal(s)
	}
	now++
	if s := update("phone", "online", "", "4"); s.Status != "offline" || s.Mode != "offline" {
		t.Fatal(s)
	}
	now++
	if s := update("browser", "online", "auto", "5"); s.Status != "online" {
		t.Fatal(s)
	}
	now += TTL
	s, err := store.Snapshot(ctx, []string{"alice"})
	if err != nil || s[0].Status != "offline" {
		t.Fatal(s, err)
	}
	// Replaying a previously accepted event cannot revive an expired lease.
	replay, err := store.Apply(ctx, Update{PubKey: "alice", Connection: "browser", Status: "online", EventID: "5", CreatedAt: now - TTL, Sequence: (now - TTL) * 1000})
	if err != nil || replay.Status != "offline" {
		t.Fatal(replay, err)
	}
}

func TestAwayPreferenceRequiresAnUnexpiredLease(t *testing.T) {
	store := NewMemory()
	now := int64(1700000000)
	store.Now = func() time.Time { return time.Unix(now, 0) }
	_, err := store.Apply(t.Context(), Update{PubKey: "alice", Connection: "phone", Status: "online", Mode: "away", EventID: "1", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := store.Snapshot(t.Context(), []string{"alice"})
	if s[0].Status != "away" {
		t.Fatal(s)
	}
	now += TTL
	s, _ = store.Snapshot(t.Context(), []string{"alice"})
	if s[0].Status != "offline" || s[0].Mode != "away" {
		t.Fatal(s)
	}
}

func TestOutOfOrderAndSameSecondUpdates(t *testing.T) {
	store := NewMemory()
	now := int64(1700000000)
	store.Now = func() time.Time { return time.Unix(now, 0) }
	for _, u := range []Update{
		{Status: "away", Mode: "away", EventID: "new", CreatedAt: now, Sequence: 2},
		{Status: "online", Mode: "auto", EventID: "old", CreatedAt: now, Sequence: 1},
	} {
		u.PubKey = "alice"
		u.Connection = "phone"
		if _, err := store.Apply(t.Context(), u); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := store.Snapshot(t.Context(), []string{"alice"})
	if s[0].Status != "away" || s[0].Mode != "away" {
		t.Fatal(s)
	}
}
