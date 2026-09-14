package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

type memoryStore struct {
	flows         map[[32]byte]OAuthFlow
	accounts      map[string]Account
	pubkeys       map[nostr.PubKey]string
	identities    map[string]ExternalIdentity
	keys          map[string]ManagedKey
	sessions      map[[32]byte]Session
	challenges    map[[32]byte]NostrChallenge
	credentials   map[string]GitHubCredential
	bindings      map[string]SignerBinding
	passkeys      map[string]map[[32]byte]PasskeyRecord
	webAuthnFlows map[[32]byte]WebAuthnFlow
	transfers     map[string]CustodyTransfer
	audit         []AuditEvent
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		flows: make(map[[32]byte]OAuthFlow), accounts: make(map[string]Account), pubkeys: make(map[nostr.PubKey]string),
		identities: make(map[string]ExternalIdentity), keys: make(map[string]ManagedKey), sessions: make(map[[32]byte]Session),
		challenges: make(map[[32]byte]NostrChallenge), credentials: make(map[string]GitHubCredential),
		bindings: make(map[string]SignerBinding),
		passkeys: make(map[string]map[[32]byte]PasskeyRecord), webAuthnFlows: make(map[[32]byte]WebAuthnFlow),
		transfers: make(map[string]CustodyTransfer),
	}
}

