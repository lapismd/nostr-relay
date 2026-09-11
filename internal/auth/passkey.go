package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	webAuthnFlowRegister    = "register"
	webAuthnFlowTransfer    = "custody_transfer"
	passkeyEnrollmentWindow = 10 * time.Minute
)

type webAuthnUser struct {
	account     Account
	credentials []webauthn.Credential
}

func (user webAuthnUser) WebAuthnID() []byte   { return []byte(user.account.ID) }
func (user webAuthnUser) WebAuthnName() string { return user.account.Pubkey.Hex() }
func (user webAuthnUser) WebAuthnDisplayName() string {
	return "Lapis " + user.account.Pubkey.Hex()[:12]
}
func (user webAuthnUser) WebAuthnCredentials() []webauthn.Credential { return user.credentials }

func (service *Service) passkeyUser(ctx context.Context, account Account) (webAuthnUser, []PasskeyRecord, error) {
	records, err := service.Store.PasskeysByAccount(ctx, account.ID)
	if err != nil {
		return webAuthnUser{}, nil, err
	}
	credentials := make([]webauthn.Credential, 0, len(records))
	codec := CredentialCodec{Wrapper: service.Wrapper}
	for _, record := range records {
		plain, err := codec.Decrypt(account.ID, passkeyCredentialPurpose(record.CredentialIDHash), record.EncryptedCredential)
		if err != nil {
			return webAuthnUser{}, nil, ErrSignerUnavailable
		}
		var credential webauthn.Credential
		if err := json.Unmarshal([]byte(plain), &credential); err != nil {
			return webAuthnUser{}, nil, ErrSignerUnavailable
		}
		credentials = append(credentials, credential)
	}
	return webAuthnUser{account: account, credentials: credentials}, records, nil
}

func (service *Service) beginPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	authSession, account, err := service.Authenticate(r.Context(), r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	user, _, err := service.passkeyUser(r.Context(), account)
	if err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "passkey_store_unavailable")
		return
	}
	if service.Now().Sub(authSession.CreatedAt) > passkeyEnrollmentWindow {
		writeAuthError(w, http.StatusUnauthorized, "recent_authentication_required")
		return
	}
	if len(user.credentials) != 0 {
		writeAuthError(w, http.StatusConflict, "passkey_already_registered")
		return
	}
	options, session, err := service.WebAuthn.BeginRegistration(user,
		webauthn.WithExclusions(webauthn.Credentials(user.credentials).CredentialDescriptors()),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		}),
	)
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "passkey_registration_failed")
		return
	}
	flowID, err := service.saveWebAuthnFlow(r.Context(), account.ID, webAuthnFlowRegister, session, nil)
	if err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "passkey_store_unavailable")
		return
	}
	writeAuthJSON(w, http.StatusOK, map[string]any{"flow_id": flowID, "options": options})
}

func (service *Service) finishPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	_, account, err := service.Authenticate(r.Context(), r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	flow, session, err := service.consumeWebAuthnFlow(r.Context(), r.URL.Query().Get("flow_id"), webAuthnFlowRegister, account.ID)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid_passkey_flow")
		return
	}
	_ = flow
	user, _, err := service.passkeyUser(r.Context(), account)
	if err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "passkey_store_unavailable")
		return
	}
	credential, err := service.WebAuthn.FinishRegistration(user, session, r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "invalid_passkey_attestation")
		return
	}
	if err := service.savePasskey(r.Context(), account.ID, *credential, ""); err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "passkey_store_unavailable")
		return
	}
	writeAuthJSON(w, http.StatusCreated, map[string]any{"credential_id": encodeRawURL(credential.ID)})
}

func (service *Service) saveWebAuthnFlow(ctx context.Context, accountID, kind string, session *webauthn.SessionData, devicePublicKey []byte) (string, error) {
	if session == nil {
		return "", errors.New("WebAuthn session is missing")
	}
	flowID, err := randomToken(32)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(flowID))
	encoded, err := json.Marshal(session)
	if err != nil {
		return "", err
	}
	encrypted, err := (CredentialCodec{Wrapper: service.Wrapper}).Encrypt(accountID, webAuthnFlowPurpose(kind, hash), string(encoded))
	if err != nil {
		return "", err
	}
	expires := session.Expires
	maximum := service.Now().Add(service.Config.ChallengeTTL)
	if expires.IsZero() || expires.After(maximum) {
		expires = maximum
	}
	return flowID, service.Store.BeginWebAuthnFlow(ctx, WebAuthnFlow{
		FlowHash: hash, AccountID: accountID, Kind: kind, EncryptedSession: encrypted,
		DevicePublicKey: append([]byte(nil), devicePublicKey...), ExpiresAt: expires,
	})
}

func (service *Service) consumeWebAuthnFlow(ctx context.Context, flowID, kind, accountID string) (WebAuthnFlow, webauthn.SessionData, error) {
	if flowID == "" {
		return WebAuthnFlow{}, webauthn.SessionData{}, ErrNotFound
	}
	hash := sha256.Sum256([]byte(flowID))
	flow, err := service.Store.ConsumeWebAuthnFlow(ctx, hash, kind, accountID, service.Now())
	if err != nil {
		return flow, webauthn.SessionData{}, err
	}
	plain, err := (CredentialCodec{Wrapper: service.Wrapper}).Decrypt(accountID, webAuthnFlowPurpose(kind, hash), flow.EncryptedSession)
	if err != nil {
		return flow, webauthn.SessionData{}, err
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(plain), &session); err != nil {
		return flow, session, err
	}
	return flow, session, nil
}

func (service *Service) savePasskey(ctx context.Context, accountID string, credential webauthn.Credential, label string) error {
	encoded, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(credential.ID)
	encrypted, err := (CredentialCodec{Wrapper: service.Wrapper}).Encrypt(accountID, passkeyCredentialPurpose(hash), string(encoded))
	if err != nil {
		return err
	}
	return service.Store.SavePasskey(ctx, PasskeyRecord{
		AccountID: accountID, CredentialIDHash: hash, EncryptedCredential: encrypted,
		Label: label, CreatedAt: service.Now(), LastUsedAt: service.Now(),
	})
}

func passkeyCredentialPurpose(hash [32]byte) string {
	return "passkey-credential:" + hex.EncodeToString(hash[:])
}

func webAuthnFlowPurpose(kind string, hash [32]byte) string {
	return "webauthn-flow:" + kind + ":" + hex.EncodeToString(hash[:])
}

func credentialForID(records []PasskeyRecord, credential *webauthn.Credential) (PasskeyRecord, error) {
	if credential == nil {
		return PasskeyRecord{}, ErrNotFound
	}
	hash := sha256.Sum256(credential.ID)
	for _, record := range records {
		if record.CredentialIDHash == hash {
			return record, nil
		}
	}
	return PasskeyRecord{}, ErrNotFound
}

var _ webauthn.User = webAuthnUser{}
