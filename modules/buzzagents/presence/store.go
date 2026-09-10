// Package presence provides the in-memory projection for Buzz kind-20001
// presence. Presence events remain ephemeral and are never persisted.
package presence

import (
	"sort"
	"sync"
	"time"

	"fiatjaf.com/nostr"
)

const TTL = 180 * time.Second

type Entry struct {
	PubKey    nostr.PubKey
	Status    string
	UpdatedAt time.Time
	Event     nostr.Event
}

type Store struct {
	mu      sync.RWMutex
	entries map[nostr.PubKey]Entry
}

func NewStore() *Store {
	return &Store{entries: make(map[nostr.PubKey]Entry)}
}

// Update records a live presence delta. "offline" removes the projection.
func (store *Store) Update(event nostr.Event, status string, now time.Time) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if status == "offline" {
		delete(store.entries, event.PubKey)
		return
	}
	store.entries[event.PubKey] = Entry{PubKey: event.PubKey, Status: status, UpdatedAt: now, Event: event}
}

// Snapshot returns fresh events matching author filters in deterministic order.
func (store *Store) Snapshot(authors []nostr.PubKey, now time.Time) []nostr.Event {
	allowed := make(map[nostr.PubKey]struct{}, len(authors))
	for _, pubkey := range authors {
		allowed[pubkey] = struct{}{}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	events := make([]nostr.Event, 0, len(store.entries))
	for pubkey, entry := range store.entries {
		if now.Sub(entry.UpdatedAt) > TTL {
			delete(store.entries, pubkey)
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[pubkey]; !ok {
				continue
			}
		}
		events = append(events, entry.Event)
	}
	sort.Slice(events, func(left, right int) bool {
		if events[left].CreatedAt != events[right].CreatedAt {
			return events[left].CreatedAt > events[right].CreatedAt
		}
		return events[left].ID.Hex() < events[right].ID.Hex()
	})
	return events
}

func ParseStatus(content string) string {
	if len(content) > 128 {
		content = content[:128]
	}
	return content
}
