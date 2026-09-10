package githubforge

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"zooid/internal/forge"
)

const (
	APIBaseURL = "https://api.github.com"
	APIVersion = "2026-03-10"
)

type Connection struct {
	ID                  string
	Host                string
	AppID               string
	InstallationID      int64
	CredentialRef       string
	WebhookSecretRef    string
	BridgeCredentialRef string
	BridgePubkey        string
	AccountID           string
	AccountLogin        string
	Status              string
	LastError           string
	UpdatedAt           time.Time
}

type ConnectionLoader interface {
	LoadGitHubConnection(context.Context, string) (Connection, bool, error)
}

type installationToken struct {
	value     string
	expiresAt time.Time
}

type tokenManager struct {
	credentials CredentialStore
	client      *http.Client
	base        *url.URL
	now         func() time.Time

	mu    sync.Mutex
	cache map[string]installationToken
}

func newTokenManager(credentials CredentialStore, client *http.Client, base *url.URL) *tokenManager {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &tokenManager{credentials: credentials, client: client, base: base, now: time.Now, cache: make(map[string]installationToken)}
}

func (manager *tokenManager) appJWT(connection Connection) (string, error) {
	keyData, err := manager.credentials.Load(connection.CredentialRef)
	if err != nil {
		return "", fmt.Errorf("GitHub App credential unavailable")
	}
	key, err := parseRSAPrivateKey(keyData)
	if err != nil {
		return "", fmt.Errorf("invalid GitHub App credential")
	}
	now := manager.now()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": connection.AppID,
	})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT")
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func parseRSAPrivateKey(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("missing PEM data")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA")
	}
	return key, nil
}

func (manager *tokenManager) installation(ctx context.Context, connection Connection, repositoryID int64, permissions map[string]string) (string, error) {
	cacheKey := tokenCacheKey(connection.ID, repositoryID, permissions)
	manager.mu.Lock()
	if token, found := manager.cache[cacheKey]; found && token.expiresAt.After(manager.now().Add(5*time.Minute)) {
		manager.mu.Unlock()
		return token.value, nil
	}
	manager.mu.Unlock()

	jwt, err := manager.appJWT(connection)
	if err != nil {
		return "", err
	}
	payload := map[string]any{"permissions": permissions}
	if repositoryID != 0 {
		payload["repository_ids"] = []int64{repositoryID}
	}
	body, _ := json.Marshal(payload)
	target := *manager.base
	target.Path = "/app/installations/" + strconv.FormatInt(connection.InstallationID, 10) + "/access_tokens"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	setGitHubHeaders(request, jwt)
	request.Header.Set("Content-Type", "application/json")
	response, err := manager.client.Do(request)
	if err != nil {
		return "", &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_unavailable", Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", githubStatusError(response.StatusCode, response.Header)
	}
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil || result.Token == "" || len(result.Token) > 64<<10 || !result.ExpiresAt.After(manager.now()) {
		return "", &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_token_response"}
	}
	manager.mu.Lock()
	manager.cache[cacheKey] = installationToken{value: result.Token, expiresAt: result.ExpiresAt}
	manager.mu.Unlock()
	return result.Token, nil
}

func tokenCacheKey(connectionID string, repositoryID int64, permissions map[string]string) string {
	keys := make([]string, 0, len(permissions))
	for key := range permissions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString(connectionID)
	builder.WriteByte('\x00')
	builder.WriteString(strconv.FormatInt(repositoryID, 10))
	for _, key := range keys {
		builder.WriteByte('\x00')
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(permissions[key])
	}
	return builder.String()
}

func setGitHubHeaders(request *http.Request, token string) {
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", APIVersion)
	request.Header.Set("User-Agent", "lapis-relay-forge")
}

func githubStatusError(status int, headers http.Header) error {
	code := "github_request_failed"
	retryable := status == http.StatusTooManyRequests || status >= 500
	switch status {
	case http.StatusUnauthorized:
		code = "github_installation_unauthorized"
	case http.StatusForbidden:
		code = "github_access_denied"
	case http.StatusNotFound:
		code = "github_object_not_found"
	case http.StatusConflict, http.StatusUnprocessableEntity:
		code = "github_conflict"
	case http.StatusTooManyRequests:
		code = "github_rate_limited"
	}
	if headers.Get("X-RateLimit-Remaining") == "0" {
		code = "github_rate_limited"
		retryable = true
	}
	var retryAt time.Time
	if raw := strings.TrimSpace(headers.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
			retryAt = time.Now().Add(time.Duration(seconds) * time.Second)
		} else if parsed, err := http.ParseTime(raw); err == nil {
			retryAt = parsed
		}
	}
	if raw := strings.TrimSpace(headers.Get("X-RateLimit-Reset")); retryAt.IsZero() && raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
			retryAt = time.Unix(seconds, 0)
		}
	}
	return &forge.Error{Status: status, Code: code, Retryable: retryable, RetryAt: retryAt}
}
