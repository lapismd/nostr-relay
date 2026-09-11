package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip42"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"golang.org/x/crypto/hkdf"
)

const custodyTransferTTL = 15 * time.Minute

type custodyTransferEnvelope struct {
	TransferID          string `json:"transfer_id"`
	ServerPublicKey     string `json:"server_public_key"`
	EncryptedPrivateKey string `json:"encrypted_private_key"`
	PossessionChallenge string `json:"possession_challenge"`
	ExpiresAt           string `json:"expires_at"`
}

func (service *Service) beginCustodyTransfer(w http.ResponseWriter, r *http.Request) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	var request struct {
		DevicePublicKey string `json:"device_public_key"`
	}
	if !decodeAuthJSON(w, r, &request) {
		return
	}
	devicePublicKey, err := base64.RawURLEncoding.DecodeString(request.DevicePublicKey)
	if err != nil || len(devicePublicKey) != 32 {
		writeAuthError(w, http.StatusBadRequest, "invalid_device_public_key")
		return
	}
	if _, err := ecdh.X25519().NewPublicKey(devicePublicKey); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid_device_public_key")
		return
	}
	_, account, err := service.Authenticate(r.Context(), r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	binding, err := service.Store.SignerBindingByAccount(r.Context(), account.ID)
	if err != nil || binding.Type != SignerManaged || binding.Pubkey != account.Pubkey || binding.Status == SignerDisabled {
		writeAuthError(w, http.StatusConflict, "managed_custody_required")
		return
	}
	user, records, err := service.passkeyUser(r.Context(), account)
	if err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "passkey_store_unavailable")
		return
	}
	if len(records) == 0 {
		writeAuthError(w, http.StatusPreconditionRequired, "passkey_required")
		return
	}
	options, session, err := service.WebAuthn.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "passkey_assertion_failed")
		return
	}
	flowID, err := service.saveWebAuthnFlow(r.Context(), account.ID, webAuthnFlowTransfer, session, devicePublicKey)
	if err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "passkey_store_unavailable")
		return
	}
	writeAuthJSON(w, http.StatusOK, map[string]any{"flow_id": flowID, "options": options})
}

func (service *Service) authorizeCustodyTransfer(w http.ResponseWriter, r *http.Request) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	_, account, err := service.Authenticate(r.Context(), r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	flow, session, err := service.consumeWebAuthnFlow(r.Context(), r.URL.Query().Get("flow_id"), webAuthnFlowTransfer, account.ID)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid_passkey_flow")
		return
	}
	user, records, err := service.passkeyUser(r.Context(), account)
	if err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "passkey_store_unavailable")
		return
	}
	credential, err := service.WebAuthn.FinishLogin(user, session, r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "invalid_passkey_assertion")
		return
	}
	record, err := credentialForID(records, credential)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "invalid_passkey_assertion")
		return
	}
	if err := service.savePasskey(r.Context(), account.ID, *credential, record.Label); err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "passkey_store_unavailable")
		return
	}
	transferID := uuid.NewString()
	challenge, err := randomToken(32)
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "custody_transfer_failed")
		return
	}
	expires := service.Now().Add(custodyTransferTTL)
	envelope, err := service.encryptManagedKeyForDevice(r.Context(), account, transferID, challenge, expires, flow.DevicePublicKey)
	if err != nil {
		writeAuthError(w, http.StatusConflict, "managed_custody_required")
		return
	}
	challengeHash := sha256.Sum256([]byte(challenge))
	if err := service.Store.CreateCustodyTransfer(r.Context(), CustodyTransfer{
		ID: transferID, AccountID: account.ID, ChallengeHash: challengeHash,
		State: "key_received_by_device", ExpiresAt: expires, CreatedAt: service.Now(),
	}); err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "custody_transfer_failed")
		return
	}
	service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, SignerType: SignerManaged, Operation: "custody_transfer_authorize", Result: "success"})
	writeAuthJSON(w, http.StatusOK, envelope)
}

