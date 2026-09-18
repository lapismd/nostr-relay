package projecttask

import (
	"strings"

	"fiatjaf.com/nostr"
)

type MergeEligibility struct {
	OK            bool
	Code          string
	MissingOwners []string
}

type MergePolicy struct {
	RequiredApprovals int
	AdminBypass       bool
}

func EvaluateMergeEligibility(state PullRequestState, actor nostr.PubKey, expectedHead, protectedRef string, authority CollaborationAuthority, owners OwnersDocument, changedPaths []string, extraPatterns ...string) MergeEligibility {
	return EvaluateMergeEligibilityPolicy(state, actor, expectedHead, protectedRef, authority, owners, changedPaths, MergePolicy{}, extraPatterns)
}

func EvaluateMergeEligibilityPolicy(state PullRequestState, actor nostr.PubKey, expectedHead, protectedRef string, authority CollaborationAuthority, owners OwnersDocument, changedPaths []string, policy MergePolicy, extraPatterns []string) MergeEligibility {
	if state.Lifecycle != PullRequestOpen {
		return MergeEligibility{Code: "pull_request_not_open"}
	}
	if !authority.Allows(actor) {
		return MergeEligibility{Code: "merge_not_authorized"}
	}
	if !strings.EqualFold(state.CurrentCommit, expectedHead) || expectedHead == "" {
		return MergeEligibility{Code: "pull_request_head_changed"}
	}
	if !sameProtectedTarget(state.TargetBranch, protectedRef, extraPatterns) {
		return MergeEligibility{Code: "protected_target_required"}
	}
	if len(state.ChangeRequests) > 0 {
		return MergeEligibility{Code: "changes_requested"}
	}
	adminActor := policy.AdminBypass && authority.Administers(actor)
	if !adminActor && trustedNonAuthorApprovalCount(state, authority) < normalizeMergeApprovals(policy.RequiredApprovals) {
		return MergeEligibility{Code: "maintainer_approval_required"}
	}
	if adminActor {
		return MergeEligibility{OK: true}
	}
	missing := missingPathOwners(state, authority, owners, changedPaths)
	if len(missing) > 0 {
		return MergeEligibility{Code: "owners_approval_required", MissingOwners: missing}
	}
	return MergeEligibility{OK: true}
}

func normalizeMergeApprovals(value int) int {
	if value <= 0 {
		return 1
	}
	if value > 16 {
		return 16
	}
	return value
}

func sameProtectedTarget(target, protectedRef string, extraPatterns []string) bool {
	normalized := normalizeMergeRef(target)
	if normalized == normalizeMergeRef(protectedRef) {
		return true
	}
	for _, pattern := range extraPatterns {
		compiled := normalizeMergeRef(pattern)
		if compiled == normalized {
			return true
		}
		if strings.HasSuffix(compiled, "/*") {
			prefix := strings.TrimSuffix(compiled, "/*") + "/"
			rest := strings.TrimPrefix(normalized, prefix)
			if rest != normalized && rest != "" && !strings.Contains(rest, "/") {
				return true
			}
		}
	}
	return false
}

func normalizeMergeRef(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "refs/heads/main"
	}
	if strings.HasPrefix(value, "refs/") {
		return value
	}
	return "refs/heads/" + strings.TrimPrefix(value, "heads/")
}

func trustedNonAuthorApprovalCount(state PullRequestState, authority CollaborationAuthority) int {
	count := 0
	seen := make(map[nostr.PubKey]struct{})
	for _, approval := range state.Approvals {
		if approval.Reviewer == state.Root.PubKey {
			continue
		}
		if _, found := seen[approval.Reviewer]; found {
			continue
		}
		if authority.Allows(approval.Reviewer) {
			seen[approval.Reviewer] = struct{}{}
			count++
		}
	}
	return count
}

func missingPathOwners(state PullRequestState, authority CollaborationAuthority, owners OwnersDocument, changedPaths []string) []string {
	missing := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range owners.RequiredGroups(changedPaths) {
		if pathGroupSatisfied(state, authority, group.Pubkeys) {
			continue
		}
		for _, pubkey := range group.Pubkeys {
			if _, found := seen[pubkey]; found {
				continue
			}
			seen[pubkey] = struct{}{}
			missing = append(missing, pubkey)
		}
	}
	return missing
}

func pathGroupSatisfied(state PullRequestState, authority CollaborationAuthority, owners []string) bool {
	ownerSet := make(map[string]struct{}, len(owners))
	for _, pubkey := range owners {
		ownerSet[strings.ToLower(pubkey)] = struct{}{}
	}
	for _, approval := range state.Approvals {
		if approval.Reviewer == state.Root.PubKey {
			continue
		}
		if authority.Administers(approval.Reviewer) {
			return true
		}
		if _, found := ownerSet[strings.ToLower(approval.Reviewer.Hex())]; found {
			return true
		}
	}
	return false
}
