package auth

import (
	"context"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip42"
	"net/http"
	"time"
)

func (service *Service) signerForAccount(ctx context.Context, account Account) (Signer, error) {
	binding, err := service.Store.SignerBindingByAccount(ctx, account.ID)
	if err != nil || binding.Status != SignerActive || binding.Pubkey != account.Pubkey {
		return nil, ErrSignerUnavailable
	}
	policy := ApplicationSigningPolicy{PublicURL: service.Config.PublicURL, RelayURL: service.relayURL()}
	switch binding.Type {
	case SignerManaged:
		return &ManagedSigner{AccountID: account.ID, Pubkey: account.Pubkey, Keys: service.Store, Wrapper: service.Wrapper, Policy: policy}, nil
	case SignerNIP46:
		bunkerURL, clientSecret, err := decodeNIP46Binding(account.ID, binding.Metadata, CredentialCodec{Wrapper: service.Wrapper})
		if err != nil || service.NIP46 == nil {
			return nil, ErrSignerUnavailable
		}
		defer clear(clientSecret[:])
		remote, err := service.NIP46.Connect(ctx, clientSecret, bunkerURL)
		if err != nil {
			return nil, ErrSignerUnavailable
		}
		return &NIP46Signer{AccountID: account.ID, Pubkey: account.Pubkey, Remote: remote, Policy: policy}, nil
	default:
		return nil, ErrSignerUnavailable
	}
}

func (service *Service) signRelayAuth(w http.ResponseWriter, r *http.Request) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	var request struct {
		Challenge string `json:"challenge"`
	}
	if !decodeAuthJSON(w, r, &request) {
		return
	}
	if len(request.Challenge) < 16 || len(request.Challenge) > 512 {
		writeAuthError(w, http.StatusBadRequest, "invalid_challenge")
		return
	}
	session, account, err := service.Authenticate(r.Context(), r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	signer, err := service.signerForAccount(r.Context(), account)
	if err != nil {
		writeAuthError(w, http.StatusConflict, "signer_unavailable")
		return
	}
	event := nip42.CreateUnsignedAuthEvent(request.Challenge, account.Pubkey, service.relayURL())
	event.CreatedAt = nostr.Timestamp(service.Now().Unix())
	signed, err := signer.SignEvent(r.Context(), event, SignContext{
		AccountID: account.ID, Purpose: SignPurposeRelayAuth, RequestedKind: event.Kind,
		Source: request.Challenge, CorrelationID: r.Header.Get("X-Correlation-ID"),
	})
	if err != nil {
		service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: session.AuthProvider, Operation: "sign_relay_auth", CorrelationID: r.Header.Get("X-Correlation-ID"), Result: "denied"})
		writeAuthError(w, http.StatusForbidden, "signing_denied")
		return
	}
	binding, _ := service.Store.SignerBindingByAccount(r.Context(), account.ID)
	service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: session.AuthProvider, SignerType: binding.Type, Operation: "sign_relay_auth", CorrelationID: r.Header.Get("X-Correlation-ID"), Result: "success"})
	writeAuthJSON(w, http.StatusOK, map[string]any{"event": signed})
}

func (service *Service) signApplicationEvent(w http.ResponseWriter, r *http.Request) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	var request struct {
		Event struct {
			Kind      nostr.Kind      `json:"kind"`
			CreatedAt nostr.Timestamp `json:"created_at"`
			Content   string          `json:"content"`
			Tags      nostr.Tags      `json:"tags"`
		} `json:"event"`
	}
	if !decodeAuthJSON(w, r, &request) {
		return
	}
	event := nostr.Event{Kind: request.Event.Kind, CreatedAt: request.Event.CreatedAt, Content: request.Event.Content, Tags: request.Event.Tags}
	session, account, err := service.Authenticate(r.Context(), r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	purpose, allowed := CommunitySignPurpose(event.Kind)
	if !allowed {
		service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: session.AuthProvider, Operation: "sign_event", CorrelationID: r.Header.Get("X-Correlation-ID"), Result: "denied"})
		writeAuthError(w, http.StatusForbidden, "event_kind_not_allowed")
		return
	}
	now := service.Now()
	createdAt := time.Unix(int64(event.CreatedAt), 0)
	if createdAt.Before(now.Add(-5*time.Minute)) || createdAt.After(now.Add(5*time.Minute)) {
		service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: session.AuthProvider, Operation: "sign_event", CorrelationID: r.Header.Get("X-Correlation-ID"), Result: "denied"})
		writeAuthError(w, http.StatusBadRequest, "event_timestamp_out_of_range")
		return
	}
	signer, err := service.signerForAccount(r.Context(), account)
	if err != nil {
		service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: session.AuthProvider, Operation: "sign_event", CorrelationID: r.Header.Get("X-Correlation-ID"), Result: "denied"})
		writeAuthError(w, http.StatusConflict, "signer_unavailable")
		return
	}
	source := ""
	if purpose == SignPurposeRelayAuth {
		if tag := event.Tags.Find("challenge"); len(tag) >= 2 {
			source = tag[1]
		}
	} else if purpose == SignPurposeHTTPAuth {
		if tag := event.Tags.Find("u"); len(tag) >= 2 {
			source = tag[1]
		}
	}
	signed, err := signer.SignEvent(r.Context(), event, SignContext{
		AccountID: account.ID, Purpose: purpose, RequestedKind: event.Kind,
		Source: source, CorrelationID: r.Header.Get("X-Correlation-ID"),
	})
	if err != nil {
		service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: session.AuthProvider, Operation: "sign_event", CorrelationID: r.Header.Get("X-Correlation-ID"), Result: "denied"})
		writeAuthError(w, http.StatusForbidden, "signing_denied")
		return
	}
	binding, _ := service.Store.SignerBindingByAccount(r.Context(), account.ID)
	service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: session.AuthProvider, SignerType: binding.Type, Operation: "sign_event", SourceEventID: signed.ID.Hex(), CorrelationID: r.Header.Get("X-Correlation-ID"), Result: "success"})
	writeAuthJSON(w, http.StatusOK, map[string]any{"event": signed})
}
