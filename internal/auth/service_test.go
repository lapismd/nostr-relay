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
	flows       map[[32]byte]OAuthFlow
	accounts    map[string]Account
	pubkeys     map[nostr.PubKey]string
	identities  map[string]ExternalIdentity
	keys        map[string]ManagedKey
	sessions    map[[32]byte]Session
	challenges  map[[32]byte]NostrChallenge
	credentials map[string]GitHubCredential
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		flows: make(map[[32]byte]OAuthFlow), accounts: make(map[string]Account), pubkeys: make(map[nostr.PubKey]string),
		identities: make(map[string]ExternalIdentity), keys: make(map[string]ManagedKey), sessions: make(map[[32]byte]Session),
		challenges: make(map[[32]byte]NostrChallenge), credentials: make(map[string]GitHubCredential),
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
	return account, nil
}
func (store *memoryStore) CreateExternalAccount(_ context.Context, pubkey nostr.PubKey) (Account, error) {
	if id, ok := store.pubkeys[pubkey]; ok {
		return store.accounts[id], nil
	}
	account := Account{ID: "external-account", Pubkey: pubkey, CreatedAt: time.Now()}
	store.accounts[account.ID] = account
	store.pubkeys[pubkey] = account.ID
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
	service, err := NewService(ServiceConfig{PublicURL: "https://relay.example", GitHubClientID: "github-client"}, store, wrapper, providers...)
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
