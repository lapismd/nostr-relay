package githubforge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
)

const (
	maxGitHubResponse = 32 << 20
	maxBrowserBlob    = 2 << 20
)

var gitReadPermissions = map[string]string{"metadata": "read", "contents": "read"}
var gitWritePermissions = map[string]string{"metadata": "read", "contents": "write"}

type Backend struct {
	connections ConnectionLoader
	credentials CredentialStore
	userTokens  UserTokenResolver
	client      *http.Client
	base        *url.URL
	tokens      *tokenManager
	reads       *githubReadCache
}

// UserTokenResolver returns an OAuth token for the Nostr author selected when
// a durable forge operation was enqueued.
type UserTokenResolver interface {
	ResolveGitHubUserToken(context.Context, nostr.PubKey) (string, error)
	MarkGitHubUserReconnectRequired(context.Context, nostr.PubKey) error
}

func (backend *Backend) SetUserTokenResolver(resolver UserTokenResolver) {
	backend.userTokens = resolver
}

func New(connections ConnectionLoader, credentials CredentialStore, client *http.Client) (*Backend, error) {
	base, _ := url.Parse(APIBaseURL)
	return newBackend(connections, credentials, client, base, false)
}

func newBackend(connections ConnectionLoader, credentials CredentialStore, client *http.Client, base *url.URL, allowTestHost bool) (*Backend, error) {
	if connections == nil || credentials == nil {
		return nil, fmt.Errorf("GitHub backend requires connection and credential stores")
	}
	if base == nil || base.Scheme == "" || base.Host == "" || (!allowTestHost && base.String() != APIBaseURL) {
		return nil, fmt.Errorf("GitHub API base must be %s", APIBaseURL)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Backend{
		connections: connections, credentials: credentials, client: client, base: base,
		tokens: newTokenManager(credentials, client, base), reads: newGitHubReadCache(),
	}, nil
}

func (*Backend) Name() string { return forge.BackendGitHub }

type RepositoryMetadata struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	HTMLURL       string `json:"html_url"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Disabled      bool   `json:"disabled"`
	Owner         struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	} `json:"owner"`
}

func (backend *Backend) AuthorityCapabilities(ctx context.Context, repository forge.Repository) (forge.GitAuthorityCapabilities, error) {
	descriptor, err := backend.CloneDescriptor(ctx, repository)
	if err != nil {
		return forge.GitAuthorityCapabilities{}, err
	}
	return forge.GitAuthorityCapabilities{Clone: descriptor, RefEvents: true}, nil
}

func (*Backend) ContentCapabilities(context.Context, forge.Repository) (forge.ContentCapabilities, error) {
	return forge.ContentCapabilities{Browse: true, Compare: true, Limits: forge.ContentLimits{
		MaximumBlobBytes: 100 << 20, MaximumChangedFiles: 300, MaximumTreeEntries: 1000,
		MayTruncateCompare: true, MayTruncateTree: true,
	}}, nil
}

func (backend *Backend) Capabilities(ctx context.Context, repository forge.Repository) (forge.Capabilities, error) {
	authority, err := backend.AuthorityCapabilities(ctx, repository)
	if err != nil {
		return forge.Capabilities{}, err
	}
	content, _ := backend.ContentCapabilities(ctx, repository)
	return forge.Capabilities{
		GitBackend: forge.BackendGitHub, GitAuthorityBackend: forge.BackendGitHub,
		ContentBackend: forge.BackendGitHub, Clone: authority.Clone,
		Browse: content.Browse, Compare: content.Compare, RefEvents: authority.RefEvents,
		Issues: true, PullRequests: true, Reviews: true, Merge: true,
		Limits: content.Limits, CapabilityLimits: content.Limits,
	}, nil
}

func (backend *Backend) CloneDescriptor(_ context.Context, repository forge.Repository) (forge.CloneDescriptor, error) {
	fullName, err := repositoryName(repository)
	if err != nil {
		return forge.CloneDescriptor{}, err
	}
	return forge.CloneDescriptor{
		Available: true, URL: "https://github.com/" + fullName + ".git",
		Auth: forge.CloneAuthProvider, Provider: forge.BackendGitHub,
	}, nil
}

func (backend *Backend) DefaultBranch(ctx context.Context, repository forge.Repository, _ string) (string, error) {
	metadata, err := backend.Repository(ctx, repository)
	if err != nil {
		return "", err
	}
	if metadata.DefaultBranch == "" {
		return "", &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_repository"}
	}
	return metadata.DefaultBranch, nil
}

func (backend *Backend) Repository(ctx context.Context, repository forge.Repository) (RepositoryMetadata, error) {
	repositoryID, err := repositoryID(repository)
	if err != nil {
		return RepositoryMetadata{}, err
	}
	return backend.RepositoryByID(ctx, repository.Integration.ConnectionID, repositoryID)
}

func (backend *Backend) RepositoryByID(ctx context.Context, connectionID string, repositoryID int64) (RepositoryMetadata, error) {
	var metadata RepositoryMetadata
	connection, found, err := backend.connections.LoadGitHubConnection(ctx, connectionID)
	if err != nil {
		return metadata, err
	}
	if !found || validateConnection(connection) != nil || connection.Status != "active" {
		return metadata, &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_connection_unavailable", Retryable: true}
	}
	response, err := backend.getRepositoryWithConnection(ctx, connection, repositoryID, "/repositories/"+strconv.FormatInt(repositoryID, 10), gitReadPermissions)
	if err != nil {
		return metadata, err
	}
	defer response.Body.Close()
	if err := decodeGitHub(response, &metadata, 2<<20); err != nil {
		return metadata, err
	}
	if metadata.ID != repositoryID || !validRepositoryName(metadata.FullName) {
		return metadata, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_repository"}
	}
	return metadata, nil
}

func (backend *Backend) ValidateConnection(ctx context.Context, connection Connection) (Connection, error) {
	if err := validateConnection(connection); err != nil {
		return Connection{}, err
	}
	jwt, err := backend.tokens.appJWT(connection)
	if err != nil {
		return Connection{}, err
	}
	target := *backend.base
	target.Path = "/app/installations/" + strconv.FormatInt(connection.InstallationID, 10)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Connection{}, err
	}
	setGitHubHeaders(request, jwt)
	response, err := backend.client.Do(request)
	if err != nil {
		return Connection{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_unavailable", Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Connection{}, githubStatusError(response.StatusCode, response.Header)
	}
	var installation struct {
		ID      int64 `json:"id"`
		Account struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"account"`
		SuspendedAt *time.Time `json:"suspended_at"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&installation); err != nil || installation.ID != connection.InstallationID || installation.SuspendedAt != nil {
		return Connection{}, &forge.Error{Status: http.StatusConflict, Code: "github_installation_unavailable"}
	}
	connection.AccountID = strconv.FormatInt(installation.Account.ID, 10)
	connection.AccountLogin = installation.Account.Login
	connection.Host = "github.com"
	connection.Status = "active"
	return connection, nil
}

func (backend *Backend) ListRepositories(ctx context.Context, connection Connection) ([]RepositoryMetadata, error) {
	if err := validateConnection(connection); err != nil {
		return nil, err
	}
	// This administrative discovery call is the single exception to per-repo
	// narrowing: the installation repository list is needed before attachment.
	token, err := backend.tokens.installation(ctx, connection, 0, map[string]string{"metadata": "read"})
	if err != nil {
		return nil, err
	}
	results := make([]RepositoryMetadata, 0)
	for page := 1; page <= 10; page++ {
		target := *backend.base
		target.Path = "/installation/repositories"
		query := target.Query()
		query.Set("per_page", "100")
		query.Set("page", strconv.Itoa(page))
		target.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return nil, err
		}
		setGitHubHeaders(request, token)
		response, err := backend.client.Do(request)
		if err != nil {
			return nil, &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_unavailable", Retryable: true}
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, githubStatusError(response.StatusCode, response.Header)
		}
		var pageResult struct {
			Repositories []RepositoryMetadata `json:"repositories"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&pageResult)
		response.Body.Close()
		if err != nil {
			return nil, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_response"}
		}
		results = append(results, pageResult.Repositories...)
		if len(pageResult.Repositories) < 100 {
			break
		}
	}
	return results, nil
}

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

