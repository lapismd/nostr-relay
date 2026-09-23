package projecttask

import (
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestAllowsAuthorReviewDecision(t *testing.T) {
	owner := pubkey(t, "1")
	author := pubkey(t, "2")
	admin := pubkey(t, "3")
	authority := CollaborationAuthority{
		CanMaintain: func(pubkey nostr.PubKey) bool {
			return pubkey == author || pubkey == admin || pubkey == owner
		},
		IsAdmin: func(pubkey nostr.PubKey) bool { return pubkey == admin },
	}

	require.True(t, AllowsAuthorReviewDecision(admin, author, owner, authority))
	require.True(t, AllowsAuthorReviewDecision(owner, owner, owner, authority))
	require.True(t, AllowsAuthorReviewDecision(admin, admin, owner, authority))
	require.False(t, AllowsAuthorReviewDecision(author, author, owner, authority))
	require.True(t, TrustsReviewDecision(owner, owner, owner, authority))
	require.True(t, TrustsReviewDecision(admin, admin, owner, authority))
	require.False(t, TrustsReviewDecision(author, author, owner, authority))
}
