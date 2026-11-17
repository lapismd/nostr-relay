package buzzcompat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request, body []byte) (context.Context, bool) {
	event, err := parseNIP98(r, body, h.now())
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return nil, false
	}
	if !h.replay.use(event.ID, h.now()) {
		writeError(w, http.StatusUnauthorized, "auth event has already been used")
		return nil, false
	}
	authTag, err := extractAuthTagHeader(r.Header.Values("X-Auth-Tag"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return nil, false
	}
	if h.backend.Authenticate != nil {
		ctx, err := h.backend.Authenticate(r.Context(), HTTPAuth{PubKey: event.PubKey, SignedAt: uint64(event.CreatedAt), AuthTag: authTag})
		if err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return nil, false
		}
		return ctx, true
	}
	return h.backend.WithAuthenticatedPubkey(r.Context(), event.PubKey), true
}

func extractAuthTagHeader(values []string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", errors.New("multiple X-Auth-Tag headers are not allowed")
	}
	value := strings.TrimSpace(values[0])
	if len(value) > maxAuthTagSize {
		return "", errors.New("X-Auth-Tag header is too large")
	}
	return value, nil
}

func parseNIP98(r *http.Request, body []byte, now time.Time) (nostr.Event, error) {
	event, _, err := decodeAuthorization(r.Header.Get("Authorization"))
	if err != nil {
		return event, fmt.Errorf("invalid NIP-98 authorization: %w", err)
	}
	if event.Kind != nostr.KindHTTPAuth || event.Content != "" || !event.CheckID() || !event.VerifySignature() {
		return event, errors.New("invalid NIP-98 auth event")
	}
	created := time.Unix(int64(event.CreatedAt), 0)
	if created.Before(now.Add(-authWindow)) || created.After(now.Add(authWindow)) {
		return event, errors.New("auth event is outside the allowed time window")
	}

	method, ok := tagValue(event.Tags, "method")
	if !ok || !strings.EqualFold(method, r.Method) {
		return event, errors.New("auth event method does not match request")
	}
	signedURL, ok := tagValue(event.Tags, "u")
	if !ok || normalizeURL(signedURL) != normalizeURL(requestURL(r)) {
		return event, errors.New("auth event URL does not match request")
	}
	payload, ok := tagValue(event.Tags, "payload")
	expectedPayload := sha256.Sum256(body)
	if !ok || !strings.EqualFold(payload, hex.EncodeToString(expectedPayload[:])) {
		return event, errors.New("auth event payload does not match request")
	}
	return event, nil
}

func parseBlossomAuth(r *http.Request, operation string) (nostr.Event, []byte, error) {
	event, raw, err := decodeAuthorization(r.Header.Get("Authorization"))
	if err != nil {
		return event, nil, fmt.Errorf("invalid Blossom authorization: %w", err)
	}
	if event.Kind != nostr.KindBlobs || !event.CheckID() || !event.VerifySignature() {
		return event, nil, errors.New("invalid Blossom authorization event")
	}
	expirationValue, ok := tagValue(event.Tags, "expiration")
	if !ok {
		return event, nil, errors.New("Blossom authorization is missing expiration")
	}
	expiration, err := strconv.ParseInt(expirationValue, 10, 64)
	if err != nil || nostr.Timestamp(expiration) < nostr.Now() {
		return event, nil, errors.New("Blossom authorization has expired")
	}
	if action, ok := tagValue(event.Tags, "t"); !ok || action != operation {
		return event, nil, fmt.Errorf("Blossom authorization does not permit %s", operation)
	}
	return event, raw, nil
}

func decodeAuthorization(authorization string) (nostr.Event, []byte, error) {
	var event nostr.Event
	authorization = strings.TrimSpace(authorization)
	if len(authorization) < 7 || !strings.EqualFold(authorization[:6], "Nostr ") {
		return event, nil, errors.New("missing Nostr authorization")
	}
	encoded := strings.TrimSpace(authorization[6:])
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var decoded []byte
	var err error
	for _, encoding := range encodings {
		decoded, err = encoding.DecodeString(encoded)
		if err == nil {
			break
		}
	}
	if err != nil {
		return event, nil, errors.New("invalid base64 auth event")
	}
	if err := json.Unmarshal(decoded, &event); err != nil {
		return event, nil, errors.New("invalid auth event JSON")
	}
	return event, decoded, nil
}

func requestURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host + r.URL.RequestURI()
}

func normalizeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path != "/" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}
	return parsed.String()
}

func cloneWithPath(r *http.Request, path string) *http.Request {
	clone := r.Clone(r.Context())
	clonedURL := *r.URL
	clonedURL.Path = path
	clone.URL = &clonedURL
	return clone
}

func blobHash(path string) string {
	name := strings.TrimPrefix(path, "/")
	if strings.Contains(name, "/") {
		return ""
	}
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		name = name[:dot]
	}
	if len(name) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(name); err != nil {
		return ""
	}
	return strings.ToLower(name)
}

func serverMatchesRequest(server string, r *http.Request) bool {
	server = strings.TrimSpace(strings.TrimRight(server, "/"))
	if parsed, err := url.Parse(server); err == nil && parsed.Host != "" {
		server = parsed.Host
	}
	return strings.EqualFold(server, r.Host)
}

func tagValue(tags nostr.Tags, name string) (string, bool) {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1], true
		}
	}
	return "", false
}

func (cache *replayCache) use(id nostr.ID, now time.Time) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cutoff := now.Add(-authWindow)
	for seenID, usedAt := range cache.seen {
		if usedAt.Before(cutoff) {
			delete(cache.seen, seenID)
		}
	}
	if _, exists := cache.seen[id]; exists {
		return false
	}
	cache.seen[id] = now
	return true
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		return nil, false
	}
	return body, true
}

func decodeJSON(body []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after JSON value")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, fmt.Sprintf("method must be %s", strings.Join(methods, " or ")))
}
