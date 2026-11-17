package gitforge

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseProtectionDocumentMigratesLegacyAndRejectsInvalidInput(t *testing.T) {
	document, err := ParseProtectionDocument([]byte(`{
		"protected_patterns": ["release/*", "release/*"],
		"required_approvals": 0,
		"admin_bypass": true
	}`))
	require.NoError(t, err)
	require.Equal(t, ProtectionDocument{
		Version: ProtectionDocumentVersion,
		Rules: []ProtectionRule{{
			ID:          LegacyDefaultRuleID,
			Name:        LegacyDefaultRuleName,
			Enforcement: RulesetEnforcementActive,
			Targets: []RulesetTarget{
				{Type: RulesetTargetDefault},
				{Type: RulesetTargetInclude, Pattern: "refs/heads/release/*"},
			},
			Bypass:            []RulesetBypass{{Kind: RulesetBypassAdmin}},
			RestrictUpdates:   true,
			RestrictDeletions: true,
			RequiredApprovals: 1,
		}},
	}, document)

	_, err = ParseProtectionDocument([]byte(`{"protected_patterns":["*"],"required_approvals":1,"admin_bypass":false}`))
	require.ErrorIs(t, err, ErrInvalidProtectionPattern)

	_, err = ParseProtectionDocument([]byte(`{"protected_patterns":[],"required_approvals":17,"admin_bypass":false}`))
	require.ErrorIs(t, err, ErrInvalidProtectionDocument)

	_, err = ParseProtectionDocument([]byte(`{"protected_patterns":[],"required_approvals":1,"admin_bypass":false,"pull_requests_required":false}`))
	require.ErrorIs(t, err, ErrInvalidProtectionDocument)
}

func TestParseProtectionDocumentAcceptsNamedRulesets(t *testing.T) {
	document, err := ParseProtectionDocument([]byte(`{
		"version": 1,
		"rules": [{
			"name": "Release",
			"enforcement": "active",
			"targets": [
				{"type": "default"},
				{"type": "include", "pattern": "releases/**"},
				{"type": "exclude", "pattern": "releases/legacy"}
			],
			"bypass": [{"kind": "maintainer"}],
			"restrict_creations": false,
			"restrict_updates": true,
			"restrict_deletions": true,
			"required_approvals": 2
		}]
	}`))
	require.NoError(t, err)
	require.Equal(t, "release", document.Rules[0].ID)
	require.Equal(t, "refs/heads/releases/**", document.Rules[0].Targets[1].Pattern)
	require.Equal(t, 2, document.Rules[0].RequiredApprovals)
}
