package githubforge

import (
	"context"
	"errors"
	"fiatjaf.com/nostr"
	"io"
	"net/http"
	"strings"
	"zooid/internal/forge"
)

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
