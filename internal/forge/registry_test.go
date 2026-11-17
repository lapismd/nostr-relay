package forge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type stubGitBackend struct{ name string }

func (backend stubGitBackend) Name() string { return backend.name }
func (stubGitBackend) AuthorityCapabilities(context.Context, Repository) (GitAuthorityCapabilities, error) {
	return GitAuthorityCapabilities{Clone: CloneDescriptor{Available: true, Auth: CloneAuthNostr}}, nil
}
func (stubGitBackend) ContentCapabilities(context.Context, Repository) (ContentCapabilities, error) {
	return ContentCapabilities{Browse: true, Compare: true}, nil
}
func (stubGitBackend) CloneDescriptor(context.Context, Repository) (CloneDescriptor, error) {
	return CloneDescriptor{Available: true, Auth: CloneAuthNostr}, nil
}
func (stubGitBackend) DefaultBranch(context.Context, Repository, string) (string, error) {
	return "main", nil
}
func (stubGitBackend) Refs(context.Context, Repository, RefsRequest) (RefPage, error) {
	return RefPage{}, nil
}
func (stubGitBackend) SnapshotRefs(context.Context, Repository) (map[string]string, error) {
	return map[string]string{}, nil
}
func (stubGitBackend) Resolve(context.Context, Repository, RevisionRequest) (Revision, error) {
	return Revision{}, nil
}
func (stubGitBackend) Tree(context.Context, Repository, RevisionRequest) (Tree, error) {
	return Tree{}, nil
}
func (stubGitBackend) Blob(context.Context, Repository, RevisionRequest) (Blob, error) {
	return Blob{}, nil
}
func (stubGitBackend) Commits(context.Context, Repository, CommitsRequest) (CommitPage, error) {
	return CommitPage{}, nil
}
func (stubGitBackend) Commit(context.Context, Repository, RevisionRequest) (CommitDetail, error) {
	return CommitDetail{}, nil
}
func (stubGitBackend) Compare(context.Context, Repository, CompareRequest) (Diff, error) {
	return Diff{}, nil
}
func (stubGitBackend) ReplicateRepository(context.Context, Repository, GitReplicationTarget, bool, bool) error {
	return nil
}

type stubReplicationTarget struct{ name string }

func (backend stubReplicationTarget) Name() string { return backend.name }
func (stubReplicationTarget) GitReplicationTarget(context.Context, Repository) (GitReplicationTarget, error) {
	return GitReplicationTarget{}, nil
}

func TestRegistryResolvesOnlyExplicitCapabilities(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registry.RegisterGit(stubGitBackend{name: BackendWalGit}))
	repository := Repository{Integration: Integration{
		GitBackend: BackendWalGit, CollaborationBackend: BackendRelay,
	}}
	resolved, err := registry.Resolve(context.Background(), repository)
	require.NoError(t, err)
	require.NotNil(t, resolved.Authority)
	require.NotNil(t, resolved.Content)
	require.True(t, resolved.Capabilities.Browse)
	require.True(t, resolved.Capabilities.Compare)
	require.True(t, resolved.Capabilities.Merge)
	require.NotNil(t, resolved.Capabilities.Protection)
	require.True(t, resolved.Capabilities.Protection.PullRequestsRequired)
	require.False(t, resolved.Capabilities.CI)
	require.Equal(t, BackendRelay, resolved.Capabilities.CollaborationBackend)
}

func TestRegistryKeepsUnmanagedCloneProviderNative(t *testing.T) {
	registry := NewRegistry()
	resolved, err := registry.Resolve(context.Background(), Repository{
		CloneURL:    "https://github.com/lapis/notes.git",
		Integration: Integration{GitBackend: BackendUnmanaged, CollaborationBackend: BackendRelay},
	})
	require.NoError(t, err)
	require.Nil(t, resolved.Authority)
	require.Nil(t, resolved.Content)
	require.True(t, resolved.Capabilities.Clone.Available)
	require.Equal(t, CloneAuthProvider, resolved.Capabilities.Clone.Auth)
	require.Equal(t, "https://github.com/lapis/notes.git", resolved.Capabilities.Clone.URL)
}

func TestRegistryRejectsDuplicateAndUnavailableBackends(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registry.RegisterGit(stubGitBackend{name: BackendWalGit}))
	require.Error(t, registry.RegisterGit(stubGitBackend{name: BackendWalGit}))
	_, err := registry.Resolve(context.Background(), Repository{Integration: Integration{GitBackend: BackendGitHub}})
	var backendError *Error
	require.ErrorAs(t, err, &backendError)
	require.Equal(t, "git_authority_backend_unavailable", backendError.Code)
}

func TestRegistryKeepsGitReplicationSeparateFromCollaborationMirror(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registry.RegisterGit(stubGitBackend{name: BackendWalGit}))
	require.NoError(t, registry.RegisterGitReplicationTarget(stubReplicationTarget{name: BackendGitHub}))
	resolved, err := registry.Resolve(context.Background(), Repository{Integration: Integration{
		GitAuthorityBackend: BackendWalGit, ContentBackend: BackendWalGit,
		CollaborationBackend: BackendRelay, GitReplicationTargetBackend: BackendGitHub,
	}})
	require.NoError(t, err)
	require.True(t, resolved.Capabilities.GitReplicationTarget)
	require.False(t, resolved.Capabilities.Mirror)
	require.Nil(t, resolved.Mirror)
	require.NotNil(t, resolved.ReplicationTarget)
}