func (store *memoryStore) BeginOAuthFlow(_ context.Context, flow OAuthFlow) error {
	store.flows[flow.StateHash] = flow
	return nil
}
func (store *memoryStore) ConsumeOAuthFlow(_ context.Context, hash [32]byte, now time.Time) (OAuthFlow, error) {
	flow, ok := store.flows[hash]
	delete(store.flows, hash)
	if !ok || !flow.ExpiresAt.After(now) {
		return flow, ErrNotFound
	}
	return flow, nil
}
func identityKey(provider, id string) string { return provider + "\x00" + id }
func (store *memoryStore) FindAccountByIdentity(_ context.Context, provider, id string) (Account, error) {
	identity, ok := store.identities[identityKey(provider, id)]
	if !ok {
		return Account{}, ErrNotFound
	}
	return store.accounts[identity.AccountID], nil
}
func (store *memoryStore) AccountByID(_ context.Context, id string) (Account, error) {
	account, ok := store.accounts[id]
	if !ok {
		return account, ErrNotFound
	}
	return account, nil
}
func (store *memoryStore) AccountByPubkey(_ context.Context, pubkey nostr.PubKey) (Account, error) {
	id, ok := store.pubkeys[pubkey]
	if !ok {
		return Account{}, ErrNotFound
	}
	return store.accounts[id], nil
}
func (store *memoryStore) CreateManagedAccount(_ context.Context, identity ExternalIdentity, key ManagedKey) (Account, error) {
	pubkey, _ := nostr.PubKeyFromHex(key.Pubkey)
	now := time.Now()
	account := Account{ID: identity.AccountID, Pubkey: pubkey, CreatedAt: now, UpdatedAt: now}
	store.accounts[account.ID] = account
	store.pubkeys[pubkey] = account.ID
	store.identities[identityKey(identity.Provider, identity.ProviderID)] = identity
	store.keys[account.ID] = key
	store.bindings[account.ID] = SignerBinding{AccountID: account.ID, Type: SignerManaged, Pubkey: pubkey, Status: SignerActive}
	return account, nil
}
func (store *memoryStore) CreateExternalAccount(_ context.Context, pubkey nostr.PubKey) (Account, error) {
	if id, ok := store.pubkeys[pubkey]; ok {
		return store.accounts[id], nil
	}
	account := Account{ID: "external-account", Pubkey: pubkey, CreatedAt: time.Now()}
	store.accounts[account.ID] = account
	store.pubkeys[pubkey] = account.ID
	store.bindings[account.ID] = SignerBinding{AccountID: account.ID, Type: SignerExternal, Pubkey: pubkey, Status: SignerActive}
	return account, nil
}
func (store *memoryStore) LinkIdentity(_ context.Context, identity ExternalIdentity) error {
	key := identityKey(identity.Provider, identity.ProviderID)
	if _, ok := store.identities[key]; ok {
		return ErrIdentityConflict
	}
	store.identities[key] = identity
	return nil
}
func (store *memoryStore) ExternalIdentities(_ context.Context, accountID string) ([]ExternalIdentity, error) {
	var result []ExternalIdentity
	for _, identity := range store.identities {
		if identity.AccountID == accountID {
			result = append(result, identity)
		}
	}
	return result, nil
}
func (store *memoryStore) RemoveIdentity(context.Context, string, string) error { return nil }
func (store *memoryStore) LoadManagedKey(_ context.Context, accountID string) (ManagedKey, error) {
	key, ok := store.keys[accountID]
	if !ok {
		return key, ErrNotFound
	}
	return key, nil
}
func (store *memoryStore) SignerBindingByAccount(_ context.Context, accountID string) (SignerBinding, error) {
	binding, ok := store.bindings[accountID]
	if !ok {
		return binding, ErrNotFound
	}
	return binding, nil
}
func (store *memoryStore) SetNIP46Binding(_ context.Context, accountID string, pubkey nostr.PubKey, metadata []byte, now time.Time) error {
	binding, ok := store.bindings[accountID]
	if !ok || binding.Pubkey != pubkey || binding.Type == SignerManaged {
		return ErrSigningDenied
	}
	binding.Type, binding.Status, binding.Metadata, binding.UpdatedAt = SignerNIP46, SignerActive, metadata, now
	store.bindings[accountID] = binding
	return nil
}
func (store *memoryStore) SetExternalBinding(_ context.Context, accountID string, pubkey nostr.PubKey, now time.Time) error {
	binding, ok := store.bindings[accountID]
	if !ok || binding.Pubkey != pubkey || binding.Type != SignerNIP46 {
		return ErrSigningDenied
	}
	binding.Type, binding.Status, binding.Metadata, binding.UpdatedAt = SignerExternal, SignerActive, nil, now
	store.bindings[accountID] = binding
	return nil
}
func (store *memoryStore) CreateSession(_ context.Context, session Session) error {
	store.sessions[session.TokenHash] = session
	return nil
}
func (store *memoryStore) SessionByTokenHash(_ context.Context, hash [32]byte, now time.Time) (Session, Account, error) {
	session, ok := store.sessions[hash]
	if !ok || !session.ExpiresAt.After(now) || !session.IdleExpires.After(now) {
		return session, Account{}, ErrNotFound
	}
	return session, store.accounts[session.AccountID], nil
}
func (*memoryStore) TouchSession(context.Context, string, time.Time, time.Time) error { return nil }
func (*memoryStore) RevokeSession(context.Context, string, time.Time) error           { return nil }
func (*memoryStore) RevokeAccountSessions(context.Context, string, string, time.Time) error {
	return nil
}
func (store *memoryStore) SaveNostrChallenge(_ context.Context, challenge NostrChallenge) error {
	store.challenges[challenge.ChallengeHash] = challenge
	return nil
}
func (store *memoryStore) ConsumeNostrChallenge(_ context.Context, hash [32]byte, pubkey nostr.PubKey, now time.Time) error {
	challenge, ok := store.challenges[hash]
	delete(store.challenges, hash)
	if !ok || challenge.Pubkey != pubkey || !challenge.ExpiresAt.After(now) {
		return ErrNotFound
	}
	return nil
}
func (store *memoryStore) SaveGitHubCredential(_ context.Context, credential GitHubCredential) error {
	store.credentials[credential.AccountID] = credential
	return nil
}
func (store *memoryStore) GitHubCredentialByPubkey(_ context.Context, pubkey nostr.PubKey) (GitHubCredential, error) {
	id, ok := store.pubkeys[pubkey]
	if !ok {
		return GitHubCredential{}, ErrNotFound
	}
	return store.GitHubCredentialByAccount(context.Background(), id)
}
func (store *memoryStore) GitHubCredentialByAccount(_ context.Context, id string) (GitHubCredential, error) {
	value, ok := store.credentials[id]
	if !ok {
		return value, ErrNotFound
	}
	return value, nil
}
func (store *memoryStore) DisconnectGitHubCredential(_ context.Context, id string, _ time.Time) error {
	value := store.credentials[id]
	value.Status = "disconnected"
	store.credentials[id] = value
	return nil
}
func (store *memoryStore) UpdateGitHubCredentialStatus(_ context.Context, id, status string, updatedAt time.Time) error {
	value, ok := store.credentials[id]
	if !ok {
		return ErrNotFound
	}
	value.Status = status
	value.UpdatedAt = updatedAt
	store.credentials[id] = value
	return nil
}
func (store *memoryStore) PasskeysByAccount(_ context.Context, accountID string) ([]PasskeyRecord, error) {
	values := store.passkeys[accountID]
	result := make([]PasskeyRecord, 0, len(values))
	for _, record := range values {
		result = append(result, record)
	}
	return result, nil
}
func (store *memoryStore) SavePasskey(_ context.Context, record PasskeyRecord) error {
	if store.passkeys[record.AccountID] == nil {
		store.passkeys[record.AccountID] = make(map[[32]byte]PasskeyRecord)
	}
	store.passkeys[record.AccountID][record.CredentialIDHash] = record
	return nil
}
func (store *memoryStore) BeginWebAuthnFlow(_ context.Context, flow WebAuthnFlow) error {
	store.webAuthnFlows[flow.FlowHash] = flow
	return nil
}
func (store *memoryStore) ConsumeWebAuthnFlow(_ context.Context, hash [32]byte, kind, accountID string, now time.Time) (WebAuthnFlow, error) {
	flow, ok := store.webAuthnFlows[hash]
	delete(store.webAuthnFlows, hash)
	if !ok || flow.Kind != kind || flow.AccountID != accountID || !flow.ExpiresAt.After(now) {
		return WebAuthnFlow{}, ErrNotFound
	}
	return flow, nil
}
func (store *memoryStore) CreateCustodyTransfer(_ context.Context, transfer CustodyTransfer) error {
	binding, ok := store.bindings[transfer.AccountID]
	if !ok || binding.Type != SignerManaged || binding.Status == SignerDisabled {
		return ErrSigningDenied
	}
	for id, active := range store.transfers {
		if active.AccountID == transfer.AccountID && active.State == "key_received_by_device" {
			active.State = "cancelled"
			store.transfers[id] = active
		}
	}
	binding.Status = SignerMigrationPending
	store.bindings[transfer.AccountID] = binding
	store.transfers[transfer.ID] = transfer
	return nil
}
func (store *memoryStore) CustodyTransferByID(_ context.Context, transferID, accountID string, now time.Time) (CustodyTransfer, error) {
	transfer, ok := store.transfers[transferID]
	if !ok || transfer.AccountID != accountID || !transfer.ExpiresAt.After(now) {
		return CustodyTransfer{}, ErrNotFound
	}
	return transfer, nil
}
func (store *memoryStore) CompleteCustodyTransfer(_ context.Context, transferID, accountID string, pubkey nostr.PubKey, metadata []byte, now time.Time) error {
	transfer, ok := store.transfers[transferID]
	binding := store.bindings[accountID]
	key, hasKey := store.keys[accountID]
	if !ok || transfer.AccountID != accountID || transfer.State != "key_received_by_device" || !transfer.ExpiresAt.After(now) ||
		binding.Type != SignerManaged || binding.Status != SignerMigrationPending || binding.Pubkey != pubkey || !hasKey || key.Pubkey != pubkey.Hex() {
		return ErrSigningDenied
	}
	binding.Type, binding.Status, binding.Metadata, binding.UpdatedAt = SignerNIP46, SignerActive, metadata, now
	store.bindings[accountID] = binding
	delete(store.keys, accountID)
	transfer.State = "complete"
	transfer.CompletedAt = &now
	store.transfers[transferID] = transfer
	return nil
}
func (store *memoryStore) SaveAuditEvent(_ context.Context, event AuditEvent) error {
	store.audit = append(store.audit, event)
	return nil
}

