package agentbunker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip46"
	"github.com/stretchr/testify/require"
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
	service, err := NewService(Config{PublicURL: "https://relay.example", AllowedBrowserOrigins: []string{"https://community.example"}, RelayPubkey: relay.Public()}, store, wrapper)
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

func TestManagedAgentCreateBootstrapActivationAndReplay(t *testing.T) {
	service, store, _ := testService(t)
	owner := nostr.Generate()
	client := nostr.Generate()
	body, _ := json.Marshal(createRequest{ClientPubkey: client.Public().Hex(), ProfileContent: `{"name":"Helper"}`, ProfileCreatedAt: service.Now().Unix(), IdempotencyKey: "create-1"})
	request := authRequest(t, http.MethodPost, "/api/agents/managed", body, owner, service.Now())
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	var created createResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
	agent := nostr.MustPubKeyFromHex(created.Agent.Pubkey)
	parsed, err := urlParse(created.BunkerURL)
	require.NoError(t, err)
	invitation := parsed.Query().Get("secret")
	require.NotEmpty(t, invitation)
	cachedCreate := store.http[owner.Public().Hex()+":create-1"].body
	require.NotContains(t, string(cachedCreate), invitation)
	retryRequest := authRequest(t, http.MethodPost, "/api/agents/managed", body, owner, service.Now().Add(time.Second))
	retryRecorder := httptest.NewRecorder()
	service.ServeHTTP(retryRecorder, retryRequest)
	require.Equal(t, http.StatusOK, retryRecorder.Code, retryRecorder.Body.String())
	var retried createResponse
	require.NoError(t, json.Unmarshal(retryRecorder.Body.Bytes(), &retried))
	retryURL, err := urlParse(retried.BunkerURL)
	require.NoError(t, err)
	require.Equal(t, invitation, retryURL.Query().Get("secret"))
	connect := rpcEvent(t, agent, nostr.SecretKey{}, client, nip46.Request{ID: "connect-1", Method: "connect", Params: []string{"{}", invitation}}, service.Now())
	response, err := service.HandleNIP46(context.Background(), connect)
	require.NoError(t, err)
	require.Equal(t, "ack", decodeBunkerResponse(t, response, agent, client).Result)
	cached, err := service.HandleNIP46(context.Background(), connect)
	require.NoError(t, err)
	require.Equal(t, response.ID, cached.ID)
	pairedRetry := authRequest(t, http.MethodPost, "/api/agents/managed", body, owner, service.Now().Add(2*time.Second))
	pairedRecorder := httptest.NewRecorder()
	service.ServeHTTP(pairedRecorder, pairedRetry)
	require.Equal(t, http.StatusOK, pairedRecorder.Code, pairedRecorder.Body.String())
	var pairedCreated createResponse
	require.NoError(t, json.Unmarshal(pairedRecorder.Body.Bytes(), &pairedCreated))
	pairedURL, err := urlParse(pairedCreated.BunkerURL)
	require.NoError(t, err)
	require.Empty(t, pairedURL.Query().Get("secret"))
	different := rpcEvent(t, agent, nostr.SecretKey{}, client, nip46.Request{ID: "connect-1", Method: "ping"}, service.Now())
	_, err = service.HandleNIP46(context.Background(), different)
	require.ErrorIs(t, err, ErrReplay)
	profileJSON, _ := json.Marshal(created.ProfileTemplate)
	sign := rpcEvent(t, agent, nostr.SecretKey{}, client, nip46.Request{ID: "sign-1", Method: "sign_event", Params: []string{string(profileJSON)}}, service.Now())
	signedResponse, err := service.HandleNIP46(context.Background(), sign)
	require.NoError(t, err)
	decoded := decodeBunkerResponse(t, signedResponse, agent, client)
	require.Empty(t, decoded.Error)
	var profile nostr.Event
	require.NoError(t, json.Unmarshal([]byte(decoded.Result), &profile))
	require.True(t, profile.VerifySignature())
	ownerEvent := nostr.Event{Kind: 30177, CreatedAt: nostr.Timestamp(service.Now().Unix()), Tags: nostr.Tags{{"d", agent.Hex()}}, Content: "{}"}
	require.NoError(t, ownerEvent.Sign(owner))
	activationBody, _ := json.Marshal(activationRequest{OwnerEvent: ownerEvent, ProfileEvent: profile})
	activation := authRequest(t, http.MethodPost, "/api/agents/managed/"+agent.Hex()+"/activate", activationBody, owner, service.Now())
	activationRecorder := httptest.NewRecorder()
	service.ServeHTTP(activationRecorder, activation)
	require.Equal(t, http.StatusOK, activationRecorder.Code, activationRecorder.Body.String())
	stored, err := store.Agent(context.Background(), agent.Hex())
	require.NoError(t, err)
	require.Equal(t, StatusActive, stored.Status)
	require.True(t, service.VerifyActiveOwnership(context.Background(), agent, owner.Public(), service.Config.RelayPubkey))

	replacementClient := nostr.Generate()
	replacementBody, _ := json.Marshal(replacementRequest{ClientPubkey: replacementClient.Public().Hex(), IdempotencyKey: "replace-1"})
	replacementPath := "/api/agents/managed/" + agent.Hex() + "/connections"
	replacementRequest := authRequest(t, http.MethodPost, replacementPath, replacementBody, owner, service.Now().Add(3*time.Second))
	replacementRecorder := httptest.NewRecorder()
	service.ServeHTTP(replacementRecorder, replacementRequest)
	require.Equal(t, http.StatusCreated, replacementRecorder.Code, replacementRecorder.Body.String())
	var replacement replacementResponse
	require.NoError(t, json.Unmarshal(replacementRecorder.Body.Bytes(), &replacement))
	replacementURL, err := urlParse(replacement.BunkerURL)
	require.NoError(t, err)
	replacementInvitation := replacementURL.Query().Get("secret")
	require.NotEmpty(t, replacementInvitation)
	cachedReplacement := store.http[owner.Public().Hex()+":connection:"+agent.Hex()+":replace-1"].body
	require.NotContains(t, string(cachedReplacement), replacementInvitation)

	replacementRetry := authRequest(t, http.MethodPost, replacementPath, replacementBody, owner, service.Now().Add(4*time.Second))
	replacementRetryRecorder := httptest.NewRecorder()
	service.ServeHTTP(replacementRetryRecorder, replacementRetry)
	require.Equal(t, http.StatusOK, replacementRetryRecorder.Code, replacementRetryRecorder.Body.String())
	var retriedReplacement replacementResponse
	require.NoError(t, json.Unmarshal(replacementRetryRecorder.Body.Bytes(), &retriedReplacement))
	retriedReplacementURL, err := urlParse(retriedReplacement.BunkerURL)
	require.NoError(t, err)
	require.Equal(t, replacementInvitation, retriedReplacementURL.Query().Get("secret"))

	replacementConnect := rpcEvent(t, agent, nostr.SecretKey{}, replacementClient, nip46.Request{ID: "replacement-connect", Method: "connect", Params: []string{"{}", replacementInvitation}}, service.Now())
	replacementConnectResponse, err := service.HandleNIP46(context.Background(), replacementConnect)
	require.NoError(t, err)
	require.Equal(t, "ack", decodeBunkerResponse(t, replacementConnectResponse, agent, replacementClient).Result)
	require.Equal(t, ClientRevoked, store.clients[agent.Hex()+":"+client.Public().Hex()].Status)

	pairedReplacementRetry := authRequest(t, http.MethodPost, replacementPath, replacementBody, owner, service.Now().Add(5*time.Second))
	pairedReplacementRecorder := httptest.NewRecorder()
	service.ServeHTTP(pairedReplacementRecorder, pairedReplacementRetry)
	require.Equal(t, http.StatusOK, pairedReplacementRecorder.Code, pairedReplacementRecorder.Body.String())
	var pairedReplacement replacementResponse
	require.NoError(t, json.Unmarshal(pairedReplacementRecorder.Body.Bytes(), &pairedReplacement))
	pairedReplacementURL, err := urlParse(pairedReplacement.BunkerURL)
	require.NoError(t, err)
	require.Empty(t, pairedReplacementURL.Query().Get("secret"))
}

