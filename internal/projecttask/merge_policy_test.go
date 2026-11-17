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

	state.Approvals = []PullRequestDecision{{
		Reviewer: author, Commit: state.CurrentCommit, Decision: ReviewApproved,
	}}
	adminAuthor := CollaborationAuthority{
		CanMaintain: func(pubkey nostr.PubKey) bool {
			return pubkey == author || pubkey == maintainer
		},
		IsAdmin: func(pubkey nostr.PubKey) bool { return pubkey == author },
	}
	require.Equal(t, "maintainer_approval_required", EvaluateMergeEligibility(
		state, author, state.CurrentCommit, "refs/heads/main", adminAuthor, OwnersDocument{}, nil,
	).Code)
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

func TestEvaluateMergeEligibilityAcceptsExtraProtectedPattern(t *testing.T) {
	owner := pubkey(t, "1")
	author := pubkey(t, "2")
	maintainer := pubkey(t, "3")
	root := validPullRequestEvent(t, id(t, "a"), author, owner, "notes", strings.Repeat("b", 40))
	state := PullRequestState{
		Root: root, Lifecycle: PullRequestOpen, CurrentCommit: strings.Repeat("b", 40),
		TargetBranch: "release/1.0",
		Approvals: []PullRequestDecision{{
			Reviewer: maintainer, Commit: strings.Repeat("b", 40), Decision: ReviewApproved,
		}},
	}
	authority := Maintainers(maintainer)
	require.Equal(t, "protected_target_required", EvaluateMergeEligibility(
		state, maintainer, state.CurrentCommit, "main", authority, OwnersDocument{}, nil,
	).Code)
	require.True(t, EvaluateMergeEligibility(
		state, maintainer, state.CurrentCommit, "main", authority, OwnersDocument{}, nil, "release/*",
	).OK)
}

func TestEvaluateMergeEligibilityTreatsMissingTargetAsLiveDefault(t *testing.T) {
	owner := pubkey(t, "1")
	author := pubkey(t, "2")
	maintainer := pubkey(t, "3")
	root := validPullRequestEvent(t, id(t, "a"), author, owner, "notes", strings.Repeat("b", 40))
	state := PullRequestState{
		Root: root, Lifecycle: PullRequestOpen, CurrentCommit: strings.Repeat("b", 40),
		Approvals: []PullRequestDecision{{
			Reviewer: maintainer, Commit: strings.Repeat("b", 40), Decision: ReviewApproved,
		}},
	}
	authority := Maintainers(maintainer)
	require.True(t, EvaluateMergeEligibility(
		state, maintainer, state.CurrentCommit, "refs/heads/trunk", authority, OwnersDocument{}, nil,
	).OK)
	require.True(t, EvaluateMergeEligibility(
		state, maintainer, state.CurrentCommit, "master", authority, OwnersDocument{}, nil,
	).OK)
	state.TargetBranch = "develop"
	require.Equal(t, "protected_target_required", EvaluateMergeEligibility(
		state, maintainer, state.CurrentCommit, "refs/heads/trunk", authority, OwnersDocument{}, nil,
	).Code)
}

func TestEvaluateMergeEligibilityRequiresApprovalCount(t *testing.T) {
	owner := pubkey(t, "1")
	author := pubkey(t, "2")
	maintainer := pubkey(t, "3")
	second := pubkey(t, "4")
	admin := pubkey(t, "5")
	root := validPullRequestEvent(t, id(t, "a"), author, owner, "notes", strings.Repeat("b", 40))
	state := PullRequestState{
		Root: root, Lifecycle: PullRequestOpen, CurrentCommit: strings.Repeat("b", 40),
		TargetBranch: "main",
		Approvals: []PullRequestDecision{{
			Reviewer: maintainer, Commit: strings.Repeat("b", 40), Decision: ReviewApproved,
		}},
	}
	authority := CollaborationAuthority{
		CanMaintain: func(pubkey nostr.PubKey) bool {
			return pubkey == maintainer || pubkey == second || pubkey == admin
		},
		IsAdmin: func(pubkey nostr.PubKey) bool { return pubkey == admin },
	}
	policy := MergePolicy{RequiredApprovals: 2}
	require.Equal(t, "maintainer_approval_required", EvaluateMergeEligibilityPolicy(
		state, maintainer, state.CurrentCommit, "main", authority, OwnersDocument{}, nil, policy, nil,
	).Code)

	state.Approvals = append(state.Approvals, PullRequestDecision{
		Reviewer: second, Commit: state.CurrentCommit, Decision: ReviewApproved,
	})
	require.True(t, EvaluateMergeEligibilityPolicy(
		state, maintainer, state.CurrentCommit, "main", authority, OwnersDocument{}, nil, policy, nil,
	).OK)

	state.Approvals = nil
	require.True(t, EvaluateMergeEligibilityPolicy(
		state, admin, state.CurrentCommit, "main", authority, OwnersDocument{}, nil,
		MergePolicy{RequiredApprovals: 2, AdminBypass: true}, nil,
	).OK)
	require.Equal(t, "maintainer_approval_required", EvaluateMergeEligibilityPolicy(
		state, maintainer, state.CurrentCommit, "main", authority, OwnersDocument{}, nil,
		MergePolicy{RequiredApprovals: 2, AdminBypass: true}, nil,
	).Code)

	state.ChangeRequests = []PullRequestDecision{{
		Reviewer: maintainer, Commit: state.CurrentCommit, Decision: ReviewChangesRequested,
	}}
	require.Equal(t, "changes_requested", EvaluateMergeEligibilityPolicy(
		state, admin, state.CurrentCommit, "main", authority, OwnersDocument{}, nil,
		MergePolicy{RequiredApprovals: 2, AdminBypass: true}, nil,
	).Code)
}
