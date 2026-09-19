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

func TestManagedAgentDiscoveryAndBrowserOriginCORS(t *testing.T) {
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

	service.Config.AllowedBrowserOrigins = append(service.Config.AllowedBrowserOrigins, "http://127.0.0.1:*")
	desktop := httptest.NewRequest(http.MethodOptions, "/api/agents/managed", nil)
	desktop.Header.Set("Origin", "http://127.0.0.1:53337")
	desktopRecorder := httptest.NewRecorder()
	service.ServeHTTP(desktopRecorder, desktop)
	require.Equal(t, http.StatusNoContent, desktopRecorder.Code)
	require.Equal(t, "http://127.0.0.1:53337", desktopRecorder.Header().Get("Access-Control-Allow-Origin"))

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
	template := eventTemplate{Kind: 1, CreatedAt: now.Unix(), Tags: [][]string{}, Content: ""}
	raw, _ := json.Marshal(template)
	request := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "forbidden", Method: "sign_event", Params: []string{string(raw)}}, now)
	event, err := service.HandleNIP46(context.Background(), request)
	require.NoError(t, err)
	require.Contains(t, decodeBunkerResponse(t, event, agentSecret.Public(), clientSecret).Error, "kind is not allowed")
	stranger := nostr.Generate()
	encrypt := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "encrypt", Method: "nip44_encrypt", Params: []string{stranger.Public().Hex(), "secret"}}, now)
	event, err = service.HandleNIP46(context.Background(), encrypt)
	require.NoError(t, err)
	require.Contains(t, decodeBunkerResponse(t, event, agentSecret.Public(), clientSecret).Error, "not approved by the owner")
}

func TestManagedAgentSignsOnlyScopedJJAndGitTemplates(t *testing.T) {
	service, _, _ := testService(t)
	agentKey, owner, repositoryOwner := nostr.Generate(), nostr.Generate(), nostr.Generate()
	agent := Agent{Pubkey: agentKey.Public().Hex(), OwnerPubkey: owner.Public().Hex(), Status: StatusActive}
	now := nostr.Timestamp(service.Now().Unix())
	repository := nostr.Event{
		Kind: nostr.KindRepositoryAnnouncement, PubKey: repositoryOwner.Public(), CreatedAt: now,
		Tags: nostr.Tags{{"d", "notes"}, {"buzz-channel", "room"}, {"h", "room"}},
	}
	coordinate := "30617:" + repositoryOwner.Public().Hex() + ":notes"
	service.CurrentEvent = func(kind nostr.Kind, author nostr.PubKey, d string) *nostr.Event {
		if kind == nostr.KindRepositoryAnnouncement && author == repositoryOwner.Public() && d == "notes" {
			return &repository
		}
		return nil
	}
	service.IsMember = func(group string, key nostr.PubKey) bool {
		return group == "room" && key == agentKey.Public()
	}

	jj := nostr.Event{Kind: 3908, CreatedAt: now, Tags: nostr.Tags{
		{"t", "jj-agent-context-page"}, {"r", "scope"}, {"o", "operation"},
		{"d", "checkpoint"}, {"sequence", "0"}, {"a", coordinate}, {"h", "room"},
	}, Content: `{"schema":"jj-sync/agent-context-page"}`}
	require.NoError(t, service.authorizeSigning(agent, jj))

	wrongRepository := jj
	wrongRepository.Tags = jj.Tags.CloneDeep()
	wrongRepository.Tags[5][1] = "30617:" + repositoryOwner.Public().Hex() + ":other"
	require.ErrorContains(t, service.authorizeSigning(agent, wrongRepository), "JJ context")

	extraTag := jj
	extraTag.Tags = append(jj.Tags.CloneDeep(), nostr.Tag{"client", "untrusted"})
	require.ErrorContains(t, service.authorizeSigning(agent, extraTag), "JJ context tags")

	gitAuth := nostr.Event{Kind: nostr.KindHTTPAuth, CreatedAt: now, Tags: nostr.Tags{
		{"u", "https://relay.example/git/" + repositoryOwner.Public().Hex() + "/notes"},
		{"method", http.MethodGet},
	}}
	require.NoError(t, service.authorizeSigning(agent, gitAuth))
	gitAuth.Tags[0][1] = "https://attacker.example/git/" + repositoryOwner.Public().Hex() + "/notes"
	require.ErrorContains(t, service.authorizeSigning(agent, gitAuth), "repository URL")
}