func TestManagedAgentDiscoveryAndExactOriginCORS(t *testing.T) {
	service, _, _ := testService(t)
	discovery := service.NIP11()
	require.Equal(t, 1, discovery["version"])
	require.Equal(t, "wss://relay.example/nip46", discovery["bunker_url"])
	require.Equal(t, "lapis-managed-v1", discovery["authorization_type"])

	approved := httptest.NewRequest(http.MethodOptions, "/api/agents/managed", nil)
	approved.Header.Set("Origin", "https://community.example")
	approvedRecorder := httptest.NewRecorder()
	service.ServeHTTP(approvedRecorder, approved)
	require.Equal(t, http.StatusNoContent, approvedRecorder.Code)
	require.Equal(t, "https://community.example", approvedRecorder.Header().Get("Access-Control-Allow-Origin"))

	unapproved := httptest.NewRequest(http.MethodOptions, "/api/agents/managed", nil)
	unapproved.Header.Set("Origin", "https://other.example")
	unapprovedRecorder := httptest.NewRecorder()
	service.ServeHTTP(unapprovedRecorder, unapproved)
	require.Equal(t, http.StatusForbidden, unapprovedRecorder.Code)
	require.Empty(t, unapprovedRecorder.Header().Get("Access-Control-Allow-Origin"))
}

