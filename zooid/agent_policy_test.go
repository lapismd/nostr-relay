package zooid

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"zooid/buzzagents/protocol"
)

func TestAgentPolicyHTTPAdmissionAndOwnerScopedEvents(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	ownerSecret := nostr.MustSecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000001")
	agentSecret := nostr.MustSecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000002")
	owner := ownerSecret.Public()
	agent := agentSecret.Public()

	config := &Config{}
	config.Agents.Enabled = true
	config.Agents.AllowOwnerAuth = true
	config.Agents.EnforceCredentialKinds = true
	management := &ManagementStore{Config: config, cachesWarmed: true}
	management.relayMembers.Store(owner, struct{}{})
	groups := &GroupStore{Config: config, Management: management}
	agentPolicy := NewAgentPolicy(config, management, groups)
	agentPolicy.now = func() time.Time { return now }

	authTag := makeOwnerAuthTag(t, ownerSecret, agent, "")
	ctx, err := agentPolicy.AuthenticateHTTP(t.Context(), agent, uint64(now.Unix()), authTag)
	if err != nil {
		t.Fatalf("owner-derived admission failed: %v", err)
	}
	if !agentPolicy.IsRelayMember(ctx, agent) || management.IsMember(agent) {
		t.Fatal("agent was not virtual-only relay member")
	}

	observer := nostr.Event{
		Kind: protocol.KindAgentObserverFrame, PubKey: agent, CreatedAt: nostr.Timestamp(now.Unix()),
		Content: strings.Repeat("x", protocol.NIP44MinContentLength),
		Tags:    nostr.Tags{{"p", owner.Hex()}, {"agent", agent.Hex()}, {"frame", "telemetry"}},
	}
	if reason := agentPolicy.CheckWrite(ctx, observer); reason != "" {
		t.Fatalf("observer frame rejected: %s", reason)
	}

	groups.membershipFullyLoaded.Store("private", struct{}{})
	memberSet := groups.getOrCreateMemberSet("private")
	memberSet.members[owner] = struct{}{}
	observer.Tags = append(observer.Tags, nostr.Tag{"h", "private"})
	if reason := agentPolicy.CheckWrite(ctx, observer); !strings.Contains(reason, "agent and owner channel membership") {
		t.Fatalf("ownership unexpectedly inherited channel membership: %q", reason)
	}
}

func TestAgentPolicyEnforcesCredentialKind(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	ownerSecret := nostr.MustSecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000001")
	agent := nostr.MustSecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000002").Public()
	config := &Config{}
	config.Agents.Enabled = true
	config.Agents.AllowOwnerAuth = true
	config.Agents.EnforceCredentialKinds = true
	management := &ManagementStore{Config: config, cachesWarmed: true}
	management.relayMembers.Store(ownerSecret.Public(), struct{}{})
	agentPolicy := NewAgentPolicy(config, management, &GroupStore{Config: config, Management: management})
	agentPolicy.now = func() time.Time { return now }

	ctx, err := agentPolicy.AuthenticateHTTP(t.Context(), agent, uint64(now.Unix()), makeOwnerAuthTag(t, ownerSecret, agent, "kind=1"))
	if err != nil {
		t.Fatal(err)
	}
	if reason := agentPolicy.CheckWrite(ctx, nostr.Event{Kind: 1, PubKey: agent}); reason != "" {
		t.Fatalf("allowed kind rejected: %s", reason)
	}
	if reason := agentPolicy.CheckWrite(ctx, nostr.Event{Kind: 7, PubKey: agent}); !strings.Contains(reason, "does not allow") {
		t.Fatalf("disallowed kind accepted: %q", reason)
	}
}

func TestAgentPolicySynthesizesRelaySignedPresence(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	relaySecret := nostr.MustSecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000003")
	subjectSecret := nostr.MustSecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000004")
	config := &Config{secret: relaySecret}
	management := &ManagementStore{Config: config, cachesWarmed: true}
	agentPolicy := NewAgentPolicy(config, management, &GroupStore{Config: config, Management: management})
	agentPolicy.now = func() time.Time { return now }

	presenceEvent := nostr.Event{
		Kind: protocol.KindPresence, PubKey: subjectSecret.Public(),
		CreatedAt: nostr.Timestamp(now.Unix()), Content: "online",
	}
	agentPolicy.OnEphemeral(presenceEvent)
	events, err := agentPolicy.PresenceEvents([]nostr.Filter{{
		Kinds: []nostr.Kind{protocol.KindPresenceSnapshot}, Authors: []nostr.PubKey{subjectSecret.Public()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d presence events", len(events))
	}
	event := events[0]
	if event.Kind != protocol.KindPresence || event.PubKey != relaySecret.Public() || event.Content != "online" || !event.VerifySignature() {
		t.Fatalf("unexpected synthesized presence: %#v", event)
	}
	if p, err := exactRequiredTag(event.Tags, "p"); err != nil || p != subjectSecret.Public().Hex() {
		t.Fatalf("presence subject tag = %q, %v", p, err)
	}
}

func TestAgentPolicyRejectsClientMembershipNotifications(t *testing.T) {
	config := &Config{}
	config.Policy.Open = true
	management := &ManagementStore{Config: config, cachesWarmed: true}
	agentPolicy := NewAgentPolicy(config, management, &GroupStore{Config: config, Management: management})
	for _, kind := range []nostr.Kind{protocol.KindMemberAddedNotification, protocol.KindMemberRemovedNotification} {
		if reason := agentPolicy.CheckWrite(t.Context(), nostr.Event{Kind: kind}); !strings.Contains(reason, "relay-signed only") {
			t.Fatalf("kind %d rejection = %q", kind, reason)
		}
	}
}

func makeOwnerAuthTag(t *testing.T, ownerSecret nostr.SecretKey, agent nostr.PubKey, conditions string) string {
	t.Helper()
	digest := sha256.Sum256([]byte("nostr:agent-auth:" + agent.Hex() + ":" + conditions))
	privateKey, _ := btcec.PrivKeyFromBytes(ownerSecret[:])
	signature, err := schnorr.Sign(privateKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return `["auth","` + ownerSecret.Public().Hex() + `","` + conditions + `","` + hex.EncodeToString(signature.Serialize()) + `"]`
}
