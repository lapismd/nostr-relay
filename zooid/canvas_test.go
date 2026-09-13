package zooid

import (
	"fiatjaf.com/nostr"
	"strings"
	"testing"
)

func TestChannelCanvasWritePolicy(t *testing.T) {
	creator, member, outsider := nostr.Generate().Public(), nostr.Generate().Public(), nostr.Generate().Public()
	config := &Config{}
	config.Groups.Enabled = true
	group := &GroupStore{Config: config, cachesWarmed: true}
	metadata := nostr.Event{Kind: 39000, Tags: nostr.Tags{{"d", "project"}, {"private"}}}
	group.metadataCache.Store("project", &groupMetaCache{event: metadata, found: true, private: true})
	group.creatorCache.Store("project", creator)
	members := group.getOrCreateMemberSet("project")
	members.members[creator] = struct{}{}
	members.members[member] = struct{}{}
	group.membershipFullyLoaded.Store("project", true)
	event := nostr.Event{Kind: 40100, PubKey: creator, Tags: nostr.Tags{{"h", "project"}}, Content: "# Canvas"}
	if reason := group.CheckWrite(event); reason != "" {
		t.Fatal(reason)
	}
	for _, key := range []nostr.PubKey{member, outsider} {
		denied := event
		denied.PubKey = key
		if reason := group.CheckWrite(denied); reason == "" {
			t.Fatal("unauthorized canvas accepted")
		}
	}
	for _, tags := range []nostr.Tags{nil, {{"h", "project"}, {"h", "project"}}, {{"h", "project", "extra"}}} {
		invalid := event
		invalid.Tags = tags
		if !group.IsGroupEvent(invalid) {
			t.Fatal("canvas bypassed group policy")
		}
		if reason := group.CheckWrite(invalid); reason == "" {
			t.Fatal("ambiguous canvas accepted")
		}
	}
	event.Content = strings.Repeat("é", 32768)
	if reason := group.CheckWrite(event); reason != "" {
		t.Fatal(reason)
	}
	event.Content += "x"
	if reason := group.CheckWrite(event); reason == "" {
		t.Fatal("oversized canvas accepted")
	}
	event.Content = ""
	if reason := group.CheckWrite(event); reason != "" {
		t.Fatal(reason)
	}
	metadata.Tags = append(metadata.Tags, nostr.Tag{"archived"})
	group.metadataCache.Store("project", &groupMetaCache{event: metadata, found: true, private: true})
	if reason := group.CheckWrite(event); reason == "" {
		t.Fatal("archived canvas accepted")
	}
}
