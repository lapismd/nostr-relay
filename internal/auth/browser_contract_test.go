package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestBrowserCORSUsesExactCredentialedOrigins(t *testing.T) {
	service := newTestService(t, newMemoryStore())

	preflight := httptest.NewRequest(http.MethodOptions, "https://relay.example/api/auth/session", nil)
	preflight.Header.Set("Origin", "https://community.example")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodGet)
	response := httptest.NewRecorder()
	service.ServeHTTP(response, preflight)
	require.Equal(t, http.StatusNoContent, response.Code)
	require.Equal(t, "https://community.example", response.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", response.Header().Get("Access-Control-Allow-Credentials"))
	require.Contains(t, response.Header().Values("Vary"), "Origin")

	denied := httptest.NewRequest(http.MethodOptions, "https://relay.example/api/auth/session", nil)
	denied.Header.Set("Origin", "https://attacker.example")
	deniedResponse := httptest.NewRecorder()
	service.ServeHTTP(deniedResponse, denied)
	require.Equal(t, http.StatusForbidden, deniedResponse.Code)
	require.Empty(t, deniedResponse.Header().Get("Access-Control-Allow-Origin"))
}

func TestBrowserCORSAllowsConfiguredLoopbackDesktopPorts(t *testing.T) {
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

	preflight := httptest.NewRequest(http.MethodOptions, "https://relay.example/api/auth/session", nil)
	preflight.Header.Set("Origin", "http://127.0.0.1:53337")
	response := httptest.NewRecorder()
	service.ServeHTTP(response, preflight)
	require.Equal(t, http.StatusNoContent, response.Code)
	require.Equal(t, "http://127.0.0.1:53337", response.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", response.Header().Get("Access-Control-Allow-Credentials"))

	noPort := httptest.NewRequest(http.MethodOptions, "https://relay.example/api/auth/session", nil)
	noPort.Header.Set("Origin", "http://127.0.0.1")
	noPortResponse := httptest.NewRecorder()
	service.ServeHTTP(noPortResponse, noPort)
	require.Equal(t, http.StatusForbidden, noPortResponse.Code)
	require.Empty(t, noPortResponse.Header().Get("Access-Control-Allow-Origin"))

	otherLoopback := httptest.NewRequest(http.MethodOptions, "https://relay.example/api/auth/session", nil)
	otherLoopback.Header.Set("Origin", "http://127.0.0.2:53337")
	otherLoopbackResponse := httptest.NewRecorder()
	service.ServeHTTP(otherLoopbackResponse, otherLoopback)
	require.Equal(t, http.StatusForbidden, otherLoopbackResponse.Code)
	require.Empty(t, otherLoopbackResponse.Header().Get("Access-Control-Allow-Origin"))
}

func TestPublicConfigAndSiblingOriginSession(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store, fakeProvider{identity: ProviderIdentity{Provider: "github", ProviderID: "123"}})
	configRequest := httptest.NewRequest(http.MethodGet, "https://relay.example/api/auth/config", nil)
	configRequest.Header.Set("Origin", "https://community.example")
	configResponse := httptest.NewRecorder()
	service.ServeHTTP(configResponse, configRequest)
	require.Equal(t, http.StatusOK, configResponse.Code)
	require.JSONEq(t, `{"providers":["github"],"capabilities":{"github_linking":true,"managed_signing":true,"nip44":true,"nip46":true,"nostr_login":true,"passkey_registration":false,"github_disconnect":true,"nip46_management":true,"custody_transfer":false}}`, configResponse.Body.String())

	secret := nostr.Generate()
	account, err := store.CreateExternalAccount(context.Background(), secret.Public())
	require.NoError(t, err)
	cookie := createTestSession(t, service, store, account)
	sessionRequest := httptest.NewRequest(http.MethodGet, "https://relay.example/api/auth/session", nil)
	sessionRequest.Header.Set("Origin", "https://community.example")
	sessionRequest.AddCookie(cookie)
	sessionResponse := httptest.NewRecorder()
	service.ServeHTTP(sessionResponse, sessionRequest)
	require.Equal(t, http.StatusOK, sessionResponse.Code, sessionResponse.Body.String())
	require.Equal(t, "https://community.example", sessionResponse.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", sessionResponse.Header().Get("Access-Control-Allow-Credentials"))
}

func TestWebAuthnCapabilitiesRequireConfiguredBrowserOrigin(t *testing.T) {
	store := newMemoryStore()
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	wrapper, err := NewAESKeyWrapper("v1", master)
	require.NoError(t, err)
	service, err := NewService(ServiceConfig{
		PublicURL:             "https://registry.example.com",
		AllowedBrowserOrigins: []string{"https://community.example.com"},
		WebAuthnRPID:          "example.com",
		WebAuthnOrigins:       []string{"https://community.example.com"},
	}, store, wrapper)
	require.NoError(t, err)

	allowed := httptest.NewRequest(http.MethodGet, "https://registry.example.com/api/auth/config", nil)
	allowed.Header.Set("Origin", "https://community.example.com")
	allowedResponse := httptest.NewRecorder()
	service.ServeHTTP(allowedResponse, allowed)
	require.Equal(t, http.StatusOK, allowedResponse.Code)
	require.Contains(t, allowedResponse.Body.String(), `"passkey_registration":true`)
	require.Contains(t, allowedResponse.Body.String(), `"custody_transfer":true`)

	public := httptest.NewRequest(http.MethodGet, "https://registry.example.com/api/auth/config", nil)
	publicResponse := httptest.NewRecorder()
	service.ServeHTTP(publicResponse, public)
	require.Equal(t, http.StatusOK, publicResponse.Code)
	require.Contains(t, publicResponse.Body.String(), `"passkey_registration":false`)
}

func TestOAuthReturnURLAllowsConfiguredOriginAndSanitizesProviderFailure(t *testing.T) {
	store := newMemoryStore()
	provider := fakeProvider{identity: ProviderIdentity{Provider: "github", ProviderID: "123", Token: &oauth2.Token{AccessToken: "token"}}}
	service := newTestService(t, store, provider)
	returnTo := "https://community.example/settings/account?tab=identity#linked"
	begin := httptest.NewRecorder()
	service.ServeHTTP(begin, httptest.NewRequest(http.MethodGet, "https://relay.example/api/auth/login/github?return_to="+url.QueryEscape(returnTo), nil))
	require.Equal(t, http.StatusFound, begin.Code)
	authorize, err := url.Parse(begin.Header().Get("Location"))
	require.NoError(t, err)

	callback := httptest.NewRecorder()
	callbackURL := "https://relay.example/api/auth/callback/github?error=access_denied&error_description=secret+provider+detail&state=" + url.QueryEscape(authorize.Query().Get("state"))
	service.ServeHTTP(callback, httptest.NewRequest(http.MethodGet, callbackURL, nil))
	require.Equal(t, http.StatusFound, callback.Code)
	redirect, err := url.Parse(callback.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "https://community.example/settings/account", redirect.Scheme+"://"+redirect.Host+redirect.Path)
	require.Equal(t, "identity", redirect.Query().Get("tab"))
	require.Equal(t, "provider_authentication_failed", redirect.Query().Get("auth_error"))
	require.Equal(t, "linked", redirect.Fragment)
	require.NotContains(t, callback.Header().Get("Location"), "secret")
	require.NotContains(t, callback.Header().Get("Location"), "state=")

	openRedirect := httptest.NewRecorder()
	service.ServeHTTP(openRedirect, httptest.NewRequest(http.MethodGet, "https://relay.example/api/auth/login/github?return_to="+url.QueryEscape("https://attacker.example/steal"), nil))
	require.Equal(t, http.StatusBadRequest, openRedirect.Code)
}

func TestManagedSessionSignsCommunityEventsAndRejectsMutationFields(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store)
	account, cookie := createManagedTestAccount(t, service, store)
	now := service.Now().Unix()
	body := []byte(`{"event":{"kind":9,"created_at":` + jsonNumber(now) + `,"content":"hello","tags":[["h","group-a"]]}}`)
	request := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/events/sign", bytes.NewReader(body))
	request.Header.Set("Origin", "https://community.example")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var payload struct {
		Event nostr.Event `json:"event"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, account.Pubkey, payload.Event.PubKey)
	require.Equal(t, nostr.Kind(9), payload.Event.Kind)
	require.True(t, payload.Event.CheckID())
	require.True(t, payload.Event.VerifySignature())

	for _, invalid := range []string{
		`{"event":{"kind":9,"created_at":` + jsonNumber(now) + `,"content":"hello","tags":[["h","group-a"]],"pubkey":"` + account.Pubkey.Hex() + `"}}`,
		`{"event":{"kind":9,"created_at":` + jsonNumber(now) + `,"content":"hello","tags":[]}}`,
		`{"event":{"kind":6,"created_at":` + jsonNumber(now) + `,"content":"hello","tags":[]}}`,
		`{"event":{"kind":9,"created_at":` + jsonNumber(now-601) + `,"content":"hello","tags":[["h","group-a"]]}}`,
		`{"event":{"kind":27235,"created_at":` + jsonNumber(now) + `,"content":"","tags":[["u","https://attacker.example/api"],["method","GET"]]}}`,
	} {
		invalidRequest := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/events/sign", strings.NewReader(invalid))
		invalidRequest.Header.Set("Origin", "https://community.example")
		invalidRequest.AddCookie(cookie)
		invalidResponse := httptest.NewRecorder()
		service.ServeHTTP(invalidResponse, invalidRequest)
		require.GreaterOrEqual(t, invalidResponse.Code, http.StatusBadRequest)
		require.Less(t, invalidResponse.Code, http.StatusInternalServerError, invalidResponse.Body.String())
	}
}

func TestManagedSessionNIP44RoundTripAndSanitizedAudit(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store)
	account, cookie := createManagedTestAccount(t, service, store)
	peerSecret := nostr.Generate()
	encryptBody := `{"pubkey":"` + peerSecret.Public().Hex() + `","plaintext":"private message"}`
	encryptRequest := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/nip44/encrypt", strings.NewReader(encryptBody))
	encryptRequest.Header.Set("Origin", "https://community.example")
	encryptRequest.AddCookie(cookie)
	encryptResponse := httptest.NewRecorder()
	service.ServeHTTP(encryptResponse, encryptRequest)
	require.Equal(t, http.StatusOK, encryptResponse.Code, encryptResponse.Body.String())
	var encrypted struct {
		Ciphertext string `json:"ciphertext"`
	}
	require.NoError(t, json.Unmarshal(encryptResponse.Body.Bytes(), &encrypted))
	conversationKey, err := nip44.GenerateConversationKey(account.Pubkey, peerSecret)
	require.NoError(t, err)
	plaintext, err := nip44.Decrypt(encrypted.Ciphertext, conversationKey)
	require.NoError(t, err)
	require.Equal(t, "private message", plaintext)

	decryptBody := `{"pubkey":"` + peerSecret.Public().Hex() + `","ciphertext":"` + encrypted.Ciphertext + `"}`
	decryptRequest := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/nip44/decrypt", strings.NewReader(decryptBody))
	decryptRequest.Header.Set("Origin", "https://community.example")
	decryptRequest.AddCookie(cookie)
	decryptResponse := httptest.NewRecorder()
	service.ServeHTTP(decryptResponse, decryptRequest)
	require.Equal(t, http.StatusOK, decryptResponse.Code, decryptResponse.Body.String())
	require.JSONEq(t, `{"plaintext":"private message"}`, decryptResponse.Body.String())
	require.Len(t, store.audit, 2)
	for _, audit := range store.audit {
		require.NotContains(t, audit.Operation, "private message")
		require.Empty(t, audit.SourceEventID)
	}
}

func TestNIP46SessionDelegatesNIP44AndExternalSignerIsUnavailable(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store)
	accountSecret := nostr.Generate()
	account, err := store.CreateExternalAccount(context.Background(), accountSecret.Public())
	require.NoError(t, err)
	cookie := createTestSession(t, service, store, account)
	peerSecret := nostr.Generate()

	externalRequest := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/nip44/encrypt", strings.NewReader(`{"pubkey":"`+peerSecret.Public().Hex()+`","plaintext":"hello"}`))
	externalRequest.Header.Set("Origin", "https://community.example")
	externalRequest.AddCookie(cookie)
	externalResponse := httptest.NewRecorder()
	service.ServeHTTP(externalResponse, externalRequest)
	require.Equal(t, http.StatusConflict, externalResponse.Code)

	clientSecret := nostr.Generate()
	metadata, err := encodeNIP46Binding(account.ID, "bunker://"+account.Pubkey.Hex()+"?relay=wss%3A%2F%2Frelay.example", clientSecret, CredentialCodec{Wrapper: service.Wrapper})
	require.NoError(t, err)
	require.NoError(t, store.SetNIP46Binding(context.Background(), account.ID, account.Pubkey, metadata, service.Now()))
	service.NIP46 = &fakeNIP46Connector{remote: &fakeNIP46Remote{secret: accountSecret}}

	nip46Request := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/nip44/encrypt", strings.NewReader(`{"pubkey":"`+peerSecret.Public().Hex()+`","plaintext":"delegated"}`))
	nip46Request.Header.Set("Origin", "https://community.example")
	nip46Request.AddCookie(cookie)
	nip46Response := httptest.NewRecorder()
	service.ServeHTTP(nip46Response, nip46Request)
	require.Equal(t, http.StatusOK, nip46Response.Code, nip46Response.Body.String())
	var payload struct {
		Ciphertext string `json:"ciphertext"`
	}
	require.NoError(t, json.Unmarshal(nip46Response.Body.Bytes(), &payload))
	conversationKey, err := nip44.GenerateConversationKey(account.Pubkey, peerSecret)
	require.NoError(t, err)
	plaintext, err := nip44.Decrypt(payload.Ciphertext, conversationKey)
	require.NoError(t, err)
	require.Equal(t, "delegated", plaintext)
}

func createManagedTestAccount(t *testing.T, service *Service, store *memoryStore) (Account, *http.Cookie) {
	t.Helper()
	secret := nostr.Generate()
	accountID := "managed-browser-account"
	key, err := EncryptManagedKey(accountID, secret.Public().Hex(), secret[:], service.Wrapper)
	require.NoError(t, err)
	account, err := store.CreateManagedAccount(context.Background(), ExternalIdentity{AccountID: accountID, Provider: "google", ProviderID: "subject"}, key)
	require.NoError(t, err)
	return account, createTestSession(t, service, store, account)
}

func jsonNumber(value int64) string {
	return strconv.FormatInt(value, 10)
}