func (backend *Backend) resolveAddress(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (string, string, string, error) {
	address := strings.Trim(request.Revision, "/")
	if request.Path != "" {
		address += "/" + strings.Trim(request.Path, "/")
	}
	if len(address) > 4096 {
		return "", "", "", &forge.Error{Status: http.StatusBadRequest, Code: "invalid_revision"}
	}
	if resolved, err := backend.Resolve(ctx, repository, forge.RevisionRequest{Revision: address, Principal: request.Principal}); err == nil {
		return address, resolved.SHA, "", nil
	}
	for index := strings.LastIndex(address, "/"); index > 0; index = strings.LastIndex(address[:index], "/") {
		revision, path := address[:index], address[index+1:]
		resolved, err := backend.Resolve(ctx, repository, forge.RevisionRequest{Revision: revision, Principal: request.Principal})
		if err == nil {
			return revision, resolved.SHA, path, nil
		}
	}
	return "", "", "", &forge.Error{Status: http.StatusNotFound, Code: "github_object_not_found"}
}

func (backend *Backend) doRepository(ctx context.Context, repository forge.Repository, method, path string, body io.Reader, permissions map[string]string) (*http.Response, error) {
	connection, repositoryID, err := backend.connectionFor(ctx, repository)
	if err != nil {
		return nil, err
	}
	response, err := backend.doRepositoryWithConnection(ctx, connection, repositoryID, method, path, body, permissions)
	if err == nil && method != http.MethodGet {
		backend.reads.invalidate(connection.ID, repositoryID)
	}
	return response, err
}

func (backend *Backend) doRepositoryForActor(ctx context.Context, repository forge.Repository, actor nostr.PubKey, credentialMode, method, path string, body io.Reader, permissions map[string]string) (*http.Response, error) {
	if credentialMode == "" || credentialMode == forge.CredentialSystem {
		return backend.doRepository(ctx, repository, method, path, body, permissions)
	}
	if credentialMode != forge.CredentialUser || actor == nostr.ZeroPK || backend.userTokens == nil {
		return nil, &forge.Error{Status: http.StatusUnauthorized, Code: "github_user_reconnect_required"}
	}
	connection, repositoryID, err := backend.connectionFor(ctx, repository)
	if err != nil {
		return nil, err
	}
	token, err := backend.userTokens.ResolveGitHubUserToken(ctx, actor)
	if errors.Is(err, forge.ErrGitHubUserReconnectRequired) || err == nil && token == "" {
		return nil, &forge.Error{Status: http.StatusUnauthorized, Code: "github_user_reconnect_required"}
	}
	if err != nil {
		return nil, &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_user_credential_unavailable", Retryable: true}
	}
	target := *backend.base
	pathPart, rawQuery, _ := strings.Cut(path, "?")
	target.Path = pathPart
	target.RawQuery = rawQuery
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	setGitHubHeaders(request, token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := backend.client.Do(request)
	if err != nil {
		return nil, &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_unavailable", Retryable: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status := response.StatusCode
		response.Body.Close()
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			_ = backend.userTokens.MarkGitHubUserReconnectRequired(ctx, actor)
			return nil, &forge.Error{Status: http.StatusUnauthorized, Code: "github_user_reconnect_required"}
		}
		return nil, githubStatusError(status, response.Header)
	}
	if method != http.MethodGet {
		backend.reads.invalidate(connection.ID, repositoryID)
	}
	return response, nil
}

func (backend *Backend) getRepository(ctx context.Context, repository forge.Repository, path string, permissions map[string]string) (*http.Response, error) {
	connection, repositoryID, err := backend.connectionFor(ctx, repository)
	if err != nil {
		return nil, err
	}
	return backend.getRepositoryWithConnection(ctx, connection, repositoryID, path, permissions)
}

func (backend *Backend) getRepositoryWithConnection(ctx context.Context, connection Connection, repositoryID int64, path string, permissions map[string]string) (*http.Response, error) {
	key := githubReadCacheKey{
		githubReadCacheScope: githubReadCacheScope{connectionID: connection.ID, repositoryID: repositoryID},
		path:                 path,
	}
	return backend.reads.load(ctx, key, func() (*http.Response, error) {
		return backend.doRepositoryWithConnection(ctx, connection, repositoryID, http.MethodGet, path, nil, permissions)
	})
}

func (backend *Backend) doRepositoryWithConnection(ctx context.Context, connection Connection, repositoryID int64, method, path string, body io.Reader, permissions map[string]string) (*http.Response, error) {
	token, err := backend.tokens.installation(ctx, connection, repositoryID, permissions)
	if err != nil {
		return nil, err
	}
	target := *backend.base
	pathPart, rawQuery, _ := strings.Cut(path, "?")
	target.Path = pathPart
	target.RawQuery = rawQuery
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	setGitHubHeaders(request, token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := backend.client.Do(request)
	if err != nil {
		return nil, &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_unavailable", Retryable: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, githubStatusError(response.StatusCode, response.Header)
	}
	return response, nil
}

func (backend *Backend) connectionFor(ctx context.Context, repository forge.Repository) (Connection, int64, error) {
	if _, err := repositoryName(repository); err != nil {
		return Connection{}, 0, err
	}
	connection, found, err := backend.connections.LoadGitHubConnection(ctx, repository.Integration.ConnectionID)
	if err != nil {
		return Connection{}, 0, err
	}
	if !found || connection.Status != "active" || connection.Host != "github.com" {
		return Connection{}, 0, &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_connection_unavailable", Retryable: true}
	}
	if err := validateConnection(connection); err != nil {
		return Connection{}, 0, err
	}
	repositoryID, err := repositoryID(repository)
	return connection, repositoryID, err
}

func validateConnection(connection Connection) error {
	if connection.ID == "" || connection.Host != "github.com" || connection.AppID == "" || connection.InstallationID <= 0 || connection.CredentialRef == "" {
		return &forge.Error{Status: http.StatusConflict, Code: "invalid_github_connection"}
	}
	return nil
}

func repositoryID(repository forge.Repository) (int64, error) {
	id, err := strconv.ParseInt(repository.Integration.ExternalRepositoryID, 10, 64)
	if err != nil || id <= 0 {
		return 0, &forge.Error{Status: http.StatusConflict, Code: "invalid_github_repository"}
	}
	return id, nil
}

func repositoryName(repository forge.Repository) (string, error) {
	name := repository.Integration.ExternalRepository
	if !validRepositoryName(name) {
		return "", &forge.Error{Status: http.StatusConflict, Code: "invalid_github_repository"}
	}
	return name, nil
}

func validRepositoryName(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 100 {
			return false
		}
		for _, character := range part {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.') {
				return false
			}
		}
	}
	return true
}

