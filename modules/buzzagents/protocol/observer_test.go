package protocol

import (
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

func TestValidateObserverRoutes(t *testing.T) {
	now := time.Unix(2000, 0)
	agent := nostr.MustPubKeyFromHex("c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5")
	owner := nostr.MustPubKeyFromHex("79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	event := nostr.Event{
		Kind: KindAgentObserverFrame, PubKey: agent, CreatedAt: 2000,
		Content: strings.Repeat("x", NIP44MinContentLength),
		Tags:    nostr.Tags{{"p", owner.Hex()}, {"agent", agent.Hex()}, {"frame", "telemetry"}, {"h", "general"}},
	}
	route, err := ValidateObserver(event, now)
	if err != nil {
		t.Fatal(err)
	}
	if route.Agent != agent || route.Owner != owner || route.Direction != ObserverTelemetry || route.GroupID != "general" {
		t.Fatalf("unexpected route: %+v", route)
	}

	event.PubKey = owner
	event.Tags = nostr.Tags{{"p", agent.Hex()}, {"agent", agent.Hex()}, {"frame", "control"}}
	route, err = ValidateObserver(event, now)
	if err != nil || route.Direction != ObserverControl || route.Owner != owner {
		t.Fatalf("control route: %+v, %v", route, err)
	}
}

func TestValidateObserverRejectsUnsafeEnvelope(t *testing.T) {
	now := time.Unix(2000, 0)
	agent := nostr.MustPubKeyFromHex("c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5")
	owner := nostr.MustPubKeyFromHex("79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	base := nostr.Event{Kind: KindAgentObserverFrame, PubKey: agent, CreatedAt: 2000, Content: strings.Repeat("x", 132), Tags: nostr.Tags{{"p", owner.Hex()}, {"agent", agent.Hex()}, {"frame", "telemetry"}}}

	for name, mutate := range map[string]func(*nostr.Event){
		"short ciphertext": func(event *nostr.Event) { event.Content = "short" },
		"stale":            func(event *nostr.Event) { event.CreatedAt = 1600 },
		"duplicate p":      func(event *nostr.Event) { event.Tags = append(event.Tags, nostr.Tag{"p", owner.Hex()}) },
		"self route":       func(event *nostr.Event) { event.Tags[0][1] = agent.Hex() },
	} {
		t.Run(name, func(t *testing.T) {
			event := base
			event.Tags = make(nostr.Tags, len(base.Tags))
			for index, tag := range base.Tags {
				event.Tags[index] = append(nostr.Tag(nil), tag...)
			}
			mutate(&event)
			if _, err := ValidateObserver(event, now); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}

	base.Tags[2][1] = "future-frame"
	if _, err := ValidateObserver(base, now); !errors.Is(err, ErrUnknownObserverFrame) {
		t.Fatalf("got %v, want unknown-frame sentinel", err)
	}
}

func TestValidateObserverTreatsActivitySyncPayloadAsOpaque(t *testing.T) {
	now := time.Unix(2000, 0)
	agent := nostr.MustPubKeyFromHex("c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5")
	owner := nostr.MustPubKeyFromHex("79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	ciphertext := `activity_sync_page:{"events":[{"created_at":1,"content":"historical"}]}`
	ciphertext += strings.Repeat("x", NIP44MinContentLength-len(ciphertext))
	event := nostr.Event{
		Kind: KindAgentObserverFrame, PubKey: agent, CreatedAt: 2000,
		Content: ciphertext,
		Tags:    nostr.Tags{{"p", owner.Hex()}, {"agent", agent.Hex()}, {"frame", "telemetry"}, {"h", "general"}},
	}
	route, err := ValidateObserver(event, now)
	if err != nil {
		t.Fatal(err)
	}
	if route.Agent != agent || route.Owner != owner || route.GroupID != "general" {
		t.Fatalf("unexpected opaque sync route: %+v", route)
	}
}
