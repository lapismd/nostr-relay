package zooid

import (
	"context"
	"fiatjaf.com/nostr"
	"testing"
	"zooid/internal/khatru"
)

func TestRepositoryLiveContextMembershipScopeAndNonRetention(t *testing.T) {
	store := createTestEventStore()
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	owner, agent, stranger := nostr.Generate(), nostr.Generate(), nostr.Generate()
	management := &ManagementStore{Config: store.Config, Events: store, cachesWarmed: true}
	groups := &GroupStore{Config: store.Config, Management: management}
	groups.membershipFullyLoaded.Store("code", struct{}{})
	for _, pubkey := range []nostr.PubKey{owner.Public(), agent.Public()} {
		groups.getOrCreateMemberSet("code").members[pubkey] = struct{}{}
	}
	policy := NewAgentPolicy(store.Config, management, groups)
	policy.Events = store
	repository := nostr.Event{Kind: nostr.KindRepositoryAnnouncement, CreatedAt: 1, Tags: nostr.Tags{{"d", "probe"}, {"buzz-channel", "code"}}}
	repository.Sign(owner)
	if err := store.StoreEvent(repository); err != nil {
		t.Fatal(err)
	}
	event := nostr.Event{Kind: repositoryLiveContextKind, Tags: nostr.Tags{{"a", "30617:" + owner.Public().Hex() + ":probe"}, {"h", "code"}, {"r", "scope"}, {"d", "span"}}, Content: `{"schema":"jj-sync/agent-context-live","version":2,"scope_id":"scope","span_id":"span","sequence":1,"state":"running","records":[]}`}
	event.Sign(agent)
	if reason := policy.CheckWrite(t.Context(), event); reason != "" {
		t.Fatal(reason)
	}
	if !policy.CanRead(owner.Public(), event) || policy.CanRead(stranger.Public(), event) {
		t.Fatal("live context reader membership was not enforced")
	}
	relay := khatru.NewRelay()
	relay.OnEvent = func(ctx context.Context, event nostr.Event) (bool, string) {
		reason := policy.CheckWrite(ctx, event)
		return reason != "", reason
	}
	relay.StoreEvent = func(context.Context, nostr.Event) error {
		t.Fatal("ephemeral context reached persistent storage")
		return nil
	}
	if skip, err := relay.AddEvent(t.Context(), event); skip || err != nil {
		t.Fatalf("live event could not be broadcast: %v", err)
	}
	event.Tags[0][1] = "30617:" + owner.Public().Hex() + ":unrelated"
	if policy.CheckWrite(t.Context(), event) == "" || policy.CanRead(owner.Public(), event) {
		t.Fatal("unrelated repository scope accepted")
	}
	event.Tags[0][1] = "30617:" + owner.Public().Hex() + ":probe"
	delete(groups.getOrCreateMemberSet("code").members, agent.Public())
	if policy.CheckWrite(t.Context(), event) == "" || policy.CanRead(owner.Public(), event) {
		t.Fatal("departed publisher retained live context access")
	}
}
