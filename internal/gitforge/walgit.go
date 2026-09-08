package gitforge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"zooid/internal/forge"
	"zooid/internal/projecttask"
)

type WalGitBackend struct {
	base   *url.URL
	token  string
	client *http.Client
}

func NewWalGitBackend(rawURL, token string) (*WalGitBackend, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawURL), "/"))
	if err != nil || base.Scheme == "" || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("invalid WalGit backend URL")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("WALGIT_RELAY_TOKEN is required")
	}
	return &WalGitBackend{
		base: base, token: token,
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (backend *WalGitBackend) EnsureRepository(ctx context.Context, coordinate projecttask.RepositoryCoordinate) error {
	target := *backend.base
	target.Path = "/" + coordinate.Owner.Hex() + "/" + url.PathEscape(coordinate.ID) + "/api"
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), nil)
	if err != nil {
		return err
	}
	backend.authorize(request, coordinate.Owner.Hex())
	response, err := backend.client.Do(request)
	if err != nil {
		return &BackendError{Status: http.StatusServiceUnavailable, Code: "walgit_unavailable"}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusConflict {
		return &BackendError{Status: response.StatusCode, Code: "repository_create_failed"}
	}
	return nil
}

func (backend *WalGitBackend) ProxyGitHTTP(w http.ResponseWriter, r *http.Request, repository Repository, subpath, principal string) {
	path := "/" + repository.Coordinate.Owner.Hex() + "/" + url.PathEscape(repository.Coordinate.ID) + ".git"
	if subpath != "" {
		path += "/" + strings.TrimLeft(subpath, "/")
	}
	backend.proxy(w, r, path, principal, false)
}

func (backend *WalGitBackend) ProxyRepositoryAPI(w http.ResponseWriter, r *http.Request, repository Repository, subpath, principal string) {
	path := "/" + repository.Coordinate.Owner.Hex() + "/" + url.PathEscape(repository.Coordinate.ID) + "/api"
	if subpath != "" {
		path += "/" + strings.TrimLeft(subpath, "/")
	}
	backend.proxy(w, r, path, principal, true)
}

func (backend *WalGitBackend) Resolve(ctx context.Context, repository Repository, revision, principal string) (string, error) {
	target := *backend.base
	target.Path = "/" + repository.Coordinate.Owner.Hex() + "/" + url.PathEscape(repository.Coordinate.ID) + "/api/resolve/" + strings.TrimLeft(revision, "/")
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", err
	}
	backend.authorize(httpRequest, principal)
	response, err := backend.client.Do(httpRequest)
	if err != nil {
		return "", &BackendError{Status: http.StatusServiceUnavailable, Code: "walgit_unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", &BackendError{Status: response.StatusCode, Code: "git_revision_not_found"}
	}
	var result struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil || !isFullObjectID(result.SHA) {
		return "", &BackendError{Status: http.StatusBadGateway, Code: "invalid_resolve_response"}
	}
	return strings.ToLower(result.SHA), nil
}

func (backend *WalGitBackend) repositoryResponse(ctx context.Context, repository Repository, subpath, principal string) (forge.Response, error) {
	target := *backend.base
	parts := strings.SplitN(subpath, "?", 2)
	target.Path = "/" + repository.Coordinate.Owner.Hex() + "/" + url.PathEscape(repository.Coordinate.ID) + "/api"
	if parts[0] != "" {
		target.Path += "/" + strings.TrimLeft(parts[0], "/")
	}
	if len(parts) == 2 {
		target.RawQuery = parts[1]
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return forge.Response{}, err
	}
	backend.authorize(request, principal)
	response, err := backend.client.Do(request)
	if err != nil {
		return forge.Response{}, &BackendError{Status: http.StatusServiceUnavailable, Code: "walgit_unavailable"}
	}
	stripWalGitHeaders(response.Header)
	response.Header.Del("Set-Cookie")
	cache := response.Header.Get("Cache-Control")
	if cache == "" {
		response.Header.Set("Cache-Control", "private, no-store")
	} else {
		response.Header.Set("Cache-Control", privateCacheControl(cache))
	}
	return forge.Response{Status: response.StatusCode, Header: response.Header, Body: response.Body}, nil
}