func TestManagedAgentSupportsOwnerOnlyNIP17(t *testing.T) {
	service, store, _ := testService(t)
	owner := nostr.Generate()
	agentSecret := nostr.Generate()
	clientSecret := nostr.Generate()
	now := service.Now()
	key, err := keywrap.Encrypt(agentSecret[:], service.agentAAD(agentSecret.Public().Hex(), owner.Public().Hex()), service.Wrapper)
	require.NoError(t, err)
	store.agents[agentSecret.Public().Hex()] = Agent{Pubkey: agentSecret.Public().Hex(), OwnerPubkey: owner.Public().Hex(), RelayPubkey: service.Config.RelayPubkey.Hex(), Status: StatusActive, Key: key, CreatedAt: now}
	store.clients[agentSecret.Public().Hex()+":"+clientSecret.Public().Hex()] = Client{AgentPubkey: agentSecret.Public().Hex(), ClientPubkey: clientSecret.Public().Hex(), Status: ClientActive, CreatedAt: now}

	sign := func(id string, template eventTemplate) nip46.Response {
		raw, marshalErr := json.Marshal(template)
		require.NoError(t, marshalErr)
		request := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: id, Method: "sign_event", Params: []string{string(raw)}}, now)
		responseEvent, handleErr := service.HandleNIP46(context.Background(), request)
		require.NoError(t, handleErr)
		return decodeBunkerResponse(t, responseEvent, agentSecret.Public(), clientSecret)
	}

	inbox := sign("inbox", eventTemplate{Kind: int(nostr.KindDMRelayList), CreatedAt: now.Unix(), Tags: [][]string{{"relay", "wss://relay.example"}}, Content: ""})
	require.Empty(t, inbox.Error)
	require.NotEmpty(t, inbox.Result)
	badInbox := sign("bad-inbox", eventTemplate{Kind: int(nostr.KindDMRelayList), CreatedAt: now.Unix(), Tags: [][]string{{"relay", "wss://other.example"}}, Content: ""})
	require.Contains(t, badInbox.Error, "inbox relay list")

	seal := sign("seal", eventTemplate{Kind: int(nostr.KindSeal), CreatedAt: now.Add(-36 * time.Hour).Unix(), Tags: [][]string{}, Content: "ciphertext"})
	require.Empty(t, seal.Error)
	badSeal := sign("bad-seal", eventTemplate{Kind: int(nostr.KindSeal), CreatedAt: now.Unix(), Tags: [][]string{{"p", owner.Public().Hex()}}, Content: "ciphertext"})
	require.Contains(t, badSeal.Error, "invalid NIP-17 seal")

	rumor := `{"id":"rumor","pubkey":"` + owner.Public().Hex() + `","created_at":1,"kind":14,"tags":[["p","` + agentSecret.Public().Hex() + `"]],"content":"hello"}`
	ownerConversation, err := nip44.GenerateConversationKey(agentSecret.Public(), owner)
	require.NoError(t, err)
	sealedContent, err := nip44.Encrypt(rumor, ownerConversation)
	require.NoError(t, err)
	sealedEvent := nostr.Event{Kind: nostr.KindSeal, CreatedAt: nostr.Timestamp(now.Add(-time.Hour).Unix()), Tags: nostr.Tags{}, Content: sealedContent}
	require.NoError(t, sealedEvent.Sign(owner))
	sealedJSON, err := json.Marshal(sealedEvent)
	require.NoError(t, err)

	ephemeral := nostr.Generate()
	outerConversation, err := nip44.GenerateConversationKey(agentSecret.Public(), ephemeral)
	require.NoError(t, err)
	wrappedContent, err := nip44.Encrypt(string(sealedJSON), outerConversation)
	require.NoError(t, err)
	giftWrap := nostr.Event{Kind: nostr.KindGiftWrap, CreatedAt: nostr.Timestamp(now.Add(-time.Hour).Unix()), Tags: nostr.Tags{{"p", agentSecret.Public().Hex()}}, Content: wrappedContent}
	require.NoError(t, giftWrap.Sign(ephemeral))
	giftWrapJSON, err := json.Marshal(giftWrap)
	require.NoError(t, err)
	unwrapRequest := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "unwrap", Method: "lapis_nip17_unwrap", Params: []string{string(giftWrapJSON)}}, now)
	unwrapEvent, err := service.HandleNIP46(context.Background(), unwrapRequest)
	require.NoError(t, err)
	unwrap := decodeBunkerResponse(t, unwrapEvent, agentSecret.Public(), clientSecret)
	require.Empty(t, unwrap.Error)
	require.JSONEq(t, string(sealedJSON), unwrap.Result)

	selfRumor := nostr.Event{Kind: 14, PubKey: agentSecret.Public(), CreatedAt: 1, Tags: nostr.Tags{{"p", owner.Public().Hex()}}, Content: "hello"}
	selfRumor.ID = selfRumor.GetID()
	selfRumorJSON, err := json.Marshal(selfRumor)
	require.NoError(t, err)
	selfEncrypt := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "self-encrypt", Method: "nip44_encrypt", Params: []string{agentSecret.Public().Hex(), string(selfRumorJSON)}}, now)
	selfEncryptEvent, err := service.HandleNIP46(context.Background(), selfEncrypt)
	require.NoError(t, err)
	require.Empty(t, decodeBunkerResponse(t, selfEncryptEvent, agentSecret.Public(), clientSecret).Error)

	wrongRecipient := giftWrap
	wrongRecipient.Tags = nostr.Tags{{"p", owner.Public().Hex()}}
	require.NoError(t, wrongRecipient.Sign(ephemeral))
	wrongJSON, err := json.Marshal(wrongRecipient)
	require.NoError(t, err)
	wrongRequest := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "wrong-recipient", Method: "lapis_nip17_unwrap", Params: []string{string(wrongJSON)}}, now)
	wrongEvent, err := service.HandleNIP46(context.Background(), wrongRequest)
	require.NoError(t, err)
	require.Contains(t, decodeBunkerResponse(t, wrongEvent, agentSecret.Public(), clientSecret).Error, "invalid NIP-17")
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

