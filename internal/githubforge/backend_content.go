package githubforge

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
	"zooid/internal/forge"
)

func (backend *Backend) Refs(ctx context.Context, repository forge.Repository, request forge.RefsRequest) (forge.RefPage, error) {
	if request.Kind == "" {
		branch, err := backend.DefaultBranch(ctx, repository, request.Principal)
		if err != nil {
			return forge.RefPage{}, err
		}
		var ref gitHubRef
		response, err := backend.getRepository(ctx, repository, repoPath(repository)+"/git/ref/heads/"+escapePath(branch), gitReadPermissions)
		if err != nil {
			return forge.RefPage{}, err
		}
		defer response.Body.Close()
		if err := decodeGitHub(response, &ref, 2<<20); err != nil {
			return forge.RefPage{}, err
		}
		normalized := normalizedRef(ref)
		return forge.RefPage{Head: &normalized, ResultMetadata: githubResultMetadata()}, nil
	}
	if request.Kind != "branches" && request.Kind != "tags" {
		return forge.RefPage{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_ref_kind"}
	}
	prefix := "heads/"
	if request.Kind == "tags" {
		prefix = "tags/"
	}
	page := 1
	if request.Continuation != "" {
		parsed, parseErr := decodeContinuation(request.Continuation, request.Kind)
		if parseErr != nil {
			return forge.RefPage{}, parseErr
		}
		page = parsed
	}
	response, err := backend.getRepository(ctx, repository, repoPath(repository)+"/git/matching-refs/"+prefix+"?per_page=100&page="+strconv.Itoa(page), gitReadPermissions)
	if err != nil {
		return forge.RefPage{}, err
	}
	defer response.Body.Close()
	var refs []gitHubRef
	if err := decodeGitHub(response, &refs, 8<<20); err != nil {
		return forge.RefPage{}, err
	}
	normalized := make([]forge.Ref, 0, len(refs))
	for _, ref := range refs {
		normalized = append(normalized, normalizedRef(ref))
	}
	result := forge.RefPage{Refs: normalized, More: len(refs) == 100, ResultMetadata: githubResultMetadata()}
	if result.More {
		result.Continuation = encodeContinuation(request.Kind, page+1)
	}
	return result, nil
}

func (backend *Backend) Resolve(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.Revision, error) {
	revision := request.Revision
	if revision == "" || revision == "HEAD" {
		var err error
		revision, err = backend.DefaultBranch(ctx, repository, request.Principal)
		if err != nil {
			return forge.Revision{}, err
		}
	}
	var commit gitHubCommit
	response, err := backend.getRepository(ctx, repository, repoPath(repository)+"/commits/"+escapePath(revision), gitReadPermissions)
	if err != nil {
		return forge.Revision{}, err
	}
	defer response.Body.Close()
	if err := decodeGitHub(response, &commit, 16<<20); err != nil {
		return forge.Revision{}, err
	}
	if !fullObjectID(commit.SHA) {
		return forge.Revision{}, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_response"}
	}
	return forge.Revision{Ref: request.Revision, SHA: strings.ToLower(commit.SHA), Path: request.Path, Kind: "commit", ResultMetadata: githubResultMetadata()}, nil
}

func (backend *Backend) Tree(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.Tree, error) {
	revision, commitSHA, path, err := backend.resolveAddress(ctx, repository, request)
	if err != nil {
		return forge.Tree{}, err
	}
	entries := make([]forge.TreeEntry, 0)
	truncated := false
	if path == "" {
		var commit gitHubCommit
		response, err := backend.getRepository(ctx, repository, repoPath(repository)+"/commits/"+escapePath(commitSHA), gitReadPermissions)
		if err != nil {
			return forge.Tree{}, err
		}
		if err := decodeAndClose(response, &commit, 16<<20); err != nil {
			return forge.Tree{}, err
		}
		var tree gitHubTree
		response, err = backend.getRepository(ctx, repository, repoPath(repository)+"/git/trees/"+escapePath(commit.Commit.Tree.SHA), gitReadPermissions)
		if err != nil {
			return forge.Tree{}, err
		}
		if err := decodeAndClose(response, &tree, 16<<20); err != nil {
			return forge.Tree{}, err
		}
		truncated = tree.Truncated
		for _, item := range tree.Tree {
			entries = append(entries, forge.TreeEntry{Name: item.Path, Type: normalizeGitHubTreeType(item.Type), Mode: item.Mode, Size: item.Size, SHA: item.SHA})
		}
	} else {
		var contents []gitHubContent
		response, err := backend.getRepository(ctx, repository, repoPath(repository)+"/contents/"+escapePath(path)+"?ref="+url.QueryEscape(commitSHA), gitReadPermissions)
		if err != nil {
			return forge.Tree{}, err
		}
		if err := decodeAndClose(response, &contents, 16<<20); err != nil {
			return forge.Tree{}, err
		}
		truncated = len(contents) >= 1000
		for _, item := range contents {
			entries = append(entries, forge.TreeEntry{Name: item.Name, Type: normalizeContentType(item.Type), Mode: contentMode(item.Type), Size: item.Size, SHA: item.SHA})
		}
	}
	limits, _ := backend.ContentCapabilities(ctx, repository)
	result := forge.Tree{Ref: revision, SHA: commitSHA, Path: path, Entries: entries, ResultMetadata: githubResultMetadata()}
	if truncated {
		result.Truncated = true
		result.Incomplete = []string{"entries"}
		result.LimitExceeded = &forge.LimitExceeded{Resource: "tree_entries", Limit: int64(limits.Limits.MaximumTreeEntries), Actual: int64(len(entries))}
	}
	result.CapabilityLimits = &limits.Limits
	return result, nil
}

func (backend *Backend) Blob(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.Blob, error) {
	revision, commitSHA, path, err := backend.resolveAddress(ctx, repository, request)
	if err != nil {
		return forge.Blob{}, err
	}
	if path == "" {
		return forge.Blob{}, &forge.Error{Status: http.StatusNotFound, Code: "github_object_not_found"}
	}
	var content gitHubContent
	response, err := backend.getRepository(ctx, repository, repoPath(repository)+"/contents/"+escapePath(path)+"?ref="+url.QueryEscape(commitSHA), gitReadPermissions)
	if err != nil {
		return forge.Blob{}, err
	}
	if err := decodeAndClose(response, &content, maxGitHubResponse); err != nil {
		return forge.Blob{}, err
	}
	limits, _ := backend.ContentCapabilities(ctx, repository)
	result := forge.Blob{Ref: revision, SHA: commitSHA, Path: path, Name: content.Name, Size: content.Size, ResultMetadata: githubResultMetadata()}
	result.CapabilityLimits = &limits.Limits
	if content.Size > maxBrowserBlob {
		result.TooLarge = true
		result.LimitExceeded = &forge.LimitExceeded{Resource: "browser_blob_bytes", Limit: maxBrowserBlob, Actual: content.Size}
		return result, nil
	}
	raw, err := decodeContent(content.Content, content.Encoding)
	if err != nil {
		return forge.Blob{}, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_response"}
	}
	if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
		result.Binary = true
	} else {
		result.Contents = string(raw)
	}
	return result, nil
}

func fullObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

type gitHubRef struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

func normalizedRef(ref gitHubRef) forge.Ref {
	return forge.Ref{Name: ref.Ref, SHA: strings.ToLower(ref.Object.SHA)}
}
