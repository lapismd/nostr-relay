package buzzcompat

import (
	"context"
	"encoding/base64"
	"fiatjaf.com/nostr"
	"iter"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxRequestBody = 1 << 20
	queryLimit     = 1000
	authWindow     = 60 * time.Second
	maxAuthTagSize = 4096
)

// Backend connects the compatibility transport to the relay's existing
// authorization, query, and publish pipelines.
type Backend struct {
	Authenticate            func(context.Context, HTTPAuth) (context.Context, error)
	WithAuthenticatedPubkey func(context.Context, nostr.PubKey) context.Context
	AuthorizeQuery          func(context.Context, nostr.Filter) (bool, string)
	Query                   func(context.Context, nostr.Filter) iter.Seq[nostr.Event]
	QueryBefore             func(context.Context, nostr.Filter, string) iter.Seq[nostr.Event]
	EphemeralQuery          func(context.Context, []nostr.Filter) ([]nostr.Event, error)
	CountMany               func(context.Context, []nostr.Filter) (CountResult, error)
	Publish                 func(context.Context, nostr.Event) (bool, error)
	Broadcast               func(nostr.Event)
	NIP11                   http.HandlerFunc
	GitHTTP                 http.HandlerFunc
	RepositoryAPI           http.HandlerFunc
	PullRequestAPI          http.HandlerFunc
	WalGitEvents            http.HandlerFunc
	ForgeAPI                http.HandlerFunc
	GitHubWebhook           http.HandlerFunc
}

// HTTPAuth carries only values protected by the already verified NIP-98 event
// plus Buzz's optional owner credential header.
type HTTPAuth struct {
	PubKey   nostr.PubKey
	SignedAt uint64
	AuthTag  string
}

type CountResult struct {
	Count       uint64 `json:"count"`
	Approximate bool   `json:"approximate,omitempty"`
	HyperLogLog string `json:"hll,omitempty"`
}

type queryFilter struct {
	Filter   nostr.Filter
	BeforeID string
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
	if h.backend.GitHTTP != nil {
		mux.HandleFunc("/git/", h.backend.GitHTTP)
	}
	if h.backend.RepositoryAPI != nil {
		mux.HandleFunc("/api/git/", h.backend.RepositoryAPI)
	}
	if h.backend.PullRequestAPI != nil {
		mux.HandleFunc("/api/pull-requests/", h.backend.PullRequestAPI)
	}
	if h.backend.WalGitEvents != nil {
		mux.HandleFunc("/internal/walgit/events", h.backend.WalGitEvents)
	}
	if h.backend.ForgeAPI != nil {
		mux.HandleFunc("/api/forge/", h.backend.ForgeAPI)
	}
	if h.backend.GitHubWebhook != nil {
		mux.HandleFunc("/webhooks/github", h.backend.GitHubWebhook)
	}
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