func repoPath(repository forge.Repository) string {
	name, _ := repositoryName(repository)
	parts := strings.Split(name, "/")
	return "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
}

func escapePath(value string) string {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

func decodeGitHub(response *http.Response, target any, limit int64) error {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return githubStatusError(response.StatusCode, response.Header)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, limit))
	if err := decoder.Decode(target); err != nil {
		return &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_response"}
	}
	return nil
}

func decodeAndClose(response *http.Response, target any, limit int64) error {
	defer response.Body.Close()
	return decodeGitHub(response, target, limit)
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

type gitHubCommit struct {
	SHA     string `json:"sha"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Date  string `json:"date"`
		} `json:"author"`
		Committer struct {
			Name string `json:"name"`
			Date string `json:"date"`
		} `json:"committer"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	} `json:"commit"`
	Files []gitHubFile `json:"files"`
}

type gitHubFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int64  `json:"additions"`
	Deletions        int64  `json:"deletions"`
	Changes          int64  `json:"changes"`
	Patch            string `json:"patch"`
}

type gitHubCompare struct {
	BaseCommit      gitHubCommit `json:"base_commit"`
	HeadCommit      gitHubCommit `json:"head_commit"`
	MergeBaseCommit gitHubCommit `json:"merge_base_commit"`
	Files           []gitHubFile `json:"files"`
}

