package githubforge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"zooid/internal/forge"
)

const (
	maxGitHubResponse = 32 << 20
	maxBrowserBlob    = 2 << 20
)

var gitReadPermissions = map[string]string{"metadata": "read", "contents": "read"}

type Backend struct {
	connections ConnectionLoader
	credentials CredentialStore
	client      *http.Client
	base        *url.URL
	tokens      *tokenManager
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
		tokens: newTokenManager(credentials, client, base),
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

func (backend *Backend) Capabilities(ctx context.Context, repository forge.Repository) (forge.Capabilities, error) {
	descriptor, err := backend.CloneDescriptor(ctx, repository)
	if err != nil {
		return forge.Capabilities{}, err
	}
	return forge.Capabilities{
		GitBackend: forge.BackendGitHub, Clone: descriptor,
		Browse: true, Compare: true, RefEvents: true,
		Issues: true, PullRequests: true, Reviews: true, Merge: true,
		Limits: forge.Limits{
			MaximumBlobBytes: 100 << 20, MaximumChangedFiles: 300, MaximumTreeEntries: 1000,
			MayTruncateCompare: true, MayTruncateTree: true,
		},
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
	token, err := backend.tokens.installation(ctx, connection, repositoryID, gitReadPermissions)
	if err != nil {
		return metadata, err
	}
	target := *backend.base
	target.Path = "/repositories/" + strconv.FormatInt(repositoryID, 10)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return metadata, err
	}
	setGitHubHeaders(request, token)
	response, err := backend.client.Do(request)
	if err != nil {
		return metadata, &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_unavailable", Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return metadata, githubStatusError(response.StatusCode, response.Header)
	}
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

func (backend *Backend) Refs(ctx context.Context, repository forge.Repository, request forge.RefsRequest) (forge.Response, error) {
	if request.Kind == "" {
		branch, err := backend.DefaultBranch(ctx, repository, request.Principal)
		if err != nil {
			return forge.Response{}, err
		}
		var ref gitHubRef
		response, err := backend.doRepository(ctx, repository, http.MethodGet, repoPath(repository)+"/git/ref/heads/"+escapePath(branch), nil, gitReadPermissions)
		if err != nil {
			return forge.Response{}, err
		}
		defer response.Body.Close()
		if err := decodeGitHub(response, &ref, 2<<20); err != nil {
			return forge.Response{}, err
		}
		return jsonResponse(http.StatusOK, map[string]any{"head": normalizedRef(ref)}), nil
	}
	if request.Kind != "branches" && request.Kind != "tags" {
		return forge.Response{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_ref_kind"}
	}
	prefix := "heads/"
	if request.Kind == "tags" {
		prefix = "tags/"
	}
	response, err := backend.doRepository(ctx, repository, http.MethodGet, repoPath(repository)+"/git/matching-refs/"+prefix+"?per_page=100", nil, gitReadPermissions)
	if err != nil {
		return forge.Response{}, err
	}
	defer response.Body.Close()
	var refs []gitHubRef
	if err := decodeGitHub(response, &refs, 8<<20); err != nil {
		return forge.Response{}, err
	}
	normalized := make([]map[string]string, 0, len(refs))
	for _, ref := range refs {
		normalized = append(normalized, normalizedRef(ref))
	}
	return jsonResponse(http.StatusOK, map[string]any{"refs": normalized, "more": len(refs) == 100}), nil
}

func (backend *Backend) Resolve(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (string, error) {
	revision := request.Revision
	if revision == "" || revision == "HEAD" {
		var err error
		revision, err = backend.DefaultBranch(ctx, repository, request.Principal)
		if err != nil {
			return "", err
		}
	}
	var commit gitHubCommit
	response, err := backend.doRepository(ctx, repository, http.MethodGet, repoPath(repository)+"/commits/"+escapePath(revision), nil, gitReadPermissions)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if err := decodeGitHub(response, &commit, 16<<20); err != nil {
		return "", err
	}
	if !fullObjectID(commit.SHA) {
		return "", &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_response"}
	}
	return strings.ToLower(commit.SHA), nil
}

func (backend *Backend) Tree(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.Response, error) {
	revision, commitSHA, path, err := backend.resolveAddress(ctx, repository, request)
	if err != nil {
		return forge.Response{}, err
	}
	entries := make([]treeEntry, 0)
	truncated := false
	if path == "" {
		var commit gitHubCommit
		response, err := backend.doRepository(ctx, repository, http.MethodGet, repoPath(repository)+"/commits/"+escapePath(commitSHA), nil, gitReadPermissions)
		if err != nil {
			return forge.Response{}, err
		}
		if err := decodeAndClose(response, &commit, 16<<20); err != nil {
			return forge.Response{}, err
		}
		var tree gitHubTree
		response, err = backend.doRepository(ctx, repository, http.MethodGet, repoPath(repository)+"/git/trees/"+escapePath(commit.Commit.Tree.SHA), nil, gitReadPermissions)
		if err != nil {
			return forge.Response{}, err
		}
		if err := decodeAndClose(response, &tree, 16<<20); err != nil {
			return forge.Response{}, err
		}
		truncated = tree.Truncated
		for _, item := range tree.Tree {
			entries = append(entries, treeEntry{Name: item.Path, Type: normalizeGitHubTreeType(item.Type), Mode: item.Mode, Size: item.Size, SHA: item.SHA})
		}
	} else {
		var contents []gitHubContent
		response, err := backend.doRepository(ctx, repository, http.MethodGet, repoPath(repository)+"/contents/"+escapePath(path)+"?ref="+url.QueryEscape(commitSHA), nil, gitReadPermissions)
		if err != nil {
			return forge.Response{}, err
		}
		if err := decodeAndClose(response, &contents, 16<<20); err != nil {
			return forge.Response{}, err
		}
		truncated = len(contents) >= 1000
		for _, item := range contents {
			entries = append(entries, treeEntry{Name: item.Name, Type: normalizeContentType(item.Type), Mode: contentMode(item.Type), Size: item.Size, SHA: item.SHA})
		}
	}
	result := treeResult{Ref: revision, SHA: commitSHA, Path: path, Entries: entries}
	response := jsonResponse(http.StatusOK, result)
	if truncated {
		response.Header.Set("X-Lapis-Truncated", "tree")
	}
	return response, nil
}

func (backend *Backend) Blob(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.Response, error) {
	revision, commitSHA, path, err := backend.resolveAddress(ctx, repository, request)
	if err != nil {
		return forge.Response{}, err
	}
	if path == "" {
		return forge.Response{}, &forge.Error{Status: http.StatusNotFound, Code: "github_object_not_found"}
	}
	var content gitHubContent
	response, err := backend.doRepository(ctx, repository, http.MethodGet, repoPath(repository)+"/contents/"+escapePath(path)+"?ref="+url.QueryEscape(commitSHA), nil, gitReadPermissions)
	if err != nil {
		return forge.Response{}, err
	}
	if err := decodeAndClose(response, &content, maxGitHubResponse); err != nil {
		return forge.Response{}, err
	}
	result := blobResult{Ref: revision, SHA: commitSHA, Path: path, Name: content.Name, Size: content.Size}
	if content.Size > maxBrowserBlob {
		result.TooLarge = true
		return jsonResponse(http.StatusOK, result), nil
	}
	raw, err := decodeContent(content.Content, content.Encoding)
	if err != nil {
		return forge.Response{}, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_response"}
	}
	if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
		result.Binary = true
	} else {
		result.Contents = string(raw)
	}
	return jsonResponse(http.StatusOK, result), nil
}

func (backend *Backend) Commits(ctx context.Context, repository forge.Repository, request forge.CommitsRequest) (forge.Response, error) {
	reference := request.Ref
	if reference == "" {
		reference = "HEAD"
	}
	sha, err := backend.Resolve(ctx, repository, forge.RevisionRequest{Revision: reference, Principal: request.Principal})
	if err != nil {
		return forge.Response{}, err
	}
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
		response, err := backend.doRepository(ctx, repository, http.MethodGet, path, nil, gitReadPermissions)
		if err != nil {
			return forge.Response{}, err
		}
		var pageCommits []gitHubCommit
		if err := decodeAndClose(response, &pageCommits, 16<<20); err != nil {
			return forge.Response{}, err
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
	normalized := make([]commitResult, 0, len(commits))
	for _, commit := range commits {
		normalized = append(normalized, normalizeCommit(commit))
	}
	return jsonResponse(http.StatusOK, commitsResult{Ref: reference, SHA: sha, Commits: normalized, More: more}), nil
}

func (backend *Backend) Commit(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (forge.Response, error) {
	var commit gitHubCommit
	response, err := backend.doRepository(ctx, repository, http.MethodGet, repoPath(repository)+"/commits/"+escapePath(request.Revision)+"?per_page=100", nil, gitReadPermissions)
	if err != nil {
		return forge.Response{}, err
	}
	if err := decodeAndClose(response, &commit, maxGitHubResponse); err != nil {
		return forge.Response{}, err
	}
	stats := make([]commitStat, 0, len(commit.Files))
	var patch strings.Builder
	for _, file := range commit.Files {
		stats = append(stats, commitStat{Path: file.Filename, Additions: file.Additions, Deletions: file.Deletions})
		appendPatch(&patch, file)
	}
	result := commitDetail{Commit: normalizeCommit(commit), Stats: stats, Patch: patch.String()}
	responseResult := jsonResponse(http.StatusOK, result)
	if len(commit.Files) >= 300 {
		responseResult.Header.Set("X-Lapis-Truncated", "changed-files")
	}
	return responseResult, nil
}

func (backend *Backend) Compare(ctx context.Context, repository forge.Repository, request forge.CompareRequest) (forge.CompareResult, error) {
	var comparison gitHubCompare
	path := repoPath(repository) + "/compare/" + escapePath(request.Base) + "..." + escapePath(request.Head) + "?per_page=100&page=1"
	response, err := backend.doRepository(ctx, repository, http.MethodGet, path, nil, gitReadPermissions)
	if err != nil {
		return forge.CompareResult{}, err
	}
	if err := decodeAndClose(response, &comparison, maxGitHubResponse); err != nil {
		return forge.CompareResult{}, err
	}
	if len(comparison.Files) > request.MaxFiles {
		return forge.CompareResult{}, &forge.Error{Status: http.StatusRequestEntityTooLarge, Code: "diff_too_large"}
	}
	files := make([]forge.CompareFile, 0, len(comparison.Files))
	var patch strings.Builder
	for _, file := range comparison.Files {
		files = append(files, forge.CompareFile{
			Path: file.Filename, OldPath: file.PreviousFilename, Status: normalizeFileStatus(file.Status),
			Additions: file.Additions, Deletions: file.Deletions, Binary: file.Patch == "" && file.Changes > 0,
		})
		appendPatch(&patch, file)
		if int64(patch.Len()) > request.MaxPatchBytes {
			return forge.CompareResult{}, &forge.Error{Status: http.StatusRequestEntityTooLarge, Code: "diff_too_large"}
		}
	}
	result := forge.CompareResult{
		Base: comparison.BaseCommit.SHA, Head: comparison.HeadCommit.SHA,
		MergeBase: comparison.MergeBaseCommit.SHA, Files: files, Patch: patch.String(),
	}
	if len(comparison.Files) >= 300 {
		result.Truncated = true
		result.Incomplete = []string{"changed-files", "patch"}
	}
	return result, nil
}

func (backend *Backend) ServeRepositoryAPI(w http.ResponseWriter, r *http.Request, repository forge.Repository, subpath, principal string) {
	var response forge.Response
	var err error
	query := r.URL.Query()
	switch {
	case subpath == "" || subpath == "refs":
		response, err = backend.Refs(r.Context(), repository, forge.RefsRequest{Principal: principal})
	case subpath == "refs/branches":
		response, err = backend.Refs(r.Context(), repository, forge.RefsRequest{Kind: "branches", Principal: principal})
	case subpath == "refs/tags":
		response, err = backend.Refs(r.Context(), repository, forge.RefsRequest{Kind: "tags", Principal: principal})
	case subpath == "resolve" || subpath == "resolve/":
		response, err = backend.resolveResponse(r.Context(), repository, "HEAD", principal)
	case strings.HasPrefix(subpath, "resolve/"):
		response, err = backend.resolveResponse(r.Context(), repository, strings.TrimPrefix(subpath, "resolve/"), principal)
	case strings.HasPrefix(subpath, "tree/"):
		response, err = backend.Tree(r.Context(), repository, forge.RevisionRequest{Revision: strings.TrimPrefix(subpath, "tree/"), Principal: principal})
	case strings.HasPrefix(subpath, "blob/"):
		response, err = backend.Blob(r.Context(), repository, forge.RevisionRequest{Revision: strings.TrimPrefix(subpath, "blob/"), Principal: principal})
	case subpath == "commits":
		skip, _ := strconv.Atoi(query.Get("skip"))
		limit, _ := strconv.Atoi(query.Get("n"))
		response, err = backend.Commits(r.Context(), repository, forge.CommitsRequest{
			Ref: query.Get("ref"), Path: query.Get("path"), Skip: skip, Limit: limit, Principal: principal,
		})
	case strings.HasPrefix(subpath, "commit/"):
		response, err = backend.Commit(r.Context(), repository, forge.RevisionRequest{Revision: strings.TrimPrefix(subpath, "commit/"), Principal: principal})
	default:
		err = &forge.Error{Status: http.StatusNotFound, Code: "github_object_not_found"}
	}
	if err != nil {
		writeError(w, err)
		return
	}
	defer response.Body.Close()
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.Status)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, response.Body)
	}
}

func (backend *Backend) resolveResponse(ctx context.Context, repository forge.Repository, address, principal string) (forge.Response, error) {
	revision, sha, path, err := backend.resolveAddress(ctx, repository, forge.RevisionRequest{Revision: address, Principal: principal})
	if err != nil {
		return forge.Response{}, err
	}
	return jsonResponse(http.StatusOK, resolvedResult{Ref: revision, SHA: sha, Path: path, Kind: "commit"}), nil
}

func (backend *Backend) resolveAddress(ctx context.Context, repository forge.Repository, request forge.RevisionRequest) (string, string, string, error) {
	address := strings.Trim(request.Revision, "/")
	if request.Path != "" {
		address += "/" + strings.Trim(request.Path, "/")
	}
	if len(address) > 4096 {
		return "", "", "", &forge.Error{Status: http.StatusBadRequest, Code: "invalid_revision"}
	}
	if sha, err := backend.Resolve(ctx, repository, forge.RevisionRequest{Revision: address, Principal: request.Principal}); err == nil {
		return address, sha, "", nil
	}
	for index := strings.LastIndex(address, "/"); index > 0; index = strings.LastIndex(address[:index], "/") {
		revision, path := address[:index], address[index+1:]
		sha, err := backend.Resolve(ctx, repository, forge.RevisionRequest{Revision: revision, Principal: request.Principal})
		if err == nil {
			return revision, sha, path, nil
		}
	}
	return "", "", "", &forge.Error{Status: http.StatusNotFound, Code: "github_object_not_found"}
}

func (backend *Backend) doRepository(ctx context.Context, repository forge.Repository, method, path string, body io.Reader, permissions map[string]string) (*http.Response, error) {
	connection, repositoryID, err := backend.connectionFor(ctx, repository)
	if err != nil {
		return nil, err
	}
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

func jsonResponse(status int, value any) forge.Response {
	body, _ := json.Marshal(value)
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	header.Set("Cache-Control", "private, no-store")
	return forge.Response{Status: status, Header: header, Body: io.NopCloser(bytes.NewReader(body))}
}

func copyResponseHeaders(destination, source http.Header) {
	for _, name := range []string{"Content-Type", "Cache-Control", "ETag", "Last-Modified", "X-Lapis-Truncated"} {
		if value := source.Get(name); value != "" {
			destination.Set(name, value)
		}
	}
	if destination.Get("Cache-Control") == "" {
		destination.Set("Cache-Control", "private, no-store")
	}
}

func writeError(w http.ResponseWriter, err error) {
	status, code := http.StatusBadGateway, "github_request_failed"
	if backendError, ok := err.(*forge.Error); ok {
		status, code = backendError.Status, backendError.Code
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
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

func normalizedRef(ref gitHubRef) map[string]string {
	return map[string]string{"name": ref.Ref, "sha": ref.Object.SHA}
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

type resolvedResult struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type treeResult struct {
	Ref     string      `json:"ref"`
	SHA     string      `json:"sha"`
	Path    string      `json:"path"`
	Entries []treeEntry `json:"entries"`
}

type treeEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Mode string `json:"mode"`
	Size int64  `json:"size"`
	SHA  string `json:"sha"`
}

type blobResult struct {
	Ref      string `json:"ref"`
	SHA      string `json:"sha"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Contents string `json:"contents,omitempty"`
	Binary   bool   `json:"binary,omitempty"`
	TooLarge bool   `json:"too_large,omitempty"`
}

type commitResult struct {
	SHA         string   `json:"sha"`
	Parents     []string `json:"parents"`
	Author      string   `json:"author"`
	AuthorEmail string   `json:"author_email"`
	AuthorDate  string   `json:"author_date"`
	Committer   string   `json:"committer"`
	CommitDate  string   `json:"commit_date"`
	Subject     string   `json:"subject"`
	Body        string   `json:"body"`
	Trailers    []any    `json:"trailers"`
}

type commitsResult struct {
	Ref     string         `json:"ref"`
	SHA     string         `json:"sha"`
	Commits []commitResult `json:"commits"`
	More    bool           `json:"more"`
}

type commitStat struct {
	Path      string `json:"path"`
	Additions int64  `json:"additions"`
	Deletions int64  `json:"deletions"`
}

type commitDetail struct {
	Commit commitResult `json:"commit"`
	Stats  []commitStat `json:"stats"`
	Patch  string       `json:"patch"`
}

func normalizeCommit(commit gitHubCommit) commitResult {
	subject, body, _ := strings.Cut(commit.Commit.Message, "\n")
	parents := make([]string, 0, len(commit.Parents))
	for _, parent := range commit.Parents {
		parents = append(parents, parent.SHA)
	}
	return commitResult{
		SHA: commit.SHA, Parents: parents, Author: commit.Commit.Author.Name,
		AuthorEmail: commit.Commit.Author.Email, AuthorDate: commit.Commit.Author.Date,
		Committer: commit.Commit.Committer.Name, CommitDate: commit.Commit.Committer.Date,
		Subject: subject, Body: strings.TrimSpace(body), Trailers: []any{},
	}
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
	_ forge.GitBackend           = (*Backend)(nil)
	_ forge.RepositoryAPIBackend = (*Backend)(nil)
)
