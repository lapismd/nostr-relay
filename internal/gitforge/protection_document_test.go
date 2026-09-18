package gitforge

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseProtectionDocumentNormalizesAndRejectsInvalidInput(t *testing.T) {
	document, err := ParseProtectionDocument([]byte(`{
		"protected_patterns": ["release/*", "release/*"],
		"required_approvals": 0,
		"admin_bypass": true
	}`))
	require.NoError(t, err)
	require.Equal(t, ProtectionDocument{
		Patterns:          []string{"refs/heads/release/*"},
		RequiredApprovals: 1,
		AdminBypass:       true,
	}, document)

	_, err = ParseProtectionDocument([]byte(`{"protected_patterns":["*"],"required_approvals":1,"admin_bypass":false}`))
	require.ErrorIs(t, err, ErrInvalidProtectionPattern)

	_, err = ParseProtectionDocument([]byte(`{"protected_patterns":[],"required_approvals":17,"admin_bypass":false}`))
	require.ErrorIs(t, err, ErrInvalidProtectionDocument)

	_, err = ParseProtectionDocument([]byte(`{"protected_patterns":[],"required_approvals":1,"admin_bypass":false,"pull_requests_required":false}`))
	require.ErrorIs(t, err, ErrInvalidProtectionDocument)
}
