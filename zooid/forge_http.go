package zooid

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"io"
	"net/http"
	"strings"
	"time"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
)

const maxForgeAPIRequest = 1 << 20

// ServeForgeAPI is the NIP-98 protected application surface for capability
// discovery and explicit provider administration. Nostr event APIs remain the
// native collaboration path.
func (policy *GitForgePolicy) ServeForgeAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := readForgeBody(w, r)
	if !ok {
		return
	}
	expectedURL, err := policy.publicRequestURL(r)
	if err != nil {
		writeGitError(w, http.StatusBadRequest, "invalid_request_url")
		return
	}
	authEvent, err := gitforge.VerifyRequestAuth(r, expectedURL, body, time.Now())
	if err != nil {
		gitAuthRequired(w, r.Method)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	idempotencyKey, ok := forgeIdempotencyKey(r, authEvent)
	if !ok {
		writeGitError(w, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/forge/")
	switch {
	case r.Method == http.MethodPost && path == "connections/github":
		policy.createGitHubConnection(w, r, authEvent.PubKey, body)
	case r.Method == http.MethodGet && path == "connections/github":
		policy.listGitHubConnections(w, r, authEvent.PubKey)
	case r.Method == http.MethodPut && strings.HasPrefix(path, "connections/github/"):
		policy.updateGitHubConnection(w, r, authEvent.PubKey, path, body)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "connections/github/") && strings.HasSuffix(path, "/revalidate"):
		policy.revalidateGitHubConnection(w, r, authEvent.PubKey, path)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "connections/github/") && strings.HasSuffix(path, "/repositories"):
		policy.listGitHubRepositories(w, r, authEvent.PubKey, path)
	case strings.HasPrefix(path, "repositories/"):
		policy.serveRepositoryForgeAPI(w, r, authEvent.PubKey, idempotencyKey, body)
	case strings.HasPrefix(path, "work-items/"):
		policy.serveWorkItemForgeAPI(w, r, authEvent.PubKey, idempotencyKey, body)
	case strings.HasPrefix(path, "pull-requests/"):
		policy.servePullRequestForgeAPI(w, r, authEvent.PubKey, idempotencyKey, body)
	case strings.HasPrefix(path, "operations/"):
		policy.serveForgeOperationAPI(w, r, authEvent.PubKey, path)
	default:
		http.NotFound(w, r)
	}
}

func forgeIdempotencyKey(r *http.Request, auth nostr.Event) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if value == "" {
		return "nip98:" + auth.ID.Hex(), true
	}
	if len(value) > 200 {
		return "", false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return "", false
		}
	}
	return "client:" + auth.PubKey.Hex() + ":" + r.Method + ":" + r.URL.Path + ":" + value, true
}

func readForgeBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil, true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxForgeAPIRequest+1))
	if err != nil || len(body) > maxForgeAPIRequest {
		writeGitError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return nil, false
	}
	return body, true
}

func decodeStrictJSON(body []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}

func writeForgeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func forgeHTTPError(err error) (int, string) {
	var backendError *forge.Error
	if errors.As(err, &backendError) {
		return backendError.Status, backendError.Code
	}
	return http.StatusBadGateway, "forge_operation_failed"
}

func (policy *GitForgePolicy) ServeGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowedForGit(w)
		return
	}
	if policy.EventSource == nil || !policy.Config.GitHub.Enabled {
		writeGitError(w, http.StatusServiceUnavailable, "github_webhook_not_enabled")
		return
	}
	limit := policy.Config.GitHub.MaxWebhookBytes
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		writeGitError(w, http.StatusRequestEntityTooLarge, "github_webhook_too_large")
		return
	}
	events, err := policy.EventSource.VerifyAndDecode(r.Context(), r.Header, body)
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	if len(events) != 1 {
		writeGitError(w, http.StatusBadRequest, "invalid_github_webhook")
		return
	}
	digest := sha256.Sum256(body)
	inserted, err := policy.storeGitHubWebhook(r.Context(), events[0], hex.EncodeToString(digest[:]))
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusAccepted, map[string]any{
		"delivery_id": events[0].DeliveryID, "queued": inserted, "duplicate": !inserted,
	})
}