func TestManagedConversationSigning(t *testing.T) {
	service, _, _ := testService(t)
	secret, owner := nostr.Generate(), nostr.Generate()
	agent := Agent{Pubkey: secret.Public().Hex(), OwnerPubkey: owner.Public().Hex(), Status: StatusActive}
	now := nostr.Timestamp(service.Now().Unix())
	root := nostr.Event{Kind: 11, CreatedAt: now, Tags: nostr.Tags{{"h", "room"}}, Content: "Topic"}
	require.NoError(t, root.Sign(owner))
	reaction := nostr.Event{Kind: 7, CreatedAt: now, Tags: nostr.Tags{{"h", "room"}, {"e", root.ID.Hex()}}, Content: "👀"}
	require.NoError(t, reaction.Sign(secret))
	service.EventByID = func(id nostr.ID) *nostr.Event {
		if id == root.ID {
			return &root
		}
		if id == reaction.ID {
			return &reaction
		}
		return nil
	}
	service.IsMember = func(group string, key nostr.PubKey) bool { return group == "room" && key == secret.Public() }
	deletion := nostr.Event{Kind: 5, CreatedAt: now, Tags: nostr.Tags{{"h", "room"}, {"e", reaction.ID.Hex()}, {"k", "7"}}}
	require.NoError(t, service.authorizeSigning(agent, deletion))
	comment := nostr.Event{Kind: 1111, CreatedAt: now, Tags: nostr.Tags{{"h", "room"}, {"E", root.ID.Hex(), "", root.PubKey.Hex()}, {"K", "11"}, {"P", root.PubKey.Hex()}, {"e", root.ID.Hex(), "", root.PubKey.Hex()}, {"k", "11"}, {"p", root.PubKey.Hex()}}, Content: "Reply"}
	require.NoError(t, service.authorizeSigning(agent, comment))
	for _, kind := range []nostr.Kind{11, 1, 27235} {
		invalid := comment
		invalid.Kind = kind
		require.Error(t, service.authorizeSigning(agent, invalid))
	}
	invalid := deletion
	invalid.Tags = nostr.Tags{{"h", "room"}, {"e", root.ID.Hex()}, {"k", "7"}}
	require.Error(t, service.authorizeSigning(agent, invalid))
	reaction.PubKey = owner.Public()
	require.Error(t, service.authorizeSigning(agent, deletion))
	reaction.PubKey = secret.Public()
	root.Tags = nostr.Tags{{"h", "elsewhere"}}
	require.Error(t, service.authorizeSigning(agent, comment))
	root.Tags = nostr.Tags{{"h", "room"}}
	invalid = comment
	invalid.Tags = append(append(nostr.Tags{}, comment.Tags...), nostr.Tag{"E", root.ID.Hex()})
	require.Error(t, service.authorizeSigning(agent, invalid))
	service.IsMember = func(string, nostr.PubKey) bool { return false }
	require.Error(t, service.authorizeSigning(agent, comment))
	require.Error(t, service.authorizeSigning(agent, deletion))
}

