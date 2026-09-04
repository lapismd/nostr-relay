// Package buzzcompat implements the small authenticated HTTP bridge used by
// Buzz clients. It is deliberately isolated from the upstream-derived relay so
// upstream merges have a single integration point.
package buzzcompat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
)

const (
	maxRequestBody = 1 << 20
	queryLimit     = 1000
	authWindow     = 60 * time.Second
)

// Backend connects the compatibility transport to the relay's existing
// authorization, query, and publish pipelines.
type Backend struct {
	WithAuthenticatedPubkey func(context.Context, nostr.PubKey) context.Context
	AuthorizeQuery          func(context.Context, nostr.Filter) (bool, string)
	Query                   func(context.Context, nostr.Filter) iter.Seq[nostr.Event]
	Publish                 func(context.Context, nostr.Event) (bool, error)
	Broadcast               func(nostr.Event)
	NIP11                   http.HandlerFunc
}

// Handler serves Buzz's core HTTP bridge and delegates every other request to
// the normal Khatru/Blossom router.
type Handler struct {
	backend Backend
	now     func() time.Time
	replay  replayCache
}

type replayCache struct {
	mu   sync.Mutex
	seen map[nostr.ID]time.Time
}

type submitResponse struct {
	EventID  nostr.ID `json:"event_id"`
	Accepted bool     `json:"accepted"`
	Message  string   `json:"message"`
}

// New constructs a compatibility handler.
func New(backend Backend) *Handler {
	return &Handler{
		backend: backend,
		now:     time.Now,
		replay: replayCache{
			seen: make(map[nostr.ID]time.Time),
		},
	}
}

// Wrap installs compatibility routes in front of the existing relay router.
// The media alias is served by internally rewriting to Blossom's canonical
// content-addressed path, avoiding a redirect that Buzz's SSRF guard rejects.
func (h *Handler) Wrap(next http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/query", h.handleQuery)
	mux.HandleFunc("/events", h.handleEvents)
	mux.HandleFunc("/count", h.handleCount)
	mux.HandleFunc("/info", h.handleInfo)
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		h.serveBlossomUpload(next, w, r, "/upload")
	})
	mux.HandleFunc("/media/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media/upload" {
			if r.Method != http.MethodPut {
				methodNotAllowed(w, http.MethodPut)
				return
			}
			h.serveBlossomUpload(next, w, r, "/upload")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/media/")
		if name == "" || strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}
		h.serveBlossomRead(next, w, r, "/"+name)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && blobHash(r.URL.Path) != "" {
			h.serveBlossomRead(next, w, r, r.URL.Path)
			return
		}
		next.ServeHTTP(w, r)
	})
	return mux
}

func (h *Handler) serveBlossomUpload(next http.Handler, w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPut && !(path == "/upload" && r.Method == http.MethodHead) {
		methodNotAllowed(w, http.MethodPut, http.MethodHead)
		return
	}
	clone := cloneWithPath(r, path)
	if clone.Header.Get("Authorization") != "" {
		event, raw, err := parseBlossomAuth(clone, "upload")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if requestHash := clone.Header.Get("X-SHA-256"); requestHash != "" {
			if claimedHash, ok := tagValue(event.Tags, "x"); !ok || !strings.EqualFold(claimedHash, requestHash) {
				writeError(w, http.StatusForbidden, "Blossom upload hash does not match request")
				return
			}
		}
		// Khatru's Blossom decoder only accepts padded standard Base64. Buzz
		// deliberately emits unpadded Base64URL, so canonicalize the verified
		// event without changing its signed JSON bytes.
		clone.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(raw))
	}
	next.ServeHTTP(w, clone)
}

func (h *Handler) serveBlossomRead(next http.Handler, w http.ResponseWriter, r *http.Request, path string) {
	clone := cloneWithPath(r, path)
	if clone.Header.Get("Authorization") == "" {
		next.ServeHTTP(w, clone)
		return
	}
	event, _, err := parseBlossomAuth(clone, "get")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash := blobHash(path)
	claimedHash, hasHash := tagValue(event.Tags, "x")
	server, hasServer := tagValue(event.Tags, "server")
	if !(hasHash && strings.EqualFold(claimedHash, hash)) && !(hasServer && serverMatchesRequest(server, clone)) {
		writeError(w, http.StatusForbidden, "Blossom read authorization is not scoped to this blob or server")
		return
	}
	clone = clone.WithContext(h.backend.WithAuthenticatedPubkey(clone.Context(), event.PubKey))
	// The token has already been fully validated. Removing it avoids Khatru's
	// incompatible full-URL comparison for Buzz's BUD-11 authority tag.
	clone.Header.Del("Authorization")
	next.ServeHTTP(w, clone)
}

func (h *Handler) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	h.backend.NIP11(w, r)
}

func (h *Handler) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	ctx, ok := h.authenticate(w, r, body)
	if !ok {
		return
	}

	var filters []nostr.Filter
	if err := decodeJSON(body, &filters); err != nil {
		writeError(w, http.StatusBadRequest, "invalid filters: "+err.Error())
		return
	}

	events := make([]nostr.Event, 0)
	seen := make(map[nostr.ID]struct{})
	for _, filter := range filters {
		if reject, message := h.backend.AuthorizeQuery(ctx, filter); reject {
			writeError(w, http.StatusForbidden, message)
			return
		}
		for event := range h.backend.Query(ctx, filter) {
			if _, duplicate := seen[event.ID]; duplicate {
				continue
			}
			seen[event.ID] = struct{}{}
			events = append(events, event)
			if len(events) >= queryLimit {
				writeJSON(w, http.StatusOK, events)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, events)
}

func (h *Handler) handleCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	ctx, ok := h.authenticate(w, r, body)
	if !ok {
		return
	}

	var filters []nostr.Filter
	if err := decodeJSON(body, &filters); err != nil {
		writeError(w, http.StatusBadRequest, "invalid filters: "+err.Error())
		return
	}

	seen := make(map[nostr.ID]struct{})
	for _, filter := range filters {
		if reject, message := h.backend.AuthorizeQuery(ctx, filter); reject {
			writeError(w, http.StatusForbidden, message)
			return
		}
		for event := range h.backend.Query(ctx, filter) {
			seen[event.ID] = struct{}{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": len(seen)})
}

func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	ctx, ok := h.authenticate(w, r, body)
	if !ok {
		return
	}

	var event nostr.Event
	if err := decodeJSON(body, &event); err != nil {
		writeError(w, http.StatusBadRequest, "invalid event: "+err.Error())
		return
	}
	if !event.CheckID() {
		writeJSON(w, http.StatusBadRequest, submitResponse{EventID: event.ID, Message: "invalid: id is computed incorrectly"})
		return
	}
	if !event.VerifySignature() {
		writeJSON(w, http.StatusBadRequest, submitResponse{EventID: event.ID, Message: "invalid: signature is invalid"})
		return
	}

	skipBroadcast, err := h.backend.Publish(ctx, event)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, submitResponse{EventID: event.ID, Message: err.Error()})
		return
	}
	if !skipBroadcast {
		h.backend.Broadcast(event)
	}
	writeJSON(w, http.StatusOK, submitResponse{EventID: event.ID, Accepted: true, Message: "saved"})
}

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
	return h.backend.WithAuthenticatedPubkey(r.Context(), event.PubKey), true
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
