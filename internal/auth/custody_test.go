package auth

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/hkdf"
)

func TestCustodyTransferPreservesPubkeyAndDeletesManagedKeyAfterProof(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store)
	managedSecret := nostr.Generate()
	accountID := "managed-transfer-account"
	key, err := EncryptManagedKey(accountID, managedSecret.Public().Hex(), managedSecret[:], service.Wrapper)
	require.NoError(t, err)
	account, err := store.CreateManagedAccount(context.Background(), ExternalIdentity{
		AccountID: accountID, Provider: "google", ProviderID: "transfer-subject",
	}, key)
	require.NoError(t, err)
	cookie := createTestSession(t, service, store, account)

	devicePrivateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	transferID := "transfer-id"
	challenge := "possession-challenge-0123456789"
	expires := time.Now().Add(custodyTransferTTL)
	envelope, err := service.encryptManagedKeyForDevice(context.Background(), account, transferID, challenge, expires, devicePrivateKey.PublicKey().Bytes())
	require.NoError(t, err)
	transferredSecret := decryptTransferEnvelope(t, account, envelope, devicePrivateKey)
	defer clear(transferredSecret[:])
	require.Equal(t, account.Pubkey, transferredSecret.Public())

	challengeHash := sha256.Sum256([]byte(challenge))
	require.NoError(t, store.CreateCustodyTransfer(context.Background(), CustodyTransfer{
		ID: transferID, AccountID: account.ID, ChallengeHash: challengeHash,
		State: "key_received_by_device", ExpiresAt: expires, CreatedAt: time.Now(),
	}))
	require.Equal(t, SignerMigrationPending, store.bindings[account.ID].Status)
	service.NIP46 = &fakeNIP46Connector{remote: &fakeNIP46Remote{secret: transferredSecret}}
	proof := nostr.Event{
		Kind: nostr.KindClientAuthentication, CreatedAt: nostr.Now(), Content: "",
		Tags: nostr.Tags{{"relay", "wss://relay.example"}, {"challenge", challenge}, {"purpose", "custody-transfer"}},
	}
	require.NoError(t, proof.Sign(transferredSecret))
	bunkerURL := "bunker://" + nostr.Generate().Public().Hex() + "?relay=" + url.QueryEscape("wss://relay.example") + "&secret=one-time-secret"
	body, err := json.Marshal(map[string]any{"transfer_id": transferID, "proof_event": proof, "bunker_url": bunkerURL})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/custody/transfer/complete", bytes.NewReader(body))
	request.Header.Set("Origin", "https://relay.example")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, SignerNIP46, store.bindings[account.ID].Type)
	require.Equal(t, SignerActive, store.bindings[account.ID].Status)
	_, found := store.keys[account.ID]
	require.False(t, found)
	require.Equal(t, "complete", store.transfers[transferID].State)
	require.NotContains(t, string(store.bindings[account.ID].Metadata), "one-time-secret")
	require.NotEmpty(t, store.audit)
	require.Equal(t, "custody_transfer_complete", store.audit[len(store.audit)-1].Operation)
}

func TestCustodyTransferRequiresEnrolledPasskey(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store)
	secret := nostr.Generate()
	accountID := "managed-no-passkey"
	key, err := EncryptManagedKey(accountID, secret.Public().Hex(), secret[:], service.Wrapper)
	require.NoError(t, err)
	account, err := store.CreateManagedAccount(context.Background(), ExternalIdentity{
		AccountID: accountID, Provider: "google", ProviderID: "no-passkey-subject",
	}, key)
	require.NoError(t, err)
	cookie := createTestSession(t, service, store, account)
	devicePrivateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	body := []byte(`{"device_public_key":"` + base64.RawURLEncoding.EncodeToString(devicePrivateKey.PublicKey().Bytes()) + `"}`)
	request := httptest.NewRequest(http.MethodPost, "https://relay.example/api/auth/custody/transfer/begin", bytes.NewReader(body))
	request.Header.Set("Origin", "https://relay.example")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	require.Equal(t, http.StatusPreconditionRequired, response.Code, response.Body.String())
}

func TestWebAuthnFlowIsEncryptedAndOneTime(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store)
	session := &webauthn.SessionData{
		Challenge: "not-stored-in-plaintext", RelyingPartyID: "relay.example",
		UserID: []byte("account-a"), UserVerification: protocol.VerificationRequired,
		Expires: time.Now().Add(time.Minute),
	}
	flowID, err := service.saveWebAuthnFlow(context.Background(), "account-a", webAuthnFlowRegister, session, nil)
	require.NoError(t, err)
	require.Len(t, store.webAuthnFlows, 1)
	for _, flow := range store.webAuthnFlows {
		require.NotContains(t, flow.EncryptedSession, session.Challenge)
	}
	consumed, restored, err := service.consumeWebAuthnFlow(context.Background(), flowID, webAuthnFlowRegister, "account-a")
	require.NoError(t, err)
	require.Equal(t, "account-a", consumed.AccountID)
	require.Equal(t, session.Challenge, restored.Challenge)
	_, _, err = service.consumeWebAuthnFlow(context.Background(), flowID, webAuthnFlowRegister, "account-a")
	require.ErrorIs(t, err, ErrNotFound)
}

func decryptTransferEnvelope(t *testing.T, account Account, envelope custodyTransferEnvelope, devicePrivateKey *ecdh.PrivateKey) nostr.SecretKey {
	t.Helper()
	serverPublicBytes, err := base64.RawURLEncoding.DecodeString(envelope.ServerPublicKey)
	require.NoError(t, err)
	serverPublicKey, err := ecdh.X25519().NewPublicKey(serverPublicBytes)
	require.NoError(t, err)
	shared, err := devicePrivateKey.ECDH(serverPublicKey)
	require.NoError(t, err)
	defer clear(shared)
	derived := make([]byte, 32)
	info := []byte("lapis-custody-transfer-v1\x00" + account.ID + "\x00" + envelope.TransferID + "\x00" + account.Pubkey.Hex())
	_, err = io.ReadFull(hkdf.New(sha256.New, shared, nil, info), derived)
	require.NoError(t, err)
	defer clear(derived)
	block, err := aes.NewCipher(derived)
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	sealed, err := base64.RawURLEncoding.DecodeString(envelope.EncryptedPrivateKey)
	require.NoError(t, err)
	plain, err := open(aead, sealed, info)
	require.NoError(t, err)
	defer clear(plain)
	var secret nostr.SecretKey
	copy(secret[:], plain)
	return secret
}
