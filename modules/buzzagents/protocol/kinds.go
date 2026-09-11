// Package protocol defines the Buzz event kinds and transport/storage policy
// that the Lapis relay implements. It contains no relay or database code.
package protocol

import "fiatjaf.com/nostr"

const (
	KindAgentProfile              nostr.Kind = 10100
	KindPresence                  nostr.Kind = 20001
	KindTyping                    nostr.Kind = 20002
	KindAgentObserverFrame        nostr.Kind = 24200
	KindAgentEngram               nostr.Kind = 30174
	KindPersona                   nostr.Kind = 30175
	KindAgentTeam                 nostr.Kind = 30176
	KindManagedAgent              nostr.Kind = 30177
	KindSharedTeamCatalog         nostr.Kind = 30178
	KindPrivateManagedAgent       nostr.Kind = 30179
	KindPresenceSnapshot          nostr.Kind = 40902
	KindMemberAddedNotification   nostr.Kind = 44100
	KindMemberRemovedNotification nostr.Kind = 44101
	KindAgentTurnMetric           nostr.Kind = 44200
)

// IsWebSocketOnly reports Buzz ephemeral kinds that the HTTP bridge must
// reject because their semantics require a live connection.
func IsWebSocketOnly(kind nostr.Kind) bool {
	return kind == KindPresence || kind == KindTyping || kind == KindAgentObserverFrame
}

// IsPrivateContent reports durable encrypted kinds that must never be indexed
// into full-text search.
func IsPrivateContent(kind nostr.Kind) bool {
	switch kind {
	case KindAgentEngram, KindPersona, KindPrivateManagedAgent, KindAgentTurnMetric:
		return true
	default:
		return false
	}
}

// IsBuzzRegular reports high-numbered kinds that Buzz defines as append-only
// even though some Nostr libraries only classify the 1..9999 range as regular.
func IsBuzzRegular(kind nostr.Kind) bool {
	return kind == KindMemberAddedNotification || kind == KindMemberRemovedNotification || kind == KindAgentTurnMetric
}
