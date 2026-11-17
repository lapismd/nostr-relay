package githubforge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"zooid/internal/forge"
)

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
