package gitforge

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluateRulesetRefUpdatesIndependentRestrictFlags(t *testing.T) {
	old := strings.Repeat("a", 40)
	next := strings.Repeat("b", 40)
	zero := strings.Repeat("0", 40)
	deleteOnly := ProtectionRule{
		Name:              "Keep history",
		Enforcement:       RulesetEnforcementActive,
		Targets:           []RulesetTarget{{Type: RulesetTargetAll}},
		RestrictDeletions: true,
	}
	deleteDenied := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		UseRulesets:  true,
		ProtectedRef: "refs/heads/main",
		Rules:        []ProtectionRule{deleteOnly},
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: zero, Ref: "refs/heads/feature"},
		},
	})
	require.Equal(t, "protected_ref_delete_denied", deleteDenied.Code)

	updateAllowed := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		UseRulesets:  true,
		ProtectedRef: "refs/heads/main",
		Rules:        []ProtectionRule{deleteOnly},
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/feature"},
		},
	})
	require.True(t, updateAllowed.Allow)
}

func TestEvaluateRulesetRefUpdatesHonorsBypassAndDisabledRules(t *testing.T) {
	old := strings.Repeat("a", 40)
	next := strings.Repeat("b", 40)
	member := strings.Repeat("c", 64)
	rule := ProtectionRule{
		Name:              "Default",
		Enforcement:       RulesetEnforcementActive,
		Targets:           []RulesetTarget{{Type: RulesetTargetDefault}},
		Bypass:            []RulesetBypass{{Kind: RulesetBypassAdmin}, {Kind: RulesetBypassMember, Pubkey: member}},
		RestrictUpdates:   true,
		RestrictDeletions: true,
	}
	admin := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		UseRulesets:  true,
		Principal:    strings.Repeat("d", 64),
		ProtectedRef: "refs/heads/main",
		IsAdmin:      true,
		Rules:        []ProtectionRule{rule},
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/main"},
		},
	})
	require.True(t, admin.Allow)

	named := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		UseRulesets:  true,
		Principal:    member,
		ProtectedRef: "refs/heads/main",
		Rules:        []ProtectionRule{rule},
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/main"},
		},
	})
	require.True(t, named.Allow)

	disabled := rule
	disabled.Enforcement = RulesetEnforcementDisabled
	unrestricted := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		UseRulesets:  true,
		ProtectedRef: "refs/heads/main",
		Rules:        []ProtectionRule{disabled},
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/main"},
		},
	})
	require.True(t, unrestricted.Allow)
}

func TestEvaluateRulesetRefUpdatesExcludeAndAnnouncement(t *testing.T) {
	old := strings.Repeat("a", 40)
	next := strings.Repeat("b", 40)
	rule := ProtectionRule{
		Name:            "Releases",
		Enforcement:     RulesetEnforcementActive,
		Targets:         []RulesetTarget{{Type: RulesetTargetInclude, Pattern: "refs/heads/releases/**"}, {Type: RulesetTargetExclude, Pattern: "refs/heads/releases/legacy"}},
		RestrictUpdates: true,
	}
	denied := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		UseRulesets:  true,
		ProtectedRef: "refs/heads/main",
		Rules:        []ProtectionRule{rule},
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/releases/1.0"},
		},
	})
	require.Equal(t, "protected_ref_update_denied", denied.Code)

	excluded := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		UseRulesets:  true,
		ProtectedRef: "refs/heads/main",
		Rules:        []ProtectionRule{rule},
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/releases/legacy"},
		},
	})
	require.True(t, excluded.Allow)

	announcement := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		UseRulesets:          true,
		ProtectedRef:         "refs/heads/main",
		AnnouncementPatterns: []string{"refs/heads/release/*"},
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/release/1.0"},
		},
	})
	require.Equal(t, "protected_ref_update_denied", announcement.Code)
}

func TestCompileRulesetsDoesNotImplicitlyProtectDefault(t *testing.T) {
	compiled := CompileRulesets("main", DefaultProtectionDocument(), nil)
	require.True(t, compiled.UseRulesets)
	require.Empty(t, compiled.Rules)
	require.False(t, compiled.AnyActiveRuleMatches("refs/heads/main"))
	require.True(t, compiled.Matches("refs/heads/main"))
}
