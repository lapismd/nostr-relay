package gitforge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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

func (adapter *WalGitAdapter) AuthorityCapabilities(_ context.Context, repository forge.Repository) (forge.GitAuthorityCapabilities, error) {
	_, importAvailable := adapter.legacy.(*WalGitBackend)
	return forge.GitAuthorityCapabilities{
		Clone: forge.CloneDescriptor{
			Available: repository.CloneURL != "", URL: repository.CloneURL,
			Auth: forge.CloneAuthNostr,
		},
		SmartHTTP: forge.SmartHTTPCapabilities{Read: true, Write: true}, RefEvents: true,
		Provision: true, Import: importAvailable,
	}, nil
}

func (*WalGitAdapter) ContentCapabilities(context.Context, forge.Repository) (forge.ContentCapabilities, error) {
	return forge.ContentCapabilities{Browse: true, Compare: true, Limits: forge.ContentLimits{
		MaximumChangedFiles: 20000, MaximumPatchBytes: 64 << 20,
	}}, nil
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

func (adapter *WalGitAdapter) ImportObject(ctx context.Context, repository forge.Repository, request forge.ImportObjectRequest) error {
	backend, ok := adapter.legacy.(*WalGitBackend)
	if !ok {
		return &forge.Error{Status: http.StatusNotImplemented, Code: "repository_object_import_unavailable"}
	}
	return backend.ImportObject(ctx, repository, request)
}

func (adapter *WalGitAdapter) ReplicateRepository(ctx context.Context, repository forge.Repository, target forge.GitReplicationTarget, branches, tags bool) error {
	backend, ok := adapter.legacy.(*WalGitBackend)
	if !ok {
		return &forge.Error{Status: http.StatusNotImplemented, Code: "git_replication_unavailable"}
	}
	return backend.ReplicateRepository(ctx, repository, target, branches, tags)
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

func (adapter *WalGitAdapter) Refs(ctx context.Context, repository forge.Repository, request forge.RefsRequest) (forge.RefPage, error) {
	subpath := "refs"
	if request.Kind != "" {
		if request.Kind != "branches" && request.Kind != "tags" {
			return forge.RefPage{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_ref_kind"}
		}
		subpath += "/" + request.Kind
	}
	if request.Continuation != "" {
		subpath += "?continuation=" + url.QueryEscape(request.Continuation)
	}
	var result forge.RefPage
	metadata, err := adapter.repositoryJSON(ctx, repository, subpath, request.Principal, &result)
	result.ResultMetadata = metadata
	return result, err
}

func (adapter *WalGitAdapter) SnapshotRefs(ctx context.Context, repository forge.Repository) (map[string]string, error) {
	result := make(map[string]string)
	for _, kind := range []string{"branches", "tags"} {
		page, err := adapter.Refs(ctx, repository, forge.RefsRequest{Kind: kind, Principal: repository.Coordinate.Owner.Hex()})
		if err != nil {
			return nil, err
		}
		for _, ref := range page.Refs {
			result[ref.Name] = ref.SHA
		}
		if page.More || page.Continuation != "" {
			return nil, &forge.Error{Status: http.StatusConflict, Code: "walgit_ref_snapshot_incomplete"}
		}
	}
	return result, nil
}

func (adapter *WalGitAdapter) Resolve(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.Revision, error) {
	revision := strings.TrimLeft(request.Revision, "/")
	if request.Path != "" {
		revision += "/" + strings.TrimLeft(request.Path, "/")
	}
	if _, ok := adapter.legacy.(*WalGitBackend); ok {
		var result forge.Revision
		metadata, err := adapter.repositoryJSON(ctx, repository, "resolve/"+revision, request.Principal, &result)
		result.ResultMetadata = metadata
		return result, err
	}
	sha, err := adapter.legacy.Resolve(ctx, legacyRepository(repository), revision, request.Principal)
	if err != nil {
		return forge.Revision{}, err
	}
	return forge.Revision{Ref: request.Revision, SHA: sha, Path: request.Path, Kind: "commit"}, nil
}

func (adapter *WalGitAdapter) Tree(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.Tree, error) {
	var result forge.Tree
	metadata, err := adapter.repositoryJSON(ctx, repository, revisionPath("tree", request), request.Principal, &result)
	result.ResultMetadata = metadata
	return result, err
}

func (adapter *WalGitAdapter) Blob(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.Blob, error) {
	var result forge.Blob
	metadata, err := adapter.repositoryJSON(ctx, repository, revisionPath("blob", request), request.Principal, &result)
	result.ResultMetadata = metadata
	return result, err
}

func (adapter *WalGitAdapter) Commits(ctx context.Context, repository forge.Repository, request forge.CommitsRequest) (forge.CommitPage, error) {
	query := make(url.Values)
	if request.Ref != "" {
		query.Set("ref", request.Ref)
	}
	if request.Path != "" {
		query.Set("path", request.Path)
	}
	if request.Skip > 0 {
		query.Set("skip", strconv.Itoa(request.Skip))
	}
	if request.Limit > 0 {
		query.Set("n", strconv.Itoa(request.Limit))
	}
	if request.Continuation != "" {
		query.Set("continuation", request.Continuation)
	}
	path := "commits"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var result forge.CommitPage
	metadata, err := adapter.repositoryJSON(ctx, repository, path, request.Principal, &result)
	result.ResultMetadata = metadata
	return result, err
}

func (adapter *WalGitAdapter) Commit(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.CommitDetail, error) {
	var result forge.CommitDetail
	metadata, err := adapter.repositoryJSON(ctx, repository, "commit/"+strings.TrimLeft(request.Revision, "/"), request.Principal, &result)
	result.ResultMetadata = metadata
	return result, err
}

func (adapter *WalGitAdapter) Compare(ctx context.Context, repository forge.Repository, request forge.CompareRequest) (forge.Diff, error) {
	result, err := adapter.legacy.Compare(ctx, legacyRepository(repository), CompareRequest{
		Base: request.Base, Head: request.Head, UseMergeBase: request.UseMergeBase,
		MaxFiles: request.MaxFiles, MaxPatchBytes: request.MaxPatchBytes, Principal: request.Principal,
	})
	if err != nil {
		return forge.Diff{}, err
	}
	files := make([]forge.DiffFile, 0, len(result.Files))
	for _, file := range result.Files {
		files = append(files, forge.DiffFile{
			Path: file.Path, OldPath: file.OldPath, Status: file.Status,
			Additions: file.Additions, Deletions: file.Deletions, Binary: file.Binary,
		})
	}
	return forge.Diff{Base: result.Base, Head: result.Head, MergeBase: result.MergeBase, Files: files, Patch: result.Patch}, nil
}

func (adapter *WalGitAdapter) repositoryJSON(ctx context.Context, repository forge.Repository, subpath, principal string, target any) (forge.ResultMetadata, error) {
	backend, ok := adapter.legacy.(*WalGitBackend)
	if !ok {
		recorder := &captureResponseWriter{header: make(http.Header), status: http.StatusOK}
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://walgit.invalid/"+strings.TrimLeft(subpath, "/"), nil)
		adapter.legacy.ProxyRepositoryAPI(recorder, request, legacyRepository(repository), subpath, principal)
		if recorder.status < 200 || recorder.status >= 300 {
			return forge.ResultMetadata{}, &forge.Error{Status: recorder.status, Code: repositoryErrorCode(recorder.status)}
		}
		if recorder.body.Len() == 0 {
			recorder.body.WriteString("{}")
		}
		if err := json.NewDecoder(io.LimitReader(&recorder.body, 72<<20)).Decode(target); err != nil {
			return forge.ResultMetadata{}, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_walgit_response"}
		}
		return forge.ResultMetadata{CacheControl: recorder.header.Get("Cache-Control"), ETag: recorder.header.Get("ETag"), LastModified: recorder.header.Get("Last-Modified")}, nil
	}
	response, err := backend.repositoryResponse(ctx, legacyRepository(repository), subpath, principal)
	if err != nil {
		return forge.ResultMetadata{}, err
	}
	defer response.Body.Close()
	metadata := forge.ResultMetadata{CacheControl: response.Header.Get("Cache-Control"), ETag: response.Header.Get("ETag"), LastModified: response.Header.Get("Last-Modified")}
	if response.Status < 200 || response.Status >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return metadata, &forge.Error{Status: response.Status, Code: repositoryErrorCode(response.Status)}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 72<<20)).Decode(target); err != nil {
		return metadata, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_walgit_response"}
	}
	return metadata, nil
}

type captureResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (writer *captureResponseWriter) Header() http.Header    { return writer.header }
func (writer *captureResponseWriter) WriteHeader(status int) { writer.status = status }
func (writer *captureResponseWriter) Write(value []byte) (int, error) {
	return writer.body.Write(value)
}

func repositoryErrorCode(status int) string {
	switch status {
	case http.StatusNotFound:
		return "git_object_not_found"
	case http.StatusRequestEntityTooLarge:
		return "git_result_too_large"
	default:
		return "walgit_request_failed"
	}
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
	_ forge.GitAuthorityBackend      = (*WalGitAdapter)(nil)
	_ forge.RepositoryContentBackend = (*WalGitAdapter)(nil)
	_ forge.RepositoryProvisioner    = (*WalGitAdapter)(nil)
	_ forge.RepositoryImporter       = (*WalGitAdapter)(nil)
	_ forge.RepositoryObjectImporter = (*WalGitAdapter)(nil)
	_ forge.GitReplicationSource     = (*WalGitAdapter)(nil)
	_ forge.CommitMirrorSource       = (*WalGitAdapter)(nil)
	_ forge.SmartHTTPBackend         = (*WalGitAdapter)(nil)
)

func (adapter *WalGitAdapter) String() string {
	return fmt.Sprintf("WalGitAdapter(%s)", adapter.Name())
}
