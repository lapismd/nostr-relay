package zooid

import (
	"context"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"zooid/buzzagents/protocol"
)

func TestCountStoredCountsFilterUnionExactlyAndIgnoresLimit(t *testing.T) {
	instance := createTestInstance()
	instance.Config.Policy.Open = true
	instance.Config.Policy.AnonymousRead = true
	instance.Groups.metadataCache.Store("registry", &groupMetaCache{
		found: true,
		event: nostr.Event{Kind: nostr.KindSimpleGroupMetadata, Tags: nostr.Tags{{"d", "registry"}}},
	})

	for index, kind := range []nostr.Kind{11, 1111, 1111} {
		event := nostr.Event{
			Kind:      kind,
			CreatedAt: nostr.Timestamp(100 + index),
			Content:   "post",
			Tags:      nostr.Tags{{"h", "registry"}},
		}
		event.Sign(nostr.Generate())
		if err := instance.Events.SaveEvent(event); err != nil {
			t.Fatal(err)
		}
	}

	filters := []nostr.Filter{
		{Kinds: []nostr.Kind{11}, Tags: nostr.TagMap{"h": {"registry"}}, Limit: 1},
		{Kinds: []nostr.Kind{11, 1111}, Tags: nostr.TagMap{"h": {"registry"}}, Limit: 1},
	}
	result, err := instance.CountStored(context.Background(), filters)
	if err != nil {
		t.Fatal(err)
	}
	if result.Approximate || result.Count != 3 {
		t.Fatalf("CountStored() = %#v, want exact union count 3", result)
	}
}

func TestPersonaCountsRequireOwnerOrExactLinkedAgent(t *testing.T) {
	instance := createTestInstance()
	owner := nostr.Generate().Public()
	agent := nostr.Generate().Public()
	sibling := nostr.Generate().Public()
	instance.Management.relayMembers.Store(owner, struct{}{})
	instance.Agents = NewAgentPolicy(instance.Config, instance.Management, instance.Groups)
	instance.Agents.rememberOwner(agent, owner)
	instance.Agents.rememberOwner(sibling, owner)
	instance.Agents.linkResolver = func(candidate, candidateOwner nostr.PubKey, personaID string) bool {
		return candidate == agent && candidateOwner == owner && personaID == "research"
	}
	filter := nostr.Filter{
		Kinds: []nostr.Kind{protocol.KindPersona}, Authors: []nostr.PubKey{owner},
		Tags: nostr.TagMap{"d": []string{"research"}},
	}
	if err := instance.validateCountVisibility(withAuthenticatedPubkey(t.Context(), owner), filter); err != nil {
		t.Fatalf("owner persona count rejected: %v", err)
	}
	if err := instance.validateCountVisibility(withAuthenticatedPubkey(t.Context(), agent), filter); err != nil {
		t.Fatalf("linked-agent persona count rejected: %v", err)
	}
	if err := instance.validateCountVisibility(withAuthenticatedPubkey(t.Context(), sibling), filter); err == nil {
		t.Fatal("sibling-agent persona count was accepted")
	}
	filter.Tags = nil
	if err := instance.validateCountVisibility(withAuthenticatedPubkey(t.Context(), agent), filter); err == nil {
		t.Fatal("broad linked-agent persona count was accepted")
	}
}

func TestCountStoredRejectsPrivateAndUnsafeScopes(t *testing.T) {
	instance := createTestInstance()
	instance.Config.Policy.Open = true
	instance.Config.Policy.AnonymousRead = true
	instance.Groups.metadataCache.Store("private", &groupMetaCache{
		found:   true,
		private: true,
		event: nostr.Event{
			Kind: nostr.KindSimpleGroupMetadata,
			Tags: nostr.Tags{{"d", "private"}, {"private"}},
		},
	})

	for _, filter := range []nostr.Filter{
		{Kinds: []nostr.Kind{11}, Tags: nostr.TagMap{"h": {"private"}}},
		{Kinds: []nostr.Kind{11}},
		{},
	} {
		if _, err := instance.CountStored(context.Background(), []nostr.Filter{filter}); err == nil || !strings.HasPrefix(err.Error(), "restricted:") {
			t.Fatalf("CountStored(%s) error = %v, want restricted refusal", filter.String(), err)
		}
	}
}

func TestCountSQLIsBodyFreeOrderedFreeAndLimitFree(t *testing.T) {
	store := createTestEventStore()
	filter := nostr.Filter{
		Kinds: []nostr.Kind{11, 1111},
		Tags:  nostr.TagMap{"h": {"registry"}},
		Limit: 1,
	}
	query, _, err := store.buildCountUnionQuery([]nostr.Filter{filter, filter})
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(query)
	for _, forbidden := range []string{"content", "order by", "limit "} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("count query contains %q: %s", forbidden, query)
		}
	}
	if !strings.Contains(lower, " union ") || !strings.Contains(lower, "count(*)") {
		t.Fatalf("count query does not deduplicate a filter union: %s", query)
	}
}

func TestRegistryDiscussionCountClassification(t *testing.T) {
	valid := []nostr.Filter{{
		Kinds: []nostr.Kind{11, 1111},
		Tags:  nostr.TagMap{"h": {"support-plugin"}},
		Since: 10,
		Until: 20,
		Limit: 1,
	}}
	if !registryDiscussionFilters(valid) {
		t.Fatal("registry discussion filter should require an exact count")
	}
	invalid := append([]nostr.Filter(nil), valid...)
	invalid[0].Tags["e"] = []string{"root"}
	if registryDiscussionFilters(invalid) {
		t.Fatal("additional tags must not be classified as the exact registry shape")
	}
}
