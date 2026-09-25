package agentbunker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip46"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
	"zooid/internal/keywrap"
)

type memoryStore struct {
	mu       sync.Mutex
	agents   map[string]Agent
	clients  map[string]Client
	requests map[string]CachedRequest
	auth     map[string]bool
	http     map[string]struct {
		hash string
		body []byte
	}
}

func newMemoryStore() *memoryStore {
	return &memoryStore{agents: map[string]Agent{}, clients: map[string]Client{}, requests: map[string]CachedRequest{}, auth: map[string]bool{}, http: map[string]struct {
		hash string
		body []byte
	}{}}
}

func (m *memoryStore) CreateAgent(_ context.Context, a Agent, c Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.agents[a.Pubkey]; ok {
		return ErrConflict
	}
	m.agents[a.Pubkey] = a
	m.clients[a.Pubkey+":"+c.ClientPubkey] = c
	return nil
}

func (m *memoryStore) Agent(_ context.Context, k string) (Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[k]
	if !ok {
		return a, ErrNotFound
	}
	return a, nil
}

func (m *memoryStore) Client(_ context.Context, a, c string) (Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.clients[a+":"+c]
	if !ok {
		return v, ErrNotFound
	}
	return v, nil
}

func (m *memoryStore) ListAgents(_ context.Context, o string) ([]AgentView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []AgentView{}
	for _, a := range m.agents {
		if a.OwnerPubkey == o {
			state := "none"
			for _, c := range m.clients {
				if c.AgentPubkey == a.Pubkey && (c.Status == ClientActive || c.Status == ClientPending) {
					state = c.Status
				}
			}
			out = append(out, AgentView{Pubkey: a.Pubkey, OwnerPubkey: o, Status: a.Status, ConnectionState: state, CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt})
		}
	}
	return out, nil
}

func (m *memoryStore) OwnerCounts(_ context.Context, o string, since time.Time) (int, int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	active, pending, recent := 0, 0, 0
	for _, a := range m.agents {
		if a.OwnerPubkey == o {
			if a.Status == StatusActive {
				active++
			}
			if a.Status == StatusPending {
				pending++
			}
			if !a.CreatedAt.Before(since) {
				recent++
			}
		}
	}
	return active, pending, recent, nil
}

func (m *memoryStore) Activate(_ context.Context, k string, owner, profile []byte, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[k]
	if !ok || a.Status != StatusPending {
		return ErrConflict
	}
	a.Status = StatusActive
	a.ActivatedAt = &now
	_ = json.Unmarshal(owner, &a.OwnerEvent)
	_ = json.Unmarshal(profile, &a.ProfileEvent)
	m.agents[k] = a
	return nil
}

func (m *memoryStore) PairClient(_ context.Context, a, c string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.clients {
		if v.AgentPubkey == a && v.Status == ClientActive && v.ClientPubkey != c {
			v.Status = ClientRevoked
			m.clients[k] = v
		}
	}
	k := a + ":" + c
	v, ok := m.clients[k]
	if !ok {
		return ErrNotFound
	}
	v.Status = ClientActive
	v.InvitationHash = ""
	v.Invitation = keywrap.Envelope{}
	v.PairedAt = &now
	m.clients[k] = v
	return nil
}

func (m *memoryStore) RevokeCurrent(_ context.Context, a string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.clients {
		if v.AgentPubkey == a && v.Status == ClientActive {
			v.Status = ClientRevoked
			v.RevokedAt = &now
			m.clients[k] = v
		}
	}
	return nil
}

func (m *memoryStore) DeleteAgent(_ context.Context, a string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.agents, a)
	return nil
}

func (m *memoryStore) SaveReplacement(_ context.Context, c Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[c.AgentPubkey+":"+c.ClientPubkey] = c
	return nil
}

func (m *memoryStore) CachedRequest(_ context.Context, a, c, id string) (CachedRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	x, ok := m.requests[a+":"+c+":"+id]
	if !ok {
		return x, ErrNotFound
	}
	return x, nil
}

