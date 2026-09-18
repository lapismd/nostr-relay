package projecttask

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestEvaluateMergeEligibilityRequiresMaintainerApprovalAndBlocksAuthor(t *testing.T) {
	owner := pubkey(t, "1")
	author := pubkey(t, "2")
	maintainer := pubkey(t, "3")
	root := validPullRequestEvent(t, id(t, "a"), author, owner, "notes", strings.Repeat("b", 40))
	root.Tags = append(root.Tags, nostr.Tag{"target-branch", "main"})
	state := PullRequestState{
		Root: root, Lifecycle: PullRequestOpen, CurrentCommit: strings.Repeat("b", 40),
		TargetBranch: "main",
		Approvals: []PullRequestDecision{{
			Reviewer: author, Commit: strings.Repeat("b", 40), Decision: ReviewApproved,
		}},
	}
	authority := Maintainers(author, maintainer)
	require.Equal(t, "maintainer_approval_required", EvaluateMergeEligibility(
		state, maintainer, state.CurrentCommit, "refs/heads/main", authority, OwnersDocument{}, nil,
	).Code)

	state.Approvals = []PullRequestDecision{{
		Reviewer: maintainer, Commit: state.CurrentCommit, Decision: ReviewApproved,
	}}
	require.True(t, EvaluateMergeEligibility(
		state, maintainer, state.CurrentCommit, "main", authority, OwnersDocument{}, nil,
	).OK)
}

func TestEvaluateMergeEligibilityRequiresPathOwnersOrAdmin(t *testing.T) {
	owner := pubkey(t, "1")
	author := pubkey(t, "2")
	maintainer := pubkey(t, "3")
	pathOwner := pubkey(t, "4")
	admin := pubkey(t, "5")
	root := validPullRequestEvent(t, id(t, "a"), author, owner, "notes", strings.Repeat("b", 40))
	root.Tags = append(root.Tags, nostr.Tag{"target-branch", "main"})
	state := PullRequestState{
		Root: root, Lifecycle: PullRequestOpen, CurrentCommit: strings.Repeat("b", 40),
		TargetBranch: "main",
		Approvals: []PullRequestDecision{{
			Reviewer: maintainer, Commit: strings.Repeat("b", 40), Decision: ReviewApproved,
		}},
	}
	authority := CollaborationAuthority{
		CanMaintain: func(pubkey nostr.PubKey) bool {
			return pubkey == maintainer || pubkey == admin || pubkey == pathOwner
		},
		IsAdmin: func(pubkey nostr.PubKey) bool { return pubkey == admin },
	}
	owners := ParseOwnersDocument("docs/ " + pathOwner.Hex() + "\n")
	result := EvaluateMergeEligibility(state, maintainer, state.CurrentCommit, "main", authority, owners, []string{"docs/guide.md"})
	require.Equal(t, "owners_approval_required", result.Code)
	require.Equal(t, []string{pathOwner.Hex()}, result.MissingOwners)

	state.Approvals = append(state.Approvals, PullRequestDecision{
		Reviewer: admin, Commit: state.CurrentCommit, Decision: ReviewApproved,
	})
	require.True(t, EvaluateMergeEligibility(
		state, maintainer, state.CurrentCommit, "main", authority, owners, []string{"docs/guide.md"},
	).OK)
}
