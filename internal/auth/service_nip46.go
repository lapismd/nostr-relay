package auth

import (
	"context"
	"fiatjaf.com/nostr"
	"net/http"
	"time"
)

func (service *Service) nip44Encrypt(w http.ResponseWriter, r *http.Request) {
	service.nip44Operation(w, r, true)
}

func (service *Service) nip44Decrypt(w http.ResponseWriter, r *http.Request) {
	service.nip44Operation(w, r, false)
}

func (service *Service) nip44Operation(w http.ResponseWriter, r *http.Request, encrypt bool) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	var request struct {
		Pubkey     string `json:"pubkey"`
		Plaintext  string `json:"plaintext,omitempty"`
		Ciphertext string `json:"ciphertext,omitempty"`
	}
	if !decodeAuthJSON(w, r, &request) {
		return
	}
	session, account, authErr := service.Authenticate(r.Context(), r)
	if authErr != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	peer, err := nostr.PubKeyFromHex(request.Pubkey)
	value := request.Ciphertext
	operation := "nip44_decrypt"
	if encrypt {
		value = request.Plaintext
		operation = "nip44_encrypt"
	}
	if err != nil || len(value) > 96<<10 || encrypt && request.Ciphertext != "" || !encrypt && request.Plaintext != "" {
		service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: session.AuthProvider, Operation: operation, CorrelationID: r.Header.Get("X-Correlation-ID"), Result: "denied"})
		writeAuthError(w, http.StatusBadRequest, "invalid_nip44_request")
		return
	}
	signer, err := service.signerForAccount(r.Context(), account)
	if err != nil {
		service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: session.AuthProvider, Operation: operation, CorrelationID: r.Header.Get("X-Correlation-ID"), Result: "denied"})
		writeAuthError(w, http.StatusConflict, "signer_unavailable")
		return
	}
	if signerPubkey, keyErr := signer.PublicKey(r.Context()); keyErr != nil || signerPubkey != account.Pubkey {
		service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: session.AuthProvider, Operation: operation, CorrelationID: r.Header.Get("X-Correlation-ID"), Result: "denied"})
		writeAuthError(w, http.StatusConflict, "signer_unavailable")
		return
	}
	var result string
	if encrypt {
		result, err = signer.Encrypt(r.Context(), value, peer)
	} else {
		result, err = signer.Decrypt(r.Context(), value, peer)
	}
	if err != nil || len(result) > 128<<10 {
		service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: session.AuthProvider, Operation: operation, CorrelationID: r.Header.Get("X-Correlation-ID"), Result: "denied"})
		writeAuthError(w, http.StatusForbidden, "nip44_operation_denied")
		return
	}
	binding, _ := service.Store.SignerBindingByAccount(r.Context(), account.ID)
	service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: session.AuthProvider, SignerType: binding.Type, Operation: operation, CorrelationID: r.Header.Get("X-Correlation-ID"), Result: "success"})
	key := "plaintext"
	if encrypt {
		key = "ciphertext"
	}
	writeAuthJSON(w, http.StatusOK, map[string]string{key: result})
}

func (service *Service) connectNIP46(w http.ResponseWriter, r *http.Request) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	var request struct {
		BunkerURL string `json:"bunker_url"`
	}
	if !decodeAuthJSON(w, r, &request) {
		return
	}
	cleanURL, err := validateAndSanitizeBunkerURL(request.BunkerURL)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid_bunker_url")
		return
	}
	_, account, err := service.Authenticate(r.Context(), r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	binding, err := service.Store.SignerBindingByAccount(r.Context(), account.ID)
	if err != nil || binding.Type == SignerManaged || binding.Pubkey != account.Pubkey {
		writeAuthError(w, http.StatusConflict, "custody_transfer_required")
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
	pubkey, err := remote.GetPublicKey(connectCtx)
	if err != nil || pubkey != account.Pubkey {
		writeAuthError(w, http.StatusConflict, "nip46_pubkey_mismatch")
		return
	}
	metadata, err := encodeNIP46Binding(account.ID, cleanURL, clientSecret, CredentialCodec{Wrapper: service.Wrapper})
	if err != nil || service.Store.SetNIP46Binding(r.Context(), account.ID, account.Pubkey, metadata, service.Now()) != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "nip46_binding_failed")
		return
	}
	service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, SignerType: SignerNIP46, Operation: "nip46_connect", Result: "success"})
	writeAuthJSON(w, http.StatusOK, map[string]any{"nostr_pubkey": account.Pubkey.Hex(), "signer_type": SignerNIP46})
}

func (service *Service) switchNIP46Relays(w http.ResponseWriter, r *http.Request) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	_, account, err := service.Authenticate(r.Context(), r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	signer, err := service.signerForAccount(r.Context(), account)
	nip46Signer, ok := signer.(*NIP46Signer)
	if err != nil || !ok {
		writeAuthError(w, http.StatusConflict, "nip46_signer_unavailable")
		return
	}
	relays, err := nip46Signer.SwitchRelays(r.Context())
	if err != nil {
		writeAuthError(w, http.StatusBadGateway, "nip46_signer_unavailable")
		return
	}
	writeAuthJSON(w, http.StatusOK, map[string]any{"relays": relays})
}

func (service *Service) logoutNIP46(w http.ResponseWriter, r *http.Request) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	_, account, err := service.Authenticate(r.Context(), r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	if err := service.Store.SetExternalBinding(r.Context(), account.ID, account.Pubkey, service.Now()); err != nil {
		writeAuthError(w, http.StatusConflict, "nip46_signer_unavailable")
		return
	}
	service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, SignerType: SignerExternal, Operation: "nip46_logout", Result: "success"})
	w.WriteHeader(http.StatusNoContent)
}

func (service *Service) relayURL() string {
	relayURL, _ := relayURLForPublicURL(service.Config.PublicURL)
	return relayURL
}