type gitHubTree struct {
	Truncated bool `json:"truncated"`
	Tree      []struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		Size int64  `json:"size"`
		SHA  string `json:"sha"`
	} `json:"tree"`
}

type gitHubContent struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	SHA      string `json:"sha"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

func normalizeCommit(commit gitHubCommit) forge.Commit {
	subject, body, _ := strings.Cut(commit.Commit.Message, "\n")
	parents := make([]string, 0, len(commit.Parents))
	for _, parent := range commit.Parents {
		parents = append(parents, parent.SHA)
	}
	return forge.Commit{
		SHA: commit.SHA, Parents: parents, Author: commit.Commit.Author.Name,
		AuthorEmail: commit.Commit.Author.Email, AuthorDate: commit.Commit.Author.Date,
		Committer: commit.Commit.Committer.Name, CommitDate: commit.Commit.Committer.Date,
		Subject: subject, Body: strings.TrimSpace(body), Trailers: []any{},
	}
}

func encodeContinuation(kind string, value int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + strconv.Itoa(value)))
}

func decodeContinuation(token, kind string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) > 64 {
		return 0, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_continuation"}
	}
	prefix := kind + ":"
	if !strings.HasPrefix(string(raw), prefix) {
		return 0, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_continuation"}
	}
	value, err := strconv.Atoi(strings.TrimPrefix(string(raw), prefix))
	if err != nil || value < 0 || value > 10000 {
		return 0, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_continuation"}
	}
	return value, nil
}

func normalizeGitHubTreeType(kind string) string {
	if kind == "tree" {
		return "tree"
	}
	return "blob"
}

func normalizeContentType(kind string) string {
	if kind == "dir" {
		return "tree"
	}
	return "blob"
}

func contentMode(kind string) string {
	if kind == "dir" {
		return "040000"
	}
	if kind == "symlink" {
		return "120000"
	}
	if kind == "submodule" {
		return "160000"
	}
	return "100644"
}

func normalizeFileStatus(status string) string {
	switch status {
	case "added", "removed", "modified", "renamed", "copied", "changed", "unchanged":
		if status == "removed" {
			return "deleted"
		}
		if status == "changed" {
			return "type_changed"
		}
		return status
	default:
		return "unknown"
	}
}

func appendPatch(builder *strings.Builder, file gitHubFile) {
	if file.Patch == "" {
		return
	}
	oldPath := file.PreviousFilename
	if oldPath == "" {
		oldPath = file.Filename
	}
	builder.WriteString("diff --git a/")
	builder.WriteString(oldPath)
	builder.WriteString(" b/")
	builder.WriteString(file.Filename)
	builder.WriteByte('\n')
	builder.WriteString(file.Patch)
	if !strings.HasSuffix(file.Patch, "\n") {
		builder.WriteByte('\n')
	}
}

func decodeContent(content, encoding string) ([]byte, error) {
	if encoding == "" && content == "" {
		return []byte{}, nil
	}
	if encoding != "base64" {
		return nil, fmt.Errorf("unsupported content encoding")
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(content, "\n", ""))
}

var (
	_ forge.GitAuthorityBackend      = (*Backend)(nil)
	_ forge.RepositoryContentBackend = (*Backend)(nil)
)