func (m *memoryStore) SaveRequest(_ context.Context, a, c, id string, x CachedRequest, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := a + ":" + c + ":" + id
	if _, ok := m.requests[k]; ok {
		return ErrConflict
	}
	m.requests[k] = x
	return nil
}

func (m *memoryStore) UseAuthEvent(_ context.Context, id string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.auth[id] {
		return ErrConflict
	}
	m.auth[id] = true
	return nil
}

func (m *memoryStore) HTTPResult(_ context.Context, o, k string) (string, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.http[o+":"+k]
	if !ok {
		return "", nil, ErrNotFound
	}
	return v.hash, v.body, nil
}

func (m *memoryStore) SaveHTTPResult(_ context.Context, o, k, h string, b []byte, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.http[o+":"+k] = struct {
		hash string
		body []byte
	}{h, bytes.Clone(b)}
	return nil
}

func (m *memoryStore) Audit(context.Context, Audit) error { return nil }

func testService(t *testing.T) (*Service, *memoryStore, nostr.SecretKey) {
	t.Helper()
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	wrapper, err := keywrap.NewAESWrapper("v1", master)
	require.NoError(t, err)
	store := newMemoryStore()
	relay := nostr.Generate()
	service, err := NewService(Config{PublicURL: "https://relay.example", GitPublicURL: "https://relay.example", AllowedBrowserOrigins: []string{"https://community.example"}, RelayPubkey: relay.Public()}, store, wrapper)
	require.NoError(t, err)
	now := time.Unix(2_000_000_000, 0)
	service.Now = func() time.Time { return now }
	service.AdmitOwner = func(context.Context, nostr.PubKey) bool { return true }
	service.IsMember = func(string, nostr.PubKey) bool { return true }
	return service, store, relay
}

func authRequest(t *testing.T, method, path string, body []byte, secret nostr.SecretKey, now time.Time) *http.Request {
	t.Helper()
	tags := nostr.Tags{{"u", "https://relay.example" + path}, {"method", method}}
	if method != http.MethodGet && method != http.MethodHead {
		hash := sha256.Sum256(body)
		tags = append(tags, nostr.Tag{"payload", hex.EncodeToString(hash[:])})
	}
	event := nostr.Event{Kind: nostr.KindHTTPAuth, CreatedAt: nostr.Timestamp(now.Unix()), Tags: tags}
	require.NoError(t, event.Sign(secret))
	raw, _ := json.Marshal(event)
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(raw))
	request.Header.Set("Origin", "https://community.example")
	return request
}

func rpcEvent(t *testing.T, agent nostr.PubKey, agentSecret, clientSecret nostr.SecretKey, request nip46.Request, now time.Time) nostr.Event {
	t.Helper()
	ck, err := nip44.GenerateConversationKey(agent, clientSecret)
	require.NoError(t, err)
	raw, _ := json.Marshal(request)
	cipher, err := nip44.Encrypt(string(raw), ck)
	require.NoError(t, err)
	event := nostr.Event{Kind: nostr.KindNostrConnect, CreatedAt: nostr.Timestamp(now.Unix()), Tags: nostr.Tags{{"p", agent.Hex()}}, Content: cipher}
	require.NoError(t, event.Sign(clientSecret))
	_ = agentSecret
	return event
}

func decodeBunkerResponse(t *testing.T, event nostr.Event, agent nostr.PubKey, clientSecret nostr.SecretKey) nip46.Response {
	t.Helper()
	ck, err := nip44.GenerateConversationKey(agent, clientSecret)
	require.NoError(t, err)
	plain, err := nip44.Decrypt(event.Content, ck)
	require.NoError(t, err)
	var response nip46.Response
	require.NoError(t, json.Unmarshal([]byte(plain), &response))
	return response
}

func urlParse(value string) (*url.URL, error) { return url.Parse(value) }
