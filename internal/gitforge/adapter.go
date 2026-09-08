package gitforge

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"zooid/internal/forge"
)

// WalGitAdapter exposes the existing, deliberately unchanged WalGit boundary
// through provider-neutral capability interfaces.
type WalGitAdapter struct {
	legacy Backend
}

func NewWalGitAdapter(backend Backend) *WalGitAdapter {
	return &WalGitAdapter{legacy: backend}
}

func (*WalGitAdapter) Name() string { return forge.BackendWalGit }

func (adapter *WalGitAdapter) Capabilities(_ context.Context, repository forge.Repository) (forge.Capabilities, error) {
	_, importAvailable := adapter.legacy.(*WalGitBackend)
	return forge.Capabilities{
		GitBackend: forge.BackendWalGit,
		Clone: forge.CloneDescriptor{
			Available: repository.CloneURL != "", URL: repository.CloneURL,
			Auth: forge.CloneAuthNostr,
		},
		SmartHTTP: forge.SmartHTTPCapabilities{Read: true, Write: true},
		Browse:    true, Compare: true, RefEvents: true, Provision: true, Import: importAvailable,
		// Protected refs, provider collaboration, merge, mirroring,
		// automatic PR revisions, and CI are not present in the baseline.
		Limits: forge.Limits{MaximumChangedFiles: 20000, MaximumPatchBytes: 64 << 20},
	}, nil
}

func (*WalGitAdapter) CloneDescriptor(_ context.Context, repository forge.Repository) (forge.CloneDescriptor, error) {
	return forge.CloneDescriptor{Available: repository.CloneURL != "", URL: repository.CloneURL, Auth: forge.CloneAuthNostr}, nil
}

func (*WalGitAdapter) DefaultBranch(context.Context, forge.Repository, string) (string, error) {
	// WalGit accepts HEAD anywhere a default revision is required. Resolving
	// the symbolic branch name is not part of its current JSON contract.
	return "HEAD", nil
}

func (adapter *WalGitAdapter) EnsureRepository(ctx context.Context, repository forge.Repository) error {
	return adapter.legacy.EnsureRepository(ctx, repository.Coordinate)
}

func (adapter *WalGitAdapter) ImportRepository(ctx context.Context, repository forge.Repository, request forge.ImportRequest) (forge.ImportResult, error) {
	backend, ok := adapter.legacy.(*WalGitBackend)
	if !ok {
		return forge.ImportResult{}, &forge.Error{Status: http.StatusNotImplemented, Code: "repository_import_unavailable"}
	}
	return backend.ImportRepository(ctx, repository, request)
}

func (adapter *WalGitAdapter) MirrorCommit(ctx context.Context, repository forge.Repository, commit string, target forge.CIMirrorTarget, mirrorRef string) error {
	backend, ok := adapter.legacy.(*WalGitBackend)
	if !ok {
		return &forge.Error{Status: http.StatusNotImplemented, Code: "ci_mirror_unavailable"}
	}
	return backend.MirrorCommit(ctx, repository, commit, target, mirrorRef)
}

func (adapter *WalGitAdapter) ServeGitHTTP(w http.ResponseWriter, r *http.Request, repository forge.Repository, subpath, principal string) {
	adapter.legacy.ProxyGitHTTP(w, r, legacyRepository(repository), subpath, principal)
}

func (adapter *WalGitAdapter) ServeRepositoryAPI(w http.ResponseWriter, r *http.Request, repository forge.Repository, subpath, principal string) {
	adapter.legacy.ProxyRepositoryAPI(w, r, legacyRepository(repository), subpath, principal)
}

