package projecttask

import "fiatjaf.com/nostr"

// CollaborationAuthority is the injected channel-maintainer set used by the
// pure task and pull-request reducers. GroupStore stays in Zooid.
type CollaborationAuthority struct {
	CanMaintain func(pubkey nostr.PubKey) bool
	IsAdmin     func(pubkey nostr.PubKey) bool
}

func (authority CollaborationAuthority) Allows(pubkey nostr.PubKey) bool {
	return authority.CanMaintain != nil && authority.CanMaintain(pubkey)
}

func (authority CollaborationAuthority) Administers(pubkey nostr.PubKey) bool {
	return authority.IsAdmin != nil && authority.IsAdmin(pubkey)
}

// AllowsAuthorReviewDecision reports whether a review-decision signer may be
// the pull-request author. Ordinary maintainers cannot self-review; the
// 30617 owner or a channel/relay administrator may.
func AllowsAuthorReviewDecision(
	signer, author, repositoryOwner nostr.PubKey,
	authority CollaborationAuthority,
) bool {
	if signer != author {
		return true
	}
	return signer == repositoryOwner || authority.Administers(signer)
}

func TrustsReviewDecision(
	signer, author, repositoryOwner nostr.PubKey,
	authority CollaborationAuthority,
) bool {
	return authority.Allows(signer) &&
		AllowsAuthorReviewDecision(signer, author, repositoryOwner, authority)
}

// Maintainers builds a test or adapter authority from an explicit pubkey list.
func Maintainers(pubkeys ...nostr.PubKey) CollaborationAuthority {
	allowed := make(map[nostr.PubKey]struct{}, len(pubkeys))
	for _, pubkey := range pubkeys {
		allowed[pubkey] = struct{}{}
	}
	return CollaborationAuthority{
		CanMaintain: func(pubkey nostr.PubKey) bool {
			_, ok := allowed[pubkey]
			return ok
		},
		IsAdmin: func(pubkey nostr.PubKey) bool {
			_, ok := allowed[pubkey]
			return ok
		},
	}
}

func ReviewDecisionOf(event nostr.Event) ReviewDecisionKind {
	labels := allTagValuesFolded(event.Tags, "t")
	approved := containsFolded(labels, "approval")
	changes := containsFolded(labels, "changes-requested")
	if approved == changes {
		return ""
	}
	if approved {
		return ReviewApproved
	}
	return ReviewChangesRequested
}

func IsReviewDecision(event nostr.Event) bool {
	return ReviewDecisionOf(event) != ""
}

func BuzzChannel(event nostr.Event) string {
	return firstTagValue(event.Tags, TagBuzzChannel)
}

func containsFolded(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
