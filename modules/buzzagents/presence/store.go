// Package presence provides the in-memory projection for Buzz kind-20001
// presence. Presence events remain ephemeral and are never persisted.
package presence

import (
	"encoding/json"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"fiatjaf.com/nostr"
)

const TTL = 180 * time.Second

type Entry struct {
	PubKey    nostr.PubKey
	Status    string
	UpdatedAt time.Time
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
	store.entries[event.PubKey] = Entry{PubKey: event.PubKey, Status: status, UpdatedAt: now}
}

// Snapshot returns fresh entries matching subject pubkeys in deterministic order.
func (store *Store) Snapshot(authors []nostr.PubKey, now time.Time) []Entry {
	allowed := make(map[nostr.PubKey]struct{}, len(authors))
	for _, pubkey := range authors {
		allowed[pubkey] = struct{}{}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	entries := make([]Entry, 0, len(store.entries))
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
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(left, right int) bool {
		if !entries[left].UpdatedAt.Equal(entries[right].UpdatedAt) {
			return entries[left].UpdatedAt.After(entries[right].UpdatedAt)
		}
		return entries[left].PubKey.Hex() < entries[right].PubKey.Hex()
	})
	return entries
}

func ParseStatus(content string) string {
	if len(content) > 0 && content[0] == '{' {
		var legacy struct {
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(content), &legacy) == nil && legacy.Status != "" {
			return legacy.Status
		}
		return content
	}
	if len(content) > 128 {
		end := 128
		for end > 0 && !utf8.RuneStart(content[end]) {
			end--
		}
		content = content[:end]
	}
	return content
}