func (adapter *WalGitAdapter) Refs(ctx context.Context, repository forge.Repository, request forge.RefsRequest) (forge.Response, error) {
	subpath := "refs"
	if request.Kind != "" {
		if request.Kind != "branches" && request.Kind != "tags" {
			return forge.Response{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_ref_kind"}
		}
		subpath += "/" + request.Kind
	}
	return adapter.repositoryResponse(ctx, repository, subpath, request.Principal)
}

func (adapter *WalGitAdapter) Resolve(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (string, error) {
	revision := strings.TrimLeft(request.Revision, "/")
	if request.Path != "" {
		revision += "/" + strings.TrimLeft(request.Path, "/")
	}
	return adapter.legacy.Resolve(ctx, legacyRepository(repository), revision, request.Principal)
}

func (adapter *WalGitAdapter) Tree(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.Response, error) {
	return adapter.repositoryResponse(ctx, repository, revisionPath("tree", request), request.Principal)
}

func (adapter *WalGitAdapter) Blob(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.Response, error) {
	return adapter.repositoryResponse(ctx, repository, revisionPath("blob", request), request.Principal)
}

func (adapter *WalGitAdapter) Commits(ctx context.Context, repository forge.Repository, request forge.CommitsRequest) (forge.Response, error) {
	path := "commits"
	if request.Ref != "" {
		path += "?ref=" + request.Ref
	}
	return adapter.repositoryResponse(ctx, repository, path, request.Principal)
}

func (adapter *WalGitAdapter) Commit(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.Response, error) {
	return adapter.repositoryResponse(ctx, repository, "commit/"+strings.TrimLeft(request.Revision, "/"), request.Principal)
}

func (adapter *WalGitAdapter) Compare(ctx context.Context, repository forge.Repository, request forge.CompareRequest) (forge.CompareResult, error) {
	result, err := adapter.legacy.Compare(ctx, legacyRepository(repository), CompareRequest{
		Base: request.Base, Head: request.Head, UseMergeBase: request.UseMergeBase,
		MaxFiles: request.MaxFiles, MaxPatchBytes: request.MaxPatchBytes, Principal: request.Principal,
	})
	if err != nil {
		return forge.CompareResult{}, err
	}
	files := make([]forge.CompareFile, 0, len(result.Files))
	for _, file := range result.Files {
		files = append(files, forge.CompareFile{
			Path: file.Path, OldPath: file.OldPath, Status: file.Status,
			Additions: file.Additions, Deletions: file.Deletions, Binary: file.Binary,
		})
	}
	return forge.CompareResult{Base: result.Base, Head: result.Head, MergeBase: result.MergeBase, Files: files, Patch: result.Patch}, nil
}

func (adapter *WalGitAdapter) repositoryResponse(ctx context.Context, repository forge.Repository, subpath, principal string) (forge.Response, error) {
	backend, ok := adapter.legacy.(*WalGitBackend)
	if !ok {
		return forge.Response{}, &forge.Error{Status: http.StatusNotImplemented, Code: "repository_api_unavailable"}
	}
	return backend.repositoryResponse(ctx, legacyRepository(repository), subpath, principal)
}

func revisionPath(prefix string, request forge.RevisionRequest) string {
	path := prefix + "/" + strings.TrimLeft(request.Revision, "/")
	if request.Path != "" {
		path += "/" + strings.TrimLeft(request.Path, "/")
	}
	return path
}

func legacyRepository(repository forge.Repository) Repository {
	class := RepositoryClass(repository.Classification)
	if class != RepositoryHosted && class != RepositoryExternal && class != RepositoryAnchor {
		class = RepositoryHosted
	}
	return Repository{
		Coordinate: repository.Coordinate, Announcement: repository.Announcement,
		Channel: repository.Channel, Class: class, CloneURL: repository.CloneURL,
	}
}

var (
	_ forge.GitBackend            = (*WalGitAdapter)(nil)
	_ forge.RepositoryProvisioner = (*WalGitAdapter)(nil)
	_ forge.RepositoryImporter    = (*WalGitAdapter)(nil)
	_ forge.CommitMirrorSource    = (*WalGitAdapter)(nil)
	_ forge.SmartHTTPBackend      = (*WalGitAdapter)(nil)
	_ forge.RepositoryAPIBackend  = (*WalGitAdapter)(nil)
)

func (adapter *WalGitAdapter) String() string {
	return fmt.Sprintf("WalGitAdapter(%s)", adapter.Name())
}