func TestManagedRelayToolSigning(t *testing.T) {
	service, _, _ := testService(t)
	agentSecret, owner, repositoryOwner := nostr.Generate(), nostr.Generate(), nostr.Generate()
	agent := Agent{Pubkey: agentSecret.Public().Hex(), OwnerPubkey: owner.Public().Hex(), Status: StatusActive}
	now := nostr.Timestamp(service.Now().Unix())
	repository := nostr.Event{
		Kind: nostr.KindRepositoryAnnouncement, PubKey: repositoryOwner.Public(), CreatedAt: now,
		Tags: nostr.Tags{{"d", "repo"}, {"name", "Repo"}, {"buzz-channel", "room"}, {"h", "room"}},
	}
	coordinate := "30617:" + repositoryOwner.Public().Hex() + ":repo"
	service.CurrentEvent = func(kind nostr.Kind, author nostr.PubKey, d string) *nostr.Event {
		if kind == nostr.KindRepositoryAnnouncement && author == repositoryOwner.Public() && d == "repo" {
			return &repository
		}
		return nil
	}
	service.IsMember = func(group string, key nostr.PubKey) bool { return group == "room" && key == agentSecret.Public() }

	note := nostr.Event{Kind: 30023, CreatedAt: now, Tags: nostr.Tags{{"d", "note"}, {"title", "Note"}, {"h", "room"}}, Content: "Body"}
	require.NoError(t, service.authorizeSigning(agent, note))
	newRepository := nostr.Event{Kind: 30617, CreatedAt: now, Tags: nostr.Tags{{"d", "new-repo"}, {"name", "New repo"}, {"buzz-channel", "room"}, {"h", "room"}}}
	require.NoError(t, service.authorizeSigning(agent, newRepository))
	project := nostr.Event{Kind: 30621, CreatedAt: now, Tags: nostr.Tags{{"d", "project"}, {"name", "Project"}, {"buzz-channel", "room"}, {"h", "room"}, {"a", coordinate}}}
	require.NoError(t, service.authorizeSigning(agent, project))
	issue := nostr.Event{Kind: nostr.KindIssue, PubKey: repositoryOwner.Public(), CreatedAt: now, Tags: nostr.Tags{{"h", "room"}, {"a", coordinate}, {"subject", "Issue"}}, Content: "Body"}
	require.NoError(t, issue.Sign(repositoryOwner))
	service.EventByID = func(id nostr.ID) *nostr.Event {
		if id == issue.ID {
			return &issue
		}
		return nil
	}
	issueTemplate := nostr.Event{Kind: nostr.KindIssue, CreatedAt: now, Tags: issue.Tags.CloneDeep(), Content: issue.Content}
	require.NoError(t, service.authorizeSigning(agent, issueTemplate))
	status := nostr.Event{Kind: nostr.KindStatusClosed, CreatedAt: now, Tags: nostr.Tags{{"h", "room"}, {"a", coordinate}, {"e", issue.ID.Hex(), "", "root"}}}
	require.NoError(t, service.authorizeSigning(agent, status))

	pullRequest := nostr.Event{Kind: 1618, PubKey: repositoryOwner.Public(), CreatedAt: now, Tags: nostr.Tags{{"a", coordinate}, {"h", "room"}}}
	require.NoError(t, pullRequest.Sign(repositoryOwner))
	service.EventByID = func(id nostr.ID) *nostr.Event {
		switch id {
		case issue.ID:
			return &issue
		case pullRequest.ID:
			return &pullRequest
		default:
			return nil
		}
	}
	comment := nostr.Event{Kind: 1111, CreatedAt: now, Tags: nostr.Tags{
		{"h", "room"}, {"a", coordinate},
		{"E", pullRequest.ID.Hex()}, {"K", "1618"}, {"P", repositoryOwner.Public().Hex()},
		{"e", pullRequest.ID.Hex()}, {"k", "1618"}, {"p", repositoryOwner.Public().Hex()},
	}, Content: "Review note"}
	require.NoError(t, service.authorizeSigning(agent, comment))

	invalid := project
	invalid.Tags[2][1] = "elsewhere"
	require.ErrorContains(t, service.authorizeSigning(agent, invalid), "invalid project template")
	invalid = status
	invalid.Tags[1][1] = "30617:" + repositoryOwner.Public().Hex() + ":missing"
	require.ErrorContains(t, service.authorizeSigning(agent, invalid), "invalid issue status template")
	invalid = comment
	invalid.Tags = append(invalid.Tags, nostr.Tag{"client", "untrusted"})
	require.ErrorContains(t, service.authorizeSigning(agent, invalid), "invalid pull-request comment template")
	invalid = comment
	invalid.Tags[3][1] = "1621"
	require.ErrorContains(t, service.authorizeSigning(agent, invalid), "comment root kind is not allowed")
	invalid = note
	invalid.Tags = append(invalid.Tags, nostr.Tag{"published_at", "1"})
	require.ErrorContains(t, service.authorizeSigning(agent, invalid), "invalid long-form note template")
}

