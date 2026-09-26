package zooid

import (
	"context"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"zooid/internal/khatru"
)

func TestNIP94AdvertisementAndValidation(t *testing.T) {
	relay := khatru.NewRelay()
	advertiseNIP94(relay)
	found := false
	for _, nip := range relay.Info.SupportedNIPs {
		if nip == 94 {
			found = true
		}
	}
	if !found {
		t.Fatal("NIP-11 metadata does not advertise NIP-94")
	}

	digest := strings.Repeat("a", 64)
	valid := nostr.Event{Kind: kindFileMetadata, Tags: nostr.Tags{
		{"url", "https://cdn.example/file.png"},
		{"m", "image/png"},
		{"x", digest},
		{"ox", strings.Repeat("b", 64)},
		{"size", "123"},
		{"dim", "12x34"},
	}}
	if err := validateNIP94Event(valid); err != nil {
		t.Fatalf("valid NIP-94 event rejected: %v", err)
	}

	tests := []struct {
		name string
		tags nostr.Tags
	}{
		{name: "missing url", tags: nostr.Tags{{"m", "image/png"}, {"x", digest}}},
		{name: "relative url", tags: nostr.Tags{{"url", "/file"}, {"m", "image/png"}, {"x", digest}}},
		{name: "uppercase MIME", tags: nostr.Tags{{"url", "https://cdn.example/file"}, {"m", "IMAGE/PNG"}, {"x", digest}}},
		{name: "invalid digest", tags: nostr.Tags{{"url", "https://cdn.example/file"}, {"m", "image/png"}, {"x", "abc"}}},
		{name: "duplicate digest", tags: nostr.Tags{{"url", "https://cdn.example/file"}, {"m", "image/png"}, {"x", digest}, {"x", digest}}},
		{name: "negative size", tags: nostr.Tags{{"url", "https://cdn.example/file"}, {"m", "image/png"}, {"x", digest}, {"size", "-1"}}},
		{name: "zero dimension", tags: nostr.Tags{{"url", "https://cdn.example/file"}, {"m", "image/png"}, {"x", digest}, {"dim", "0x4"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateNIP94Event(nostr.Event{Kind: kindFileMetadata, Tags: test.tags}); err == nil {
				t.Fatal("invalid NIP-94 event was accepted")
			}
		})
	}
}

func TestInstanceAppliesNIP94Validation(t *testing.T) {
	instance := createTestInstance()
	defer instance.Cleanup()
	instance.Config.Policy.Open = true
	author := nostr.Generate()
	ctx := withAuthenticatedPubkey(context.Background(), author.Public())
	event := nostr.Event{
		Kind: kindFileMetadata, PubKey: author.Public(),
		Tags: nostr.Tags{
			{"url", "https://cdn.example/file.png"},
			{"m", "image/png"},
			{"x", strings.Repeat("a", 64)},
		},
	}
	if reject, reason := instance.OnEvent(ctx, event); reject {
		t.Fatalf("valid NIP-94 event rejected: %s", reason)
	}
	event.Tags = event.Tags[1:]
	if reject, reason := instance.OnEvent(ctx, event); !reject || !strings.Contains(reason, "NIP-94 url tag is required") {
		t.Fatalf("malformed NIP-94 event result = (%v, %q)", reject, reason)
	}
}
