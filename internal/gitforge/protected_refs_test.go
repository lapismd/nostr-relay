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
