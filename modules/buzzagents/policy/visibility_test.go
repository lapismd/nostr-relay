package policy

import (
	"testing"

	"fiatjaf.com/nostr"
	"zooid/buzzagents/protocol"
)

func TestPrivateVisibility(t *testing.T) {
	owner := nostr.MustPubKeyFromHex("79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	agent := nostr.MustPubKeyFromHex("c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5")
	stranger := nostr.MustPubKeyFromHex("f9308a019258c31049344f85f89d5229b531c845836f99b08601f113bce036f9")
	event := nostr.Event{Kind: protocol.KindAgentTurnMetric, PubKey: agent, Tags: nostr.Tags{{"p", owner.Hex()}}}
	if !CanReadPrivateEvent(owner, event) || CanReadPrivateEvent(agent, event) || CanReadPrivateEvent(stranger, event) {
		t.Fatal("turn metric visibility did not enforce owner-only scope")
	}
	event = nostr.Event{Kind: protocol.KindPersona, PubKey: owner}
	if CanReadPrivateEvent(stranger, event) {
		t.Fatal("unshared persona visible to a stranger")
	}
	event.Tags = nostr.Tags{{"shared", "true"}}
	if !CanReadPrivateEvent(stranger, event) {
		t.Fatal("shared persona hidden from a stranger")
	}
}