type fakeProvider struct{ identity ProviderIdentity }

func (fakeProvider) Name() string { return "github" }
func (fakeProvider) AuthorizationURL(state, nonce, challenge string) string {
	return "https://github.example/authorize?state=" + url.QueryEscape(state) + "&nonce=" + url.QueryEscape(nonce) + "&challenge=" + url.QueryEscape(challenge)
}
func (provider fakeProvider) Exchange(context.Context, string, string, string) (ProviderIdentity, error) {
	return provider.identity, nil
}

func newTestService(t *testing.T, store Store, providers ...OAuthProvider) *Service {
	t.Helper()
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	wrapper, err := NewAESKeyWrapper("v1", master)
	require.NoError(t, err)
	service, err := NewService(ServiceConfig{PublicURL: "https://relay.example", GitHubClientID: "github-client", AllowedBrowserOrigins: []string{"https://community.example"}}, store, wrapper, providers...)
	require.NoError(t, err)
	return service
}

func TestOAuthSignupCreatesManagedNostrAccountAndGitHubLink(t *testing.T) {
	store := newMemoryStore()
	provider := fakeProvider{identity: ProviderIdentity{Provider: "github", ProviderID: "123", Login: "octo", Token: &oauth2.Token{AccessToken: "user-token", Expiry: time.Now().Add(time.Hour)}}}
	service := newTestService(t, store, provider)

	begin := httptest.NewRecorder()
	service.ServeHTTP(begin, httptest.NewRequest(http.MethodGet, "https://relay.example/api/auth/login/github?return_to=/projects", nil))
	require.Equal(t, http.StatusFound, begin.Code)
	location, err := url.Parse(begin.Header().Get("Location"))
	require.NoError(t, err)
	state := location.Query().Get("state")
	require.NotEmpty(t, state)

	callback := httptest.NewRecorder()
	service.ServeHTTP(callback, httptest.NewRequest(http.MethodGet, "https://relay.example/api/auth/callback/github?code=test&state="+url.QueryEscape(state), nil))
	require.Equal(t, http.StatusFound, callback.Code)
	require.Equal(t, "/projects", callback.Header().Get("Location"))
	require.NotEmpty(t, callback.Result().Cookies())
	account, err := store.FindAccountByIdentity(context.Background(), "github", "123")
	require.NoError(t, err)
	require.NotEqual(t, nostr.ZeroPK, account.Pubkey)
	key, err := store.LoadManagedKey(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, account.Pubkey.Hex(), key.Pubkey)
	require.Equal(t, "active", store.credentials[account.ID].Status)

	replay := httptest.NewRecorder()
	service.ServeHTTP(replay, httptest.NewRequest(http.MethodGet, "https://relay.example/api/auth/callback/github?code=test&state="+url.QueryEscape(state), nil))
	require.Equal(t, http.StatusBadRequest, replay.Code)
}

