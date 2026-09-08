package forge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type stubGitBackend struct{ name string }

func (backend stubGitBackend) Name() string { return backend.name }
func (stubGitBackend) Capabilities(context.Context, Repository) (Capabilities, error) {
	return Capabilities{GitBackend: BackendWalGit, Browse: true, Compare: true}, nil
}
func (stubGitBackend) CloneDescriptor(context.Context, Repository) (CloneDescriptor, error) {
	return CloneDescriptor{Available: true, Auth: CloneAuthNostr}, nil
}
func (stubGitBackend) DefaultBranch(context.Context, Repository, string) (string, error) {
	return "main", nil
}
func (stubGitBackend) Refs(context.Context, Repository, RefsRequest) (Response, error) {
	return Response{}, nil
}
func (stubGitBackend) Resolve(context.Context, Repository, RevisionRequest) (string, error) {
	return "", nil
}
func (stubGitBackend) Tree(context.Context, Repository, RevisionRequest) (Response, error) {
	return Response{}, nil
}
func (stubGitBackend) Blob(context.Context, Repository, RevisionRequest) (Response, error) {
	return Response{}, nil
}
func (stubGitBackend) Commits(context.Context, Repository, CommitsRequest) (Response, error) {
	return Response{}, nil
}
func (stubGitBackend) Commit(context.Context, Repository, RevisionRequest) (Response, error) {
	return Response{}, nil
}
func (stubGitBackend) Compare(context.Context, Repository, CompareRequest) (CompareResult, error) {
	return CompareResult{}, nil
}

func TestRegistryResolvesOnlyExplicitCapabilities(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registry.RegisterGit(stubGitBackend{name: BackendWalGit}))
	repository := Repository{Integration: Integration{
		GitBackend: BackendWalGit, CollaborationBackend: BackendRelay,
	}}
	resolved, err := registry.Resolve(context.Background(), repository)
	require.NoError(t, err)
	require.NotNil(t, resolved.Git)
	require.True(t, resolved.Capabilities.Browse)
	require.True(t, resolved.Capabilities.Compare)
	require.False(t, resolved.Capabilities.Merge)
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
	require.Nil(t, resolved.Git)
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
	require.Equal(t, "git_backend_unavailable", backendError.Code)
}
