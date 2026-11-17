package protocol

import (
	"errors"
	"fiatjaf.com/nostr"
)

const (
	KindRuntimeSnapshot nostr.Kind = 30180
	KindRuntimeRequest  nostr.Kind = 30181
)

func IsRuntimeKind(kind nostr.Kind) bool {
	return kind == KindRuntimeSnapshot || kind == KindRuntimeRequest
}

// ValidateRuntimeEnvelope validates routing without decrypting retained state.
// Cached snapshots deliberately have no observer freshness window.
func ValidateRuntimeEnvelope(event nostr.Event) (ObserverRoute, error) {
	if !IsRuntimeKind(event.Kind) || len(event.Tags) != 3 {
		return ObserverRoute{}, errors.New("invalid runtime envelope")
	}
	if len(event.Content) < NIP44MinContentLength || len(event.Content) > NIP44MaxContentLength {
		return ObserverRoute{}, errors.New("runtime content exceeds NIP-44 limits")
	}
	if err := ValidateNIP44V2(event.Content); err != nil {
		return ObserverRoute{}, err
	}
	recipient, err := singleTag(event.Tags, "p")
	if err != nil {
		return ObserverRoute{}, err
	}
	d, err := singleTag(event.Tags, "d")
	if err != nil || d != recipient {
		return ObserverRoute{}, errors.New("runtime address must equal recipient")
	}
	agentRaw, err := singleTag(event.Tags, "agent")
	if err != nil {
		return ObserverRoute{}, err
	}
	if !isLowerHex(recipient, 64) || !isLowerHex(agentRaw, 64) {
		return ObserverRoute{}, errors.New("invalid runtime public key")
	}
	agent, _ := nostr.PubKeyFromHex(agentRaw)
	peer, _ := nostr.PubKeyFromHex(recipient)
	if event.Kind == KindRuntimeSnapshot && event.PubKey == agent && peer != agent {
		return ObserverRoute{Agent: agent, Owner: peer, Direction: ObserverTelemetry}, nil
	}
	if event.Kind == KindRuntimeRequest && peer == agent && event.PubKey != agent {
		return ObserverRoute{Agent: agent, Owner: event.PubKey, Direction: ObserverControl}, nil
	}
	return ObserverRoute{}, errors.New("invalid runtime author or recipient")
}
