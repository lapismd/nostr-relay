package githubforge

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"zooid/internal/forge"
)

func (backend *Backend) Commits(ctx context.Context, repository forge.Repository, request forge.CommitsRequest) (forge.CommitPage, error) {
	if request.Continuation != "" {
		if request.Skip != 0 {
			return forge.CommitPage{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_continuation"}
		}
		skip, err := decodeContinuation(request.Continuation, "commits")
		if err != nil {
			return forge.CommitPage{}, err
		}
		request.Skip = skip
	}
	reference := request.Ref
	if reference == "" {
		reference = "HEAD"
	}
	resolved, err := backend.Resolve(ctx, repository, forge.RevisionRequest{Revision: reference, Principal: request.Principal})
	if err != nil {
		return forge.CommitPage{}, err
	}
	sha := resolved.SHA
	limit := request.Limit
	if limit <= 0 {
		limit = 35
	}
	if limit > 200 {
		limit = 200
	}
	wanted := request.Skip + limit + 1
	if wanted > 1000 {
		wanted = 1000
	}
	commits := make([]gitHubCommit, 0, wanted)
	for page := 1; len(commits) < wanted; page++ {
		perPage := min(100, wanted-len(commits))
		path := repoPath(repository) + "/commits?sha=" + url.QueryEscape(sha) + "&per_page=" + strconv.Itoa(perPage) + "&page=" + strconv.Itoa(page)
		if request.Path != "" {
			path += "&path=" + url.QueryEscape(request.Path)
		}
		response, err := backend.getRepository(ctx, repository, path, gitReadPermissions)
		if err != nil {
			return forge.CommitPage{}, err
		}
		var pageCommits []gitHubCommit
		if err := decodeAndClose(response, &pageCommits, 16<<20); err != nil {
			return forge.CommitPage{}, err
		}
		commits = append(commits, pageCommits...)
		if len(pageCommits) < perPage {
			break
		}
	}
	if request.Skip < len(commits) {
		commits = commits[request.Skip:]
	} else {
		commits = nil
	}
	more := len(commits) > limit
	if more {
		commits = commits[:limit]
	}
	normalized := make([]forge.Commit, 0, len(commits))
	for _, commit := range commits {
		normalized = append(normalized, normalizeCommit(commit))
	}
	result := forge.CommitPage{Ref: reference, SHA: sha, Commits: normalized, More: more, ResultMetadata: githubResultMetadata()}
	if more {
		result.Continuation = encodeContinuation("commits", request.Skip+limit)
	}
	return result, nil
}

func (backend *Backend) Commit(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.CommitDetail, error) {
	var commit gitHubCommit
	response, err := backend.getRepository(ctx, repository, repoPath(repository)+"/commits/"+escapePath(request.Revision)+"?per_page=100", gitReadPermissions)
	if err != nil {
		return forge.CommitDetail{}, err
	}
	if err := decodeAndClose(response, &commit, maxGitHubResponse); err != nil {
		return forge.CommitDetail{}, err
	}
	stats := make([]forge.CommitStat, 0, len(commit.Files))
	var patch strings.Builder
	for _, file := range commit.Files {
		stats = append(stats, forge.CommitStat{Path: file.Filename, Additions: file.Additions, Deletions: file.Deletions})
		appendPatch(&patch, file)
	}
	limits, _ := backend.ContentCapabilities(ctx, repository)
	result := forge.CommitDetail{Commit: normalizeCommit(commit), Stats: stats, Patch: patch.String(), ResultMetadata: githubResultMetadata()}
	result.CapabilityLimits = &limits.Limits
	if len(commit.Files) >= 300 {
		result.Truncated = true
		result.Incomplete = []string{"stats", "patch"}
		result.LimitExceeded = &forge.LimitExceeded{Resource: "changed_files", Limit: int64(limits.Limits.MaximumChangedFiles), Actual: int64(len(commit.Files))}
	}
	return result, nil
}

func (backend *Backend) Compare(ctx context.Context, repository forge.Repository, request forge.CompareRequest) (forge.Diff, error) {
	var comparison gitHubCompare
	path := repoPath(repository) + "/compare/" + escapePath(request.Base) + "..." + escapePath(request.Head) + "?per_page=100&page=1"
	response, err := backend.getRepository(ctx, repository, path, gitReadPermissions)
	if err != nil {
		return forge.Diff{}, err
	}
	if err := decodeAndClose(response, &comparison, maxGitHubResponse); err != nil {
		return forge.Diff{}, err
	}
	if len(comparison.Files) > request.MaxFiles {
		return forge.Diff{}, &forge.Error{Status: http.StatusRequestEntityTooLarge, Code: "diff_too_large"}
	}
	files := make([]forge.DiffFile, 0, len(comparison.Files))
	var patch strings.Builder
	for _, file := range comparison.Files {
		files = append(files, forge.DiffFile{
			Path: file.Filename, OldPath: file.PreviousFilename, Status: normalizeFileStatus(file.Status),
			Additions: file.Additions, Deletions: file.Deletions, Binary: file.Patch == "" && file.Changes > 0,
		})
		appendPatch(&patch, file)
		if int64(patch.Len()) > request.MaxPatchBytes {
			return forge.Diff{}, &forge.Error{Status: http.StatusRequestEntityTooLarge, Code: "diff_too_large"}
		}
	}
	limits, _ := backend.ContentCapabilities(ctx, repository)
	result := forge.Diff{
		Base: comparison.BaseCommit.SHA, Head: comparison.HeadCommit.SHA,
		MergeBase: comparison.MergeBaseCommit.SHA, Files: files, Patch: patch.String(), ResultMetadata: githubResultMetadata(),
	}
	if len(comparison.Files) >= 300 {
		result.Truncated = true
		result.Incomplete = []string{"changed-files", "patch"}
		result.LimitExceeded = &forge.LimitExceeded{Resource: "changed_files", Limit: int64(limits.Limits.MaximumChangedFiles), Actual: int64(len(comparison.Files))}
	}
	result.CapabilityLimits = &limits.Limits
	return result, nil
}
