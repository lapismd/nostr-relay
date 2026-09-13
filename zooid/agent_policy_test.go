package zooid

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"zooid/buzzagents/protocol"
	"zooid/internal/presence"
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

func TestManagedAuthTagRequiresOneExactRelayScopedTag(t *testing.T) {
	owner := nostr.Generate().Public().Hex()
	relay := nostr.Generate().Public().Hex()
	tag, found := managedAuthTag(nostr.Tags{{"relay", "challenge"}, {"lapis-managed", owner, relay}})
	if !found || tag == nil || tag[1] != owner || tag[2] != relay {
		t.Fatalf("valid managed tag was not extracted: %#v, %v", tag, found)
	}
	for _, tags := range []nostr.Tags{
		{{"lapis-managed", owner}},
		{{"lapis-managed", owner, relay, "extra"}},
		{{"lapis-managed", owner, relay}, {"lapis-managed", owner, relay}},
		{{"lapis-managed", "invalid", relay}},
	} {
		if tag, found := managedAuthTag(tags); !found || tag != nil {
			t.Fatalf("malformed managed tags were accepted: %#v", tags)
		}
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

func TestPresencePolicySynthesizesRelaySignedPresence(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	relaySecret := nostr.MustSecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000003")
	subjectSecret := nostr.MustSecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000004")
	config := &Config{secret: relaySecret}
	management := &ManagementStore{Config: config, cachesWarmed: true}
	backend := presence.NewMemory()
	backend.Now = func() time.Time { return now }
	_, err := backend.Apply(t.Context(), presence.Update{PubKey: subjectSecret.Public().Hex(), Connection: "test", Status: "online", EventID: "test", CreatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	policy := NewPresencePolicy(&Instance{Config: config, Management: management}, backend)
	events, err := policy.Query(withAuthenticatedPubkey(t.Context(), subjectSecret.Public()), []nostr.Filter{{
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

func TestAgentPolicyRequiresExactCurrentPersonaLink(t *testing.T) {
	owner := nostr.Generate().Public()
	agent := nostr.Generate().Public()
	sibling := nostr.Generate().Public()
	config := &Config{}
	management := &ManagementStore{Config: config, cachesWarmed: true}
	management.relayMembers.Store(owner, struct{}{})
	agentPolicy := NewAgentPolicy(config, management, &GroupStore{Config: config, Management: management})
	agentPolicy.rememberOwner(agent, owner)
	agentPolicy.rememberOwner(sibling, owner)
	agentPolicy.linkResolver = func(candidate, candidateOwner nostr.PubKey, personaID string) bool {
		return candidate == agent && candidateOwner == owner && personaID == "research"
	}

	persona := nostr.Event{
		Kind: protocol.KindPersona, PubKey: owner,
		Tags: nostr.Tags{{"d", "research"}}, Content: `{"display_name":"Research"}`,
	}
	if !agentPolicy.CanRead(owner, persona) {
		t.Fatal("owner could not read private persona")
	}
	if !agentPolicy.CanRead(agent, persona) {
		t.Fatal("exact linked agent could not read private persona")
	}
	if agentPolicy.CanRead(sibling, persona) {
		t.Fatal("sibling agent inherited persona access")
	}
	if agentPolicy.CanRead(nostr.Generate().Public(), persona) {
		t.Fatal("stranger could read private persona")
	}

	filter := nostr.Filter{
		Kinds: []nostr.Kind{protocol.KindPersona}, Authors: []nostr.PubKey{owner},
		Tags: nostr.TagMap{"d": []string{"research"}},
	}
	if !agentPolicy.CanReadPersonaFilter(agent, filter) {
		t.Fatal("exact linked persona count was rejected")
	}
	filter.Tags["d"] = []string{"other"}
	if agentPolicy.CanReadPersonaFilter(agent, filter) {
		t.Fatal("sibling persona count was accepted")
	}
}

func TestAgentPolicyProfileDeletionRevokesDerivedOwner(t *testing.T) {
	owner := nostr.Generate().Public()
	agent := nostr.Generate().Public()
	config := &Config{}
	management := &ManagementStore{Config: config, cachesWarmed: true}
	management.relayMembers.Store(owner, struct{}{})
	agentPolicy := NewAgentPolicy(config, management, &GroupStore{Config: config, Management: management})
	agentPolicy.rememberOwner(agent, owner)
	if !agentPolicy.isOwner(agent, owner) {
		t.Fatal("owner relation was not established")
	}
	agentPolicy.ForgetStored(nostr.Event{Kind: nostr.KindProfileMetadata, PubKey: agent})
	if agentPolicy.isOwner(agent, owner) {
		t.Fatal("deleted profile retained derived owner relation")
	}
}

func TestAgentPolicyProfileReplacementRevokesDerivedOwner(t *testing.T) {
	ownerSecret := nostr.Generate()
	agentSecret := nostr.Generate()
	owner := ownerSecret.Public()
	agent := agentSecret.Public()
	config := &Config{}
	management := &ManagementStore{Config: config, cachesWarmed: true}
	management.relayMembers.Store(owner, struct{}{})
	agentPolicy := NewAgentPolicy(config, management, &GroupStore{Config: config, Management: management})

	var authTag nostr.Tag
	if err := json.Unmarshal([]byte(makeOwnerAuthTag(t, ownerSecret, agent, "")), &authTag); err != nil {
		t.Fatal(err)
	}
	profile := nostr.Event{Kind: nostr.KindProfileMetadata, PubKey: agent, CreatedAt: 10, Tags: nostr.Tags{authTag}}
	agentPolicy.RememberStored(profile)
	if !agentPolicy.isOwner(agent, owner) {
		t.Fatal("valid profile did not establish derived owner relation")
	}

	profile.CreatedAt = 11
	profile.Tags = nil
	agentPolicy.RememberStored(profile)
	if agentPolicy.isOwner(agent, owner) {
		t.Fatal("replacement without a valid credential retained derived owner relation")
	}
}

func TestAgentPolicyReadsCurrentManagedAgentLink(t *testing.T) {
	store := createTestEventStore()
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	ownerSecret := nostr.Generate()
	owner := ownerSecret.Public()
	agentSecret := nostr.Generate()
	agent := agentSecret.Public()
	config := store.Config
	management := &ManagementStore{Config: config, Events: store, cachesWarmed: true}
	management.relayMembers.Store(owner, struct{}{})
	persona := nostr.Event{Kind: protocol.KindPersona, PubKey: owner, Tags: nostr.Tags{{"d", "research"}}}
	var authTag nostr.Tag
	if err := json.Unmarshal([]byte(makeOwnerAuthTag(t, ownerSecret, agent, "")), &authTag); err != nil {
		t.Fatal(err)
	}
	profile := nostr.Event{Kind: nostr.KindProfileMetadata, CreatedAt: 9, Tags: nostr.Tags{authTag}, Content: `{}`}
	profile.Sign(agentSecret)

	link := nostr.Event{
		Kind: protocol.KindManagedAgent, CreatedAt: 10,
		Tags: nostr.Tags{{"d", agent.Hex()}}, Content: `{"persona_id":"research","name_mode":"inherited"}`,
	}
	link.Sign(ownerSecret)
	if err := store.StoreEvent(link); err != nil {
		t.Fatal(err)
	}

	// Simulate restart reconstruction: current signed profiles and links in the
	// event store are the authority for derived access.
	agentPolicy := NewAgentPolicy(config, management, &GroupStore{Config: config, Management: management})
	agentPolicy.Events = store
	agentPolicy.rememberOwner(agent, owner)
	if agentPolicy.CanRead(agent, persona) {
		t.Fatal("connection credential granted persona access without a current kind:0 proof")
	}
	if err := store.StoreEvent(profile); err != nil {
		t.Fatal(err)
	}
	for event := range store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{nostr.KindProfileMetadata}}, 0) {
		agentPolicy.RememberStored(event)
	}
	if !agentPolicy.CanRead(agent, persona) {
		t.Fatal("restart reconstruction did not grant the current exact persona link")
	}

	relinked := link
	relinked.CreatedAt = 11
	relinked.Content = `{"persona_id":"writer","name_mode":"inherited"}`
	relinked.Sign(ownerSecret)
	if err := store.StoreEvent(relinked); err != nil {
		t.Fatal(err)
	}
	if agentPolicy.CanRead(agent, persona) {
		t.Fatal("replaced link retained access to old persona")
	}
	if err := store.DeleteEvent(relinked.ID); err != nil {
		t.Fatal(err)
	}
	if agentPolicy.CanRead(agent, nostr.Event{Kind: protocol.KindPersona, PubKey: owner, Tags: nostr.Tags{{"d", "writer"}}}) {
		t.Fatal("deleted link retained persona access")
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