func (backend *WalGitBackend) proxy(w http.ResponseWriter, r *http.Request, path, principal string, privateCache bool) {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(backend.base)
			request.Out.URL.Path = path
			request.Out.URL.RawPath = ""
			request.Out.Host = backend.base.Host
			stripWalGitHeaders(request.Out.Header)
			backend.authorize(request.Out, principal)
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeBackendError(w, http.StatusBadGateway, "walgit_unavailable")
		},
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		stripWalGitHeaders(response.Header)
		response.Header.Del("Set-Cookie")
		if privateCache {
			cache := response.Header.Get("Cache-Control")
			if cache == "" {
				response.Header.Set("Cache-Control", "private, no-store")
			} else {
				response.Header.Set("Cache-Control", privateCacheControl(cache))
			}
		}
		return nil
	}
	proxy.ServeHTTP(w, r)
}

func privateCacheControl(value string) string {
	directives := strings.Split(value, ",")
	foundPrivate := false
	for index, directive := range directives {
		trimmed := strings.TrimSpace(directive)
		if strings.EqualFold(trimmed, "public") {
			directives[index] = "private"
			foundPrivate = true
		} else if strings.EqualFold(trimmed, "private") {
			foundPrivate = true
		}
	}
	if !foundPrivate {
		directives = append([]string{"private"}, directives...)
	}
	for index := range directives {
		directives[index] = strings.TrimSpace(directives[index])
	}
	return strings.Join(directives, ", ")
}

func (backend *WalGitBackend) Compare(ctx context.Context, repository Repository, request CompareRequest) (CompareResult, error) {
	var result CompareResult
	target := *backend.base
	target.Path = "/" + repository.Coordinate.Owner.Hex() + "/" + url.PathEscape(repository.Coordinate.ID) + "/api/compare"
	query := target.Query()
	query.Set("base", request.Base)
	query.Set("head", request.Head)
	if request.UseMergeBase {
		query.Set("mode", "merge-base")
	} else {
		query.Set("mode", "direct")
	}
	query.Set("max_files", strconv.Itoa(request.MaxFiles))
	query.Set("max_patch_bytes", strconv.FormatInt(request.MaxPatchBytes, 10))
	target.RawQuery = query.Encode()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return result, err
	}
	backend.authorize(httpRequest, request.Principal)
	response, err := backend.client.Do(httpRequest)
	if err != nil {
		return result, &BackendError{Status: http.StatusServiceUnavailable, Code: "walgit_unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return result, &BackendError{Status: response.StatusCode, Code: compareErrorCode(response.StatusCode)}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, request.MaxPatchBytes+(4<<20)))
	if err := decoder.Decode(&result); err != nil {
		return result, &BackendError{Status: http.StatusBadGateway, Code: "invalid_compare_response"}
	}
	return result, nil
}

func compareErrorCode(status int) string {
	switch status {
	case http.StatusRequestEntityTooLarge:
		return "diff_too_large"
	case http.StatusUnprocessableEntity:
		return "unrelated_histories"
	case http.StatusNotFound:
		return "git_object_not_found"
	default:
		return "compare_failed"
	}
}

func (backend *WalGitBackend) authorize(request *http.Request, principal string) {
	request.Header.Set("Authorization", "Bearer "+backend.token)
	request.Header.Set("X-Walgit-Principal", principal)
}

func stripWalGitHeaders(headers http.Header) {
	headers.Del("Authorization")
	headers.Del("Cookie")
	for name := range headers {
		if strings.HasPrefix(strings.ToLower(name), "x-walgit-") {
			headers.Del(name)
		}
	}
}

func writeBackendError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"error":"`+code+`"}`)
}

func isFullObjectID(value string) bool {
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