func TestManagedNIP46UsesCurrentDirectMessageApprovals(t *testing.T) {
	service, store, _ := testService(t)
	owner, agentKey, peer, client := nostr.Generate(), nostr.Generate(), nostr.Generate(), nostr.Generate()
	now := service.Now()
	key, err := keywrap.Encrypt(agentKey[:], service.agentAAD(agentKey.Public().Hex(), owner.Public().Hex()), service.Wrapper)
	require.NoError(t, err)
	store.agents[agentKey.Public().Hex()] = Agent{Pubkey: agentKey.Public().Hex(), OwnerPubkey: owner.Public().Hex(), RelayPubkey: service.Config.RelayPubkey.Hex(), Status: StatusActive, Key: key, CreatedAt: now}
	store.clients[agentKey.Public().Hex()+":"+client.Public().Hex()] = Client{AgentPubkey: agentKey.Public().Hex(), ClientPubkey: client.Public().Hex(), Status: ClientActive, CreatedAt: now}
	policy, err := json.Marshal(map[string]any{"dm_allowlist": []string{peer.Public().Hex()}})
	require.NoError(t, err)
	instance := nostr.Event{Kind: 30177, CreatedAt: nostr.Timestamp(now.Unix()), Tags: nostr.Tags{{"d", agentKey.Public().Hex()}}, Content: string(policy)}
	require.NoError(t, instance.Sign(owner))
	service.CurrentEvent = func(kind nostr.Kind, author nostr.PubKey, d string) *nostr.Event {
		if kind == 30177 && author == owner.Public() && d == agentKey.Public().Hex() {
			return &instance
		}
		return nil
	}
	rpc := func(id, method, peerKey, body string) nip46.Response {
		event, callErr := service.HandleNIP46(context.Background(), rpcEvent(t, agentKey.Public(), agentKey, client, nip46.Request{ID: id, Method: method, Params: []string{peerKey, body}}, now))
		require.NoError(t, callErr)
		return decodeBunkerResponse(t, event, agentKey.Public(), client)
	}
	rumor := nostr.Event{Kind: 14, PubKey: agentKey.Public(), CreatedAt: nostr.Timestamp(now.Unix()), Tags: nostr.Tags{{"p", peer.Public().Hex()}}, Content: "Approved private reply"}
	rumor.ID = rumor.GetID()
	encoded, err := json.Marshal(rumor)
	require.NoError(t, err)
	encrypted := rpc("approved-encrypt", "nip44_encrypt", peer.Public().Hex(), string(encoded))
	require.Empty(t, encrypted.Error)
	conversation, err := nip44.GenerateConversationKey(agentKey.Public(), peer)
	require.NoError(t, err)
	decoded, err := nip44.Decrypt(encrypted.Result, conversation)
	require.NoError(t, err)
	require.JSONEq(t, string(encoded), decoded)
	rumor.PubKey = peer.Public()
	rumor.Tags = nostr.Tags{{"p", agentKey.Public().Hex()}}
	rumor.ID = rumor.GetID()
	encoded, err = json.Marshal(rumor)
	require.NoError(t, err)
	ciphertext, err := nip44.Encrypt(string(encoded), conversation)
	require.NoError(t, err)
	received := rpc("approved-decrypt", "nip44_decrypt", peer.Public().Hex(), ciphertext)
	require.Empty(t, received.Error)
	require.JSONEq(t, string(encoded), received.Result)
	instance.Content = "{}"
	require.NoError(t, instance.Sign(owner))
	revoked := rpc("revoked-decrypt", "nip44_decrypt", peer.Public().Hex(), ciphertext)
	require.Contains(t, revoked.Error, "not approved by the owner")
}
