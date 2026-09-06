package zooid

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

func registryPolicyFixture(t *testing.T) (*RegistryPolicy, nostr.PubKey, nostr.PubKey, nostr.Event) {
	t.Helper()
	config := &Config{}
	config.Registry.Enabled = true
	config.Registry.ReleaseGroupID = "plugin-releases"
	config.Registry.CuratorRole = "registry-curator"
	groups := &GroupStore{Config: config}
	curator := nostr.MustPubKeyFromHex(strings.Repeat("1", 64))
	publisher := nostr.MustPubKeyFromHex(strings.Repeat("2", 64))
	groups.SetMemberRoles("plugin-releases", curator, []string{"registry-curator"})
	release := nostr.Event{
		ID:     nostr.MustIDFromHex(strings.Repeat("a", 64)),
		PubKey: publisher,
		Kind:   nostr.KindSimpleGroupChatMessage,
		Tags:   nostr.Tags{{"h", "plugin-releases"}, {"t", registryReleaseTopic}, {"t", "plugin:ai"}},
	}
	policy := NewRegistryPolicy(config, groups, nil)
	policy.findEventOverride = func(id nostr.ID) (nostr.Event, bool) {
		return release, id == release.ID
	}
	return policy, curator, publisher, release
}

func TestRegistryPolicyRequiresCuratorRoleAndPublisherOwnership(t *testing.T) {
	policy, curator, publisher, release := registryPolicyFixture(t)
	decision := nostr.Event{
		PubKey:  curator,
		Kind:    nostr.KindReaction,
		Content: "✅",
		Tags:    nostr.Tags{{"h", "plugin-releases"}, {"e", release.ID.Hex()}},
	}
	if handled, reason := policy.CheckWrite(decision); !handled || reason != "" {
		t.Fatalf("trusted curator decision rejected: handled=%v reason=%q", handled, reason)
	}
	decision.PubKey = publisher
	if _, reason := policy.CheckWrite(decision); !strings.HasPrefix(reason, "restricted:") {
		t.Fatalf("publisher curator decision should be restricted, got %q", reason)
	}
	decision.Content = "withdrawn"
	if _, reason := policy.CheckWrite(decision); reason != "" {
		t.Fatalf("publisher withdrawal rejected: %q", reason)
	}
	decision.PubKey = curator
	if _, reason := policy.CheckWrite(decision); !strings.Contains(reason, "own releases") {
		t.Fatalf("foreign withdrawal should fail ownership, got %q", reason)
	}
}

func TestRegistryPolicyHeadCompareAndSwap(t *testing.T) {
	policy, curator, _, release := registryPolicyFixture(t)
	createdAt := time.Unix(1_800_000_000, 0).UTC()
	headContent := registryHeadContent{
		Schema:               "lapis.registry.plugin-head/1",
		PluginID:             "ai",
		AuthorityEpoch:       "epoch-1",
		HeadRevision:         "1",
		ActiveReleaseEventID: release.ID.Hex(),
		Releases: []registryHeadRelease{{
			ReleaseEventID:  release.ID.Hex(),
			ManifestSHA256:  strings.Repeat("b", 64),
			Version:         "1.0.0",
			ReleaseSequence: "0",
			State:           "approved",
			DecisionEventID: strings.Repeat("c", 64),
		}},
		IssuedAt:   createdAt.Format(time.RFC3339),
		ValidUntil: createdAt.Add(30 * 24 * time.Hour).Format(time.RFC3339),
	}
	content, err := json.Marshal(headContent)
	if err != nil {
		t.Fatal(err)
	}
	head := nostr.Event{
		ID:        nostr.MustIDFromHex(strings.Repeat("d", 64)),
		PubKey:    curator,
		CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Kind:      nostr.KindSimpleGroupChatMessage,
		Content:   string(content),
		Tags:      nostr.Tags{{"h", "plugin-releases"}, {"t", registryHeadTopic}, {"t", "plugin:ai"}},
	}
	if handled, reason := policy.CheckWrite(head); !handled || reason != "" {
		t.Fatalf("initial head rejected: handled=%v reason=%q", handled, reason)
	}

	headContent.HeadRevision = "2"
	headContent.PreviousHeadEventID = strings.Repeat("e", 64)
	content, _ = json.Marshal(headContent)
	head.ID = nostr.MustIDFromHex(strings.Repeat("f", 64))
	head.Content = string(content)
	if _, reason := policy.CheckWrite(head); !strings.Contains(reason, "compare-and-swap") {
		t.Fatalf("wrong predecessor should fail CAS, got %q", reason)
	}

	headContent.PreviousHeadEventID = strings.Repeat("d", 64)
	content, _ = json.Marshal(headContent)
	head.Content = string(content)
	policy.latestHeadOverride = func(string) (*big.Int, nostr.ID, bool) {
		return big.NewInt(1), nostr.MustIDFromHex(strings.Repeat("d", 64)), true
	}
	if _, reason := policy.CheckWrite(head); reason != "" {
		t.Fatalf("contiguous head rejected: %q", reason)
	}
}

func TestRegistryHeadRejectsExpiryBeyondThirtyDays(t *testing.T) {
	_, curator, _, release := registryPolicyFixture(t)
	createdAt := time.Unix(1_800_000_000, 0).UTC()
	content, _ := json.Marshal(registryHeadContent{
		Schema: "lapis.registry.plugin-head/1", PluginID: "ai", AuthorityEpoch: "epoch-1", HeadRevision: "1",
		ActiveReleaseEventID: release.ID.Hex(),
		Releases:             []registryHeadRelease{{ReleaseEventID: release.ID.Hex(), ManifestSHA256: strings.Repeat("b", 64), Version: "1.0.0", ReleaseSequence: "0", State: "approved", DecisionEventID: strings.Repeat("c", 64)}},
		IssuedAt:             createdAt.Format(time.RFC3339), ValidUntil: createdAt.Add(31 * 24 * time.Hour).Format(time.RFC3339),
	})
	_, err := parseRegistryHead(nostr.Event{PubKey: curator, CreatedAt: nostr.Timestamp(createdAt.Unix()), Content: string(content)})
	if err == nil || !strings.Contains(err.Error(), "30 days") {
		t.Fatalf("expected maximum-validity failure, got %v", err)
	}
}
