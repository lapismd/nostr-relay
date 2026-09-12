package zooid

import (
	"context"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func keytrTestEvent(t *testing.T) nostr.Event {
	t.Helper()
	event := nostr.Event{Kind: 31777, CreatedAt: nostr.Now(), Content: "encrypted-record", Tags: nostr.Tags{
		{"d", "Y3JlZGVudGlhbA"}, {"rp", "community.example"}, {"algo", "aes-256-gcm"}, {"kdf", "hkdf-sha256"}, {"v", "1"},
	}}
	require.NoError(t, event.Sign(nostr.Generate()))
	return event
}

func TestKeytrStoragePolicy(t *testing.T) {
	instance := createAnonymousReadTestInstance()
	event := keytrTestEvent(t)
	reject, _ := instance.OnEvent(context.Background(), event)
	require.True(t, reject, "disabled by default")
	require.False(t, instance.CanAnonymousRead(event))
	instance.Config.Policy.KeytrStorage = true
	reject, reason := instance.OnEvent(context.Background(), event)
	require.False(t, reject, reason)
	require.True(t, instance.CanAnonymousRead(event))
	reject, _ = instance.OnEvent(context.Background(), nostr.Event{Kind: 9})
	require.True(t, reject, "ordinary writes still require auth")
	instance.Config.Policy.Open = false
	reject, _ = instance.OnEvent(context.Background(), event)
	require.True(t, reject, "closed relay policy wins")
	require.False(t, instance.CanAnonymousRead(event))
}

func TestKeytrRecordValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*nostr.Event)
	}{
		{"oversized", func(e *nostr.Event) { e.Content = strings.Repeat("x", 4097) }},
		{"empty", func(e *nostr.Event) { e.Content = "" }},
		{"duplicate", func(e *nostr.Event) { e.Tags = append(e.Tags, nostr.Tag{"v", "1"}) }},
		{"legacy", func(e *nostr.Event) { e.Tags[4][1] = "3" }},
		{"bad credential", func(e *nostr.Event) { e.Tags[0][1] = "invalid!" }},
		{"group", func(e *nostr.Event) { e.Tags = append(e.Tags, nostr.Tag{"h", "private"}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := keytrTestEvent(t)
			test.mutate(&event)
			require.NoError(t, event.Sign(nostr.Generate()))
			instance := createAnonymousReadTestInstance()
			instance.Config.Policy.KeytrStorage = true
			reject, _ := instance.OnEvent(context.Background(), event)
			require.True(t, reject)
			require.False(t, instance.CanAnonymousRead(event))
		})
	}
	event := keytrTestEvent(t)
	event.Content = "tampered"
	require.False(t, validKeytrRecord(event))
}

func TestKeytrStorageRespectsBans(t *testing.T) {
	instance := createAnonymousReadTestInstance()
	instance.Config.Policy.KeytrStorage = true
	instance.Management = &ManagementStore{cachesWarmed: true}
	event := keytrTestEvent(t)
	instance.Management.bannedPubkeys.Store(event.PubKey, "test")
	reject, _ := instance.OnEvent(context.Background(), event)
	require.True(t, reject)
	instance.Management.bannedPubkeys.Delete(event.PubKey)
	instance.Management.bannedEvents.Store(event.ID, "test")
	reject, _ = instance.OnEvent(context.Background(), event)
	require.True(t, reject)
}
