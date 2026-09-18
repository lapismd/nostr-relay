package gitforge

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluateProtectedRefUpdatesDeniesUserDefaultBranch(t *testing.T) {
	old := strings.Repeat("a", 40)
	next := strings.Repeat("b", 40)
	decision := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		Principal:    strings.Repeat("c", 64),
		ProtectedRef: "main",
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/main"},
		},
	})
	require.Equal(t, "protected_ref_update_denied", decision.Code)
}

func TestEvaluateProtectedRefUpdatesAllowsFeatureAndJJSync(t *testing.T) {
	old := strings.Repeat("0", 40)
	next := strings.Repeat("b", 40)
	decision := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		Principal:    strings.Repeat("c", 64),
		ProtectedRef: "refs/heads/main",
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/feature"},
			{OldOID: old, NewOID: next, Ref: "refs/heads/jj-sync/scope"},
		},
	})
	require.True(t, decision.Allow)
}

func TestEvaluateProtectedRefUpdatesAllowsEmptyBootstrapAndCoordinator(t *testing.T) {
	old := strings.Repeat("0", 40)
	next := strings.Repeat("b", 40)
	bootstrap := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		Principal:    strings.Repeat("c", 64),
		ProtectedRef: "refs/heads/main",
		EmptyRepo:    true,
		CanMaintain:  true,
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/main"},
		},
	})
	require.True(t, bootstrap.Allow)
	coordinator := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		Principal:    MergeCoordinatorPrincipal("aa", "notes"),
		ProtectedRef: "refs/heads/main",
		Commands: []ReceivePackCommand{
			{OldOID: strings.Repeat("a", 40), NewOID: next, Ref: "refs/heads/main"},
		},
	})
	require.True(t, coordinator.Allow)
	replica := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		Principal:    ReplicaPrincipalPrefix + "connection",
		ProtectedRef: "refs/heads/main",
		Commands: []ReceivePackCommand{
			{OldOID: strings.Repeat("a", 40), NewOID: next, Ref: "refs/heads/main"},
		},
	})
	require.True(t, replica.Allow)
}

func TestEvaluateProtectedRefUpdatesDeniesExtraPatternUpdate(t *testing.T) {
	old := strings.Repeat("a", 40)
	next := strings.Repeat("b", 40)
	decision := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		Principal:         strings.Repeat("c", 64),
		ProtectedRef:      "refs/heads/main",
		ProtectedPatterns: []string{"release/*", "*", "jj-sync/*"},
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/release/1.0"},
		},
	})
	require.Equal(t, "protected_ref_update_denied", decision.Code)

	create := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		Principal:         strings.Repeat("c", 64),
		ProtectedRef:      "refs/heads/main",
		ProtectedPatterns: []string{"refs/heads/release/*"},
		CanMaintain:       true,
		Commands: []ReceivePackCommand{
			{OldOID: strings.Repeat("0", 40), NewOID: next, Ref: "refs/heads/release/1.0"},
		},
	})
	require.True(t, create.Allow)

	unrelated := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		Principal:         strings.Repeat("c", 64),
		ProtectedRef:      "refs/heads/main",
		ProtectedPatterns: []string{"release/*"},
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/feature"},
		},
	})
	require.True(t, unrelated.Allow)
}

func TestEvaluateProtectedRefUpdatesAllowsAdminBypassUpdate(t *testing.T) {
	old := strings.Repeat("a", 40)
	next := strings.Repeat("b", 40)
	admin := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		Principal:    strings.Repeat("c", 64),
		ProtectedRef: "refs/heads/main",
		AdminBypass:  true,
		IsAdmin:      true,
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/main"},
		},
	})
	require.True(t, admin.Allow)

	deleteDenied := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		Principal:    strings.Repeat("c", 64),
		ProtectedRef: "refs/heads/main",
		AdminBypass:  true,
		IsAdmin:      true,
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: strings.Repeat("0", 40), Ref: "refs/heads/main"},
		},
	})
	require.Equal(t, "protected_ref_delete_denied", deleteDenied.Code)

	maintainer := EvaluateProtectedRefUpdates(ProtectedRefRequest{
		Principal:    strings.Repeat("c", 64),
		ProtectedRef: "refs/heads/main",
		AdminBypass:  true,
		CanMaintain:  true,
		Commands: []ReceivePackCommand{
			{OldOID: old, NewOID: next, Ref: "refs/heads/main"},
		},
	})
	require.Equal(t, "protected_ref_update_denied", maintainer.Code)
}

func TestCompileProtectionIgnoresInvalidAndDefaultPatterns(t *testing.T) {
	compiled := CompileProtection("main", []string{
		"release/*", "refs/heads/main", "*", "jj-sync/*", "release/*",
	})
	require.Equal(t, "refs/heads/main", compiled.DefaultRef)
	require.Equal(t, []string{"refs/heads/release/*"}, compiled.Patterns)
	require.True(t, compiled.Matches("release/1.0"))
	require.Equal(t, "refs/heads/release/1.0", compiled.TargetRef("release/1.0"))
	require.False(t, compiled.Matches("feature"))
}

func TestParseReceivePackCommands(t *testing.T) {
	old := strings.Repeat("0", 40)
	next := strings.Repeat("a", 40)
	body := EncodeReceivePackCommands([]ReceivePackCommand{
		{OldOID: old, NewOID: next, Ref: "refs/heads/feature"},
	})
	commands, err := ParseReceivePackCommands(body)
	require.NoError(t, err)
	require.Equal(t, []ReceivePackCommand{{
		OldOID: old, NewOID: next, Ref: "refs/heads/feature",
	}}, commands)
}