func (service *Service) encryptManagedKeyForDevice(ctx context.Context, account Account, transferID, challenge string, expires time.Time, devicePublicBytes []byte) (custodyTransferEnvelope, error) {
	key, err := service.Store.LoadManagedKey(ctx, account.ID)
	if err != nil || key.Pubkey != account.Pubkey.Hex() {
		return custodyTransferEnvelope{}, ErrSignerUnavailable
	}
	secret, err := key.Decrypt(service.Wrapper)
	if err != nil {
		return custodyTransferEnvelope{}, ErrSignerUnavailable
	}
	defer clear(secret)
	devicePublicKey, err := ecdh.X25519().NewPublicKey(devicePublicBytes)
	if err != nil {
		return custodyTransferEnvelope{}, err
	}
	serverPrivateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return custodyTransferEnvelope{}, err
	}
	shared, err := serverPrivateKey.ECDH(devicePublicKey)
	if err != nil {
		return custodyTransferEnvelope{}, err
	}
	defer clear(shared)
	derived := make([]byte, 32)
	info := []byte("lapis-custody-transfer-v1\x00" + account.ID + "\x00" + transferID + "\x00" + account.Pubkey.Hex())
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, nil, info), derived); err != nil {
		return custodyTransferEnvelope{}, err
	}
	defer clear(derived)
	block, err := aes.NewCipher(derived)
	if err != nil {
		return custodyTransferEnvelope{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return custodyTransferEnvelope{}, err
	}
	sealed, err := seal(aead, rand.Reader, secret, info)
	if err != nil {
		return custodyTransferEnvelope{}, err
	}
	return custodyTransferEnvelope{
		TransferID: transferID, ServerPublicKey: base64.RawURLEncoding.EncodeToString(serverPrivateKey.PublicKey().Bytes()),
		EncryptedPrivateKey: base64.RawURLEncoding.EncodeToString(sealed), PossessionChallenge: challenge,
		ExpiresAt: expires.UTC().Format(time.RFC3339),
	}, nil
}

func (service *Service) completeCustodyTransfer(w http.ResponseWriter, r *http.Request) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	var request struct {
		TransferID string      `json:"transfer_id"`
		ProofEvent nostr.Event `json:"proof_event"`
		BunkerURL  string      `json:"bunker_url"`
	}
	if !decodeAuthJSON(w, r, &request) {
		return
	}
	_, account, err := service.Authenticate(r.Context(), r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	transfer, err := service.Store.CustodyTransferByID(r.Context(), request.TransferID, account.ID, service.Now())
	if err != nil || transfer.State != "key_received_by_device" {
		writeAuthError(w, http.StatusBadRequest, "invalid_custody_transfer")
		return
	}
	challengeTag := request.ProofEvent.Tags.Find("challenge")
	purposeTag := request.ProofEvent.Tags.FindWithValue("purpose", "custody-transfer")
	if len(challengeTag) < 2 || purposeTag == nil || sha256.Sum256([]byte(challengeTag[1])) != transfer.ChallengeHash {
		writeAuthError(w, http.StatusUnauthorized, "invalid_possession_proof")
		return
	}
	pubkey, err := nip42.ValidateAuthEvent(request.ProofEvent, challengeTag[1], service.relayURL())
	if err != nil || pubkey != account.Pubkey {
		writeAuthError(w, http.StatusUnauthorized, "invalid_possession_proof")
		return
	}
	cleanURL, err := validateAndSanitizeBunkerURL(request.BunkerURL)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid_bunker_url")
		return
	}
	clientSecret := nostr.Generate()
	defer clear(clientSecret[:])
	connectCtx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	remote, err := service.NIP46.Connect(connectCtx, clientSecret, request.BunkerURL)
	if err != nil {
		writeAuthError(w, http.StatusBadGateway, "nip46_connection_failed")
		return
	}
	remotePubkey, err := remote.GetPublicKey(connectCtx)
	if err != nil || remotePubkey != account.Pubkey {
		writeAuthError(w, http.StatusConflict, "nip46_pubkey_mismatch")
		return
	}
	metadata, err := encodeNIP46Binding(account.ID, cleanURL, clientSecret, CredentialCodec{Wrapper: service.Wrapper})
	if err != nil || service.Store.CompleteCustodyTransfer(r.Context(), transfer.ID, account.ID, account.Pubkey, metadata, service.Now()) != nil {
		writeAuthError(w, http.StatusConflict, "custody_transfer_failed")
		return
	}
	service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, SignerType: SignerNIP46, Operation: "custody_transfer_complete", Result: "success"})
	writeAuthJSON(w, http.StatusOK, map[string]any{
		"nostr_pubkey": account.Pubkey.Hex(), "signer_type": SignerNIP46, "managed_key_deleted": true,
	})
}