func TestNostrLoginUsesBodyBoundOneTimeChallengeAndNoManagedKey(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store)
	secret := nostr.Generate()
	challengeRequest := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/nostr/challenge", bytes.NewReader([]byte(`{"pubkey":"`+secret.Public().Hex()+`"}`)))
	challengeResponse := httptest.NewRecorder()
	service.ServeHTTP(challengeResponse, challengeRequest)
	require.Equal(t, http.StatusCreated, challengeResponse.Code)
	var challenge struct {
		Challenge string `json:"challenge"`
	}
	require.NoError(t, json.Unmarshal(challengeResponse.Body.Bytes(), &challenge))
	body := []byte(`{"challenge":"` + challenge.Challenge + `"}`)
	digest := sha256.Sum256(body)
	event := nostr.Event{Kind: nostr.KindHTTPAuth, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"u", "https://relay.example/api/auth/nostr/login"}, {"method", "POST"}, {"payload", base64.RawURLEncoding.EncodeToString(digest[:])}}}
	// NIP-98 uses a lowercase hex payload hash.
	event.Tags[2][1] = fmt.Sprintf("%x", digest[:])
	require.NoError(t, event.Sign(secret))
	encoded, _ := json.Marshal(event)
	loginRequest := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/nostr/login", bytes.NewReader(body))
	loginRequest.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(encoded))
	loginResponse := httptest.NewRecorder()
	service.ServeHTTP(loginResponse, loginRequest)
	require.Equal(t, http.StatusOK, loginResponse.Code)
	require.Len(t, store.keys, 0)

	replayRequest := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/nostr/login", bytes.NewReader(body))
	replayRequest.Header.Set("Authorization", loginRequest.Header.Get("Authorization"))
	replayResponse := httptest.NewRecorder()
	service.ServeHTTP(replayResponse, replayRequest)
	require.Equal(t, http.StatusUnauthorized, replayResponse.Code)
}

