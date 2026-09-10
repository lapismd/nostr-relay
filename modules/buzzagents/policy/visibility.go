// Package policy contains transport-neutral visibility rules for Buzz private
// events. Relay adapters must apply these rules to query results and live fan-out.
package policy

import (
	"fiatjaf.com/nostr"
	"zooid/buzzagents/protocol"
)

// ValidateSharing accepts no shared tag or exactly ["shared","true"].
func ValidateSharing(tags nostr.Tags) error {
	return protocol.ValidateSharing(tags)
}

// CanReadPrivateEvent evaluates Buzz's owner/recipient gates without granting
// any channel role or relay administration privilege.
func CanReadPrivateEvent(reader nostr.PubKey, event nostr.Event) bool {
	switch event.Kind {
	case protocol.KindMemberAddedNotification, protocol.KindMemberRemovedNotification,
		protocol.KindAgentTurnMetric:
		return hasP(event.Tags, reader)
	case protocol.KindAgentEngram:
		return event.PubKey == reader || hasP(event.Tags, reader)
	case protocol.KindPrivateManagedAgent:
		return event.PubKey == reader
	case protocol.KindPersona, protocol.KindSharedTeamCatalog:
		return event.PubKey == reader || isShared(event.Tags)
	default:
		return true
	}
}

func hasP(tags nostr.Tags, pubkey nostr.PubKey) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "p" && tag[1] == pubkey.Hex() {
			return true
		}
	}
	return false
}

func isShared(tags nostr.Tags) bool {
	if ValidateSharing(tags) != nil {
		return false
	}
	for _, tag := range tags {
		if len(tag) == 2 && tag[0] == "shared" {
			return true
		}
	}
	return false
}
