package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip42"
	"fiatjaf.com/nostr/nip44"
	"github.com/stretchr/testify/require"
)

type fakeNIP46Remote struct {
	secret nostr.SecretKey
	mutate bool
}

func (remote *fakeNIP46Remote) GetPublicKey(context.Context) (nostr.PubKey, error) {
	return remote.secret.Public(), nil
}

func (remote *fakeNIP46Remote) SignEvent(_ context.Context, event *nostr.Event) error {
	if remote.mutate {
		event.Content = "changed by remote"
	}
	return event.Sign(remote.secret)
}

func (remote *fakeNIP46Remote) NIP44Encrypt(_ context.Context, recipient nostr.PubKey, plaintext string) (string, error) {
	conversationKey, err := nip44.GenerateConversationKey(recipient, remote.secret)
	if err != nil {
		return "", err
	}
	return nip44.Encrypt(plaintext, conversationKey)
}

func (remote *fakeNIP46Remote) NIP44Decrypt(_ context.Context, sender nostr.PubKey, ciphertext string) (string, error) {
	conversationKey, err := nip44.GenerateConversationKey(sender, remote.secret)
	if err != nil {
		return "", err
	}
	return nip44.Decrypt(ciphertext, conversationKey)
}

func (*fakeNIP46Remote) SwitchRelays(context.Context) ([]string, error) {
	return []string{"wss://relay.example"}, nil
}

type fakeNIP46Connector struct {
	remote     NIP46Remote
	bunkerURLs []string
}

func (connector *fakeNIP46Connector) Connect(_ context.Context, _ nostr.SecretKey, bunkerURL string) (NIP46Remote, error) {
	connector.bunkerURLs = append(connector.bunkerURLs, bunkerURL)
	return connector.remote, nil
}

func TestNIP46SignerVerifiesRemoteResultAndPolicy(t *testing.T) {
	secret := nostr.Generate()
	remote := &fakeNIP46Remote{secret: secret}
	signer := &NIP46Signer{
		AccountID: "account-a", Pubkey: secret.Public(), Remote: remote,
		Policy: ApplicationSigningPolicy{PublicURL: "https://relay.example", RelayURL: "wss://relay.example"},
	}
	event := nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Now(), Content: "hello"}
	signed, err := signer.SignEvent(context.Background(), event, SignContext{
		AccountID: "account-a", Purpose: SignPurposeNote, RequestedKind: nostr.KindTextNote,
	})
	require.NoError(t, err)
	require.Equal(t, secret.Public(), signed.PubKey)
	require.True(t, signed.VerifySignature())

	remote.mutate = true
	_, err = signer.SignEvent(context.Background(), event, SignContext{
		AccountID: "account-a", Purpose: SignPurposeNote, RequestedKind: nostr.KindTextNote,
	})
	require.ErrorIs(t, err, ErrSignerUnavailable)
	_, err = signer.SignEvent(context.Background(), event, SignContext{
		AccountID: "account-a", Purpose: SignPurposeProfile, RequestedKind: nostr.KindTextNote,
	})
	require.ErrorIs(t, err, ErrSigningDenied)
}

func TestManagedSessionSignsNIP42Challenge(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store)
	secret := nostr.Generate()
	accountID := "managed-account"
	key, err := EncryptManagedKey(accountID, secret.Public().Hex(), secret[:], service.Wrapper)
	require.NoError(t, err)
	account, err := store.CreateManagedAccount(context.Background(), ExternalIdentity{
		AccountID: accountID, Provider: "google", ProviderID: "subject",
	}, key)
	require.NoError(t, err)
	cookie := createTestSession(t, service, store, account)
	challenge := "relay-challenge-0123456789"
	request := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/relay/sign-auth", bytes.NewReader([]byte(`{"challenge":"`+challenge+`"}`)))
	request.Header.Set("Origin", "https://relay.example")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var payload struct {
		Event nostr.Event `json:"event"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	pubkey, err := nip42.ValidateAuthEvent(payload.Event, challenge, "wss://relay.example")
	require.NoError(t, err)
	require.Equal(t, account.Pubkey, pubkey)
	require.NotEmpty(t, store.audit)
	require.Equal(t, "sign_relay_auth", store.audit[len(store.audit)-1].Operation)
	require.Equal(t, SignerManaged, store.audit[len(store.audit)-1].SignerType)
}

func TestExternalAccountConnectsNIP46WithoutPersistingConnectionSecret(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store)
	secret := nostr.Generate()
	account, err := store.CreateExternalAccount(context.Background(), secret.Public())
	require.NoError(t, err)
	connector := &fakeNIP46Connector{remote: &fakeNIP46Remote{secret: secret}}
	service.NIP46 = connector
	cookie := createTestSession(t, service, store, account)
	bunkerURL := "bunker://" + nostr.Generate().Public().Hex() + "?relay=" + url.QueryEscape("wss://relay.example") + "&secret=connection-secret"
	request := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/nip46/connect", bytes.NewReader([]byte(`{"bunker_url":"`+bunkerURL+`"}`)))
	request.Header.Set("Origin", "https://relay.example")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	binding, err := store.SignerBindingByAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, SignerNIP46, binding.Type)
	require.NotContains(t, string(binding.Metadata), "connection-secret")
	var metadata nip46BindingMetadata
	require.NoError(t, json.Unmarshal(binding.Metadata, &metadata))
	parsed, err := url.Parse(metadata.BunkerURL)
	require.NoError(t, err)
	require.Empty(t, parsed.Query().Get("secret"))
	require.Equal(t, bunkerURL, connector.bunkerURLs[0])
}

func createTestSession(t *testing.T, service *Service, store *memoryStore, account Account) *http.Cookie {
	t.Helper()
	token := "test-session-token"
	hash := sha256.Sum256([]byte(token))
	now := time.Now()
	require.NoError(t, store.CreateSession(context.Background(), Session{
		ID: "session-id", AccountID: account.ID, TokenHash: hash, AuthProvider: "test",
		ExpiresAt: now.Add(time.Hour), IdleExpires: now.Add(time.Hour), CreatedAt: now, LastSeenAt: now,
	}))
	return &http.Cookie{Name: SessionCookieName, Value: token}
}