func TestNostrLoginFromLoopbackDesktopUsesCrossSiteSessionCookie(t *testing.T) {
	store := newMemoryStore()
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	wrapper, err := NewAESKeyWrapper("v1", master)
	require.NoError(t, err)
	service, err := NewService(ServiceConfig{
		PublicURL:             "https://relay.example",
		AllowedBrowserOrigins: []string{"https://community.example", "http://127.0.0.1:*"},
	}, store, wrapper)
	require.NoError(t, err)
	secret := nostr.Generate()

	challengeRequest := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/nostr/challenge", bytes.NewReader([]byte(`{"pubkey":"`+secret.Public().Hex()+`"}`)))
	challengeRequest.Header.Set("Origin", "http://127.0.0.1:53337")
	challengeResponse := httptest.NewRecorder()
	service.ServeHTTP(challengeResponse, challengeRequest)
	require.Equal(t, http.StatusCreated, challengeResponse.Code)
	require.Equal(t, "http://127.0.0.1:53337", challengeResponse.Header().Get("Access-Control-Allow-Origin"))
	var challenge struct {
		Challenge string `json:"challenge"`
	}
	require.NoError(t, json.Unmarshal(challengeResponse.Body.Bytes(), &challenge))
	body := []byte(`{"challenge":"` + challenge.Challenge + `"}`)
	digest := sha256.Sum256(body)
	event := nostr.Event{Kind: nostr.KindHTTPAuth, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"u", "https://relay.example/api/auth/nostr/login"}, {"method", "POST"}, {"payload", fmt.Sprintf("%x", digest[:])}}}
	require.NoError(t, event.Sign(secret))
	encoded, _ := json.Marshal(event)

	loginRequest := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/nostr/login", bytes.NewReader(body))
	loginRequest.Header.Set("Origin", "http://127.0.0.1:53337")
	loginRequest.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(encoded))
	loginResponse := httptest.NewRecorder()
	service.ServeHTTP(loginResponse, loginRequest)
	require.Equal(t, http.StatusOK, loginResponse.Code)
	cookies := loginResponse.Result().Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, http.SameSiteNoneMode, cookies[0].SameSite)
	require.True(t, cookies[0].Secure)

	sessionRequest := httptest.NewRequest(http.MethodGet, "https://relay.example/api/auth/session", nil)
	sessionRequest.Header.Set("Origin", "http://127.0.0.1:53337")
	sessionRequest.AddCookie(cookies[0])
	sessionResponse := httptest.NewRecorder()
	service.ServeHTTP(sessionResponse, sessionRequest)
	require.Equal(t, http.StatusOK, sessionResponse.Code, sessionResponse.Body.String())
}