func TestManagedAgentPolicyRejectsForbiddenSigningAndNonOwnerEncryption(t *testing.T) {
	service, store, _ := testService(t)
	owner := nostr.Generate()
	agentSecret := nostr.Generate()
	clientSecret := nostr.Generate()
	key, err := keywrap.Encrypt(agentSecret[:], service.agentAAD(agentSecret.Public().Hex(), owner.Public().Hex()), service.Wrapper)
	require.NoError(t, err)
	now := service.Now()
	store.agents[agentSecret.Public().Hex()] = Agent{Pubkey: agentSecret.Public().Hex(), OwnerPubkey: owner.Public().Hex(), RelayPubkey: service.Config.RelayPubkey.Hex(), Status: StatusActive, Key: key, CreatedAt: now}
	store.clients[agentSecret.Public().Hex()+":"+clientSecret.Public().Hex()] = Client{AgentPubkey: agentSecret.Public().Hex(), ClientPubkey: clientSecret.Public().Hex(), Status: ClientActive, CreatedAt: now}
	template := eventTemplate{Kind: 27235, CreatedAt: now.Unix(), Tags: [][]string{}, Content: ""}
	raw, _ := json.Marshal(template)
	request := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "forbidden", Method: "sign_event", Params: []string{string(raw)}}, now)
	event, err := service.HandleNIP46(context.Background(), request)
	require.NoError(t, err)
	require.Contains(t, decodeBunkerResponse(t, event, agentSecret.Public(), clientSecret).Error, "kind is not allowed")
	stranger := nostr.Generate()
	encrypt := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "encrypt", Method: "nip44_encrypt", Params: []string{stranger.Public().Hex(), "secret"}}, now)
	event, err = service.HandleNIP46(context.Background(), encrypt)
	require.NoError(t, err)
	require.Contains(t, decodeBunkerResponse(t, event, agentSecret.Public(), clientSecret).Error, "owner-only")
}

func TestManagedAgentRequiresPairingAndExpiresPendingProfile(t *testing.T) {
	service, store, _ := testService(t)
	owner := nostr.Generate()
	agentSecret := nostr.Generate()
	clientSecret := nostr.Generate()
	now := service.Now()
	key, err := keywrap.Encrypt(agentSecret[:], service.agentAAD(agentSecret.Public().Hex(), owner.Public().Hex()), service.Wrapper)
	require.NoError(t, err)
	profile := eventTemplate{Kind: 0, CreatedAt: now.Unix(), Tags: [][]string{{"lapis-managed", owner.Public().Hex(), service.Config.RelayPubkey.Hex()}}, Content: `{"name":"Expired"}`}
	store.agents[agentSecret.Public().Hex()] = Agent{
		Pubkey: agentSecret.Public().Hex(), OwnerPubkey: owner.Public().Hex(), RelayPubkey: service.Config.RelayPubkey.Hex(),
		Status: StatusPending, ProfileContent: profile.Content, ProfileCreatedAt: profile.CreatedAt, Key: key,
		CreatedAt: now.Add(-25 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}
	store.clients[agentSecret.Public().Hex()+":"+clientSecret.Public().Hex()] = Client{
		AgentPubkey: agentSecret.Public().Hex(), ClientPubkey: clientSecret.Public().Hex(), Status: ClientPending, CreatedAt: now.Add(-25 * time.Hour),
	}
	raw, _ := json.Marshal(profile)
	request := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "unpaired", Method: "sign_event", Params: []string{string(raw)}}, now)
	event, err := service.HandleNIP46(context.Background(), request)
	require.NoError(t, err)
	require.Contains(t, decodeBunkerResponse(t, event, agentSecret.Public(), clientSecret).Error, "not paired")

	client := store.clients[agentSecret.Public().Hex()+":"+clientSecret.Public().Hex()]
	client.Status = ClientActive
	store.clients[agentSecret.Public().Hex()+":"+clientSecret.Public().Hex()] = client
	request = rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "expired", Method: "sign_event", Params: []string{string(raw)}}, now)
	event, err = service.HandleNIP46(context.Background(), request)
	require.NoError(t, err)
	require.Contains(t, decodeBunkerResponse(t, event, agentSecret.Public(), clientSecret).Error, "draft expired")
}

func urlParse(value string) (*url.URL, error) { return url.Parse(value) }