func TestCredentialCodecBindsCiphertextToAccountAndPurpose(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store)
	codec := CredentialCodec{Wrapper: service.Wrapper}
	ciphertext, err := codec.Encrypt("account-a", "github-access", "secret-token")
	require.NoError(t, err)
	require.NotContains(t, ciphertext, "secret-token")
	plain, err := codec.Decrypt("account-a", "github-access", ciphertext)
	require.NoError(t, err)
	require.Equal(t, "secret-token", plain)
	_, err = codec.Decrypt("account-b", "github-access", ciphertext)
	require.True(t, errors.Is(err, errors.New("decrypt credential")) || err != nil)
}

func TestGitHubCredentialModeAndFreshUserToken(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store)
	secret := nostr.Generate()
	account, err := store.CreateExternalAccount(context.Background(), secret.Public())
	require.NoError(t, err)
	codec := CredentialCodec{Wrapper: service.Wrapper}
	credential, err := GitHubCredentialFromToken(account.ID, "123", "octo", "github-client", &oauth2.Token{
		AccessToken: "github-user-token", Expiry: time.Now().Add(time.Hour),
	}, codec)
	require.NoError(t, err)
	require.NoError(t, store.SaveGitHubCredential(context.Background(), credential))

	mode, err := service.GitHubCredentialMode(context.Background(), account.Pubkey)
	require.NoError(t, err)
	require.Equal(t, "user", mode)
	token, err := service.ResolveGitHubUserToken(context.Background(), account.Pubkey)
	require.NoError(t, err)
	require.Equal(t, "github-user-token", token)

	require.NoError(t, store.DisconnectGitHubCredential(context.Background(), account.ID, time.Now()))
	mode, err = service.GitHubCredentialMode(context.Background(), account.Pubkey)
	require.NoError(t, err)
	require.Equal(t, "system", mode)
	_, err = service.ResolveGitHubUserToken(context.Background(), account.Pubkey)
	require.Error(t, err)
}

func TestExpiredGitHubUserTokenIsRefreshed(t *testing.T) {
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
		require.Equal(t, "old-refresh-token", r.Form.Get("refresh_token"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-user-token", "refresh_token": "new-refresh-token",
			"token_type": "bearer", "expires_in": 28800,
		})
	}))
	defer refreshServer.Close()
	store := newMemoryStore()
	provider := &GitHubProvider{config: &oauth2.Config{
		ClientID: "github-client", ClientSecret: "client-secret",
		Endpoint: oauth2.Endpoint{TokenURL: refreshServer.URL, AuthStyle: oauth2.AuthStyleInParams},
	}, client: refreshServer.Client()}
	service := newTestService(t, store, provider)
	secret := nostr.Generate()
	account, err := store.CreateExternalAccount(context.Background(), secret.Public())
	require.NoError(t, err)
	credential, err := GitHubCredentialFromToken(account.ID, "123", "octo", "github-client", &oauth2.Token{
		AccessToken: "expired-user-token", RefreshToken: "old-refresh-token", Expiry: time.Now().Add(-time.Minute),
	}, CredentialCodec{Wrapper: service.Wrapper})
	require.NoError(t, err)
	require.NoError(t, store.SaveGitHubCredential(context.Background(), credential))

	token, err := service.ResolveGitHubUserToken(context.Background(), account.Pubkey)
	require.NoError(t, err)
	require.Equal(t, "new-user-token", token)
	stored := store.credentials[account.ID]
	require.Equal(t, "active", stored.Status)
	plain, err := (CredentialCodec{Wrapper: service.Wrapper}).Decrypt(account.ID, "github-refresh", stored.EncryptedRefreshToken)
	require.NoError(t, err)
	require.Equal(t, "new-refresh-token", plain)
	require.NotEmpty(t, store.audit)
	require.Equal(t, "github_credential_refresh", store.audit[len(store.audit)-1].Operation)
}
