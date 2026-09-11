package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip42"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
)

const (
	SessionCookieName = "lapis_session"
	maxAuthBody       = 128 << 10
)

type ServiceConfig struct {
	PublicURL       string
	CookieDomain    string
	SessionLifetime time.Duration
	SessionIdle     time.Duration
	ChallengeTTL    time.Duration
	GitHubClientID  string
}

type Service struct {
	Config    ServiceConfig
	Store     Store
	Wrapper   KeyWrapper
	Providers map[string]OAuthProvider
	Now       func() time.Time
	refreshMu sync.Mutex
	NIP46     NIP46Connector
	WebAuthn  *webauthn.WebAuthn
}

func (service *Service) GitHubCredentialMode(ctx context.Context, pubkey nostr.PubKey) (string, error) {
	credential, err := service.Store.GitHubCredentialByPubkey(ctx, pubkey)
	if errors.Is(err, ErrNotFound) || err == nil && credential.Status == "disconnected" {
		return forge.CredentialSystem, nil
	}
	if err != nil {
		return "", err
	}
	if credential.Status == "active" || credential.Status == "reconnect_required" {
		return forge.CredentialUser, nil
	}
	return "", errors.New("invalid GitHub credential status")
}

func (service *Service) ResolveGitHubUserToken(ctx context.Context, pubkey nostr.PubKey) (string, error) {
	service.refreshMu.Lock()
	defer service.refreshMu.Unlock()
	credential, err := service.Store.GitHubCredentialByPubkey(ctx, pubkey)
	if err != nil || credential.Status != "active" || credential.AppClientID != service.Config.GitHubClientID {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return "", err
		}
		return "", forge.ErrGitHubUserReconnectRequired
	}
	codec := CredentialCodec{Wrapper: service.Wrapper}
	accessToken, err := codec.Decrypt(credential.AccountID, "github-access", credential.EncryptedAccessToken)
	if err != nil || accessToken == "" {
		_ = service.Store.UpdateGitHubCredentialStatus(ctx, credential.AccountID, "reconnect_required", service.Now())
		return "", forge.ErrGitHubUserReconnectRequired
	}
	if credential.AccessTokenExpiresAt.IsZero() || credential.AccessTokenExpiresAt.After(service.Now().Add(5*time.Minute)) {
		service.audit(ctx, AuditEvent{AccountID: credential.AccountID, Pubkey: pubkey, Operation: "github_credential_resolve", GitHubActorID: credential.GitHubUserID, GitHubCredentialType: "user", Result: "success"})
		return accessToken, nil
	}
	refreshToken, err := codec.Decrypt(credential.AccountID, "github-refresh", credential.EncryptedRefreshToken)
	provider, ok := service.Providers["github"].(*GitHubProvider)
	if err != nil || refreshToken == "" || !ok {
		_ = service.Store.UpdateGitHubCredentialStatus(ctx, credential.AccountID, "reconnect_required", service.Now())
		return "", forge.ErrGitHubUserReconnectRequired
	}
	refreshed, err := provider.Refresh(ctx, &oauth2.Token{AccessToken: accessToken, RefreshToken: refreshToken, Expiry: credential.AccessTokenExpiresAt})
	if err != nil {
		_ = service.Store.UpdateGitHubCredentialStatus(ctx, credential.AccountID, "reconnect_required", service.Now())
		return "", forge.ErrGitHubUserReconnectRequired
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = refreshToken
	}
	updated, err := GitHubCredentialFromToken(credential.AccountID, credential.GitHubUserID, credential.Login, credential.AppClientID, refreshed, codec)
	if err != nil || service.Store.SaveGitHubCredential(ctx, updated) != nil {
		return "", errors.New("store refreshed GitHub credential")
	}
	service.audit(ctx, AuditEvent{AccountID: credential.AccountID, Pubkey: pubkey, Operation: "github_credential_refresh", GitHubActorID: credential.GitHubUserID, GitHubCredentialType: "user", Result: "success"})
	return refreshed.AccessToken, nil
}

func (service *Service) MarkGitHubUserReconnectRequired(ctx context.Context, pubkey nostr.PubKey) error {
	credential, err := service.Store.GitHubCredentialByPubkey(ctx, pubkey)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return service.Store.UpdateGitHubCredentialStatus(ctx, credential.AccountID, "reconnect_required", service.Now())
}

func NewService(config ServiceConfig, store Store, wrapper KeyWrapper, providers ...OAuthProvider) (*Service, error) {
	publicURL, err := url.Parse(config.PublicURL)
	if err != nil || publicURL.Scheme == "" || publicURL.Host == "" || publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" {
		return nil, errors.New("auth public URL must be an absolute URL without credentials, query, or fragment")
	}
	if store == nil || wrapper == nil {
		return nil, errors.New("auth store and key wrapper are required")
	}
	if config.SessionLifetime <= 0 {
		config.SessionLifetime = 7 * 24 * time.Hour
	}
	if config.SessionIdle <= 0 {
		config.SessionIdle = 24 * time.Hour
	}
	if config.ChallengeTTL <= 0 {
		config.ChallengeTTL = 10 * time.Minute
	}
	webAuthn, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "Lapis", RPID: publicURL.Hostname(), RPOrigins: []string{publicURL.Scheme + "://" + publicURL.Host},
	})
	if err != nil {
		return nil, err
	}
	service := &Service{Config: config, Store: store, Wrapper: wrapper, Providers: make(map[string]OAuthProvider), Now: time.Now, NIP46: RemoteNIP46Connector{}, WebAuthn: webAuthn}
	for _, provider := range providers {
		if provider != nil {
			service.Providers[provider.Name()] = provider
		}
	}
	return service, nil
}

func (service *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/auth/")
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(path, "login/"):
		service.beginOAuth(w, r, strings.TrimPrefix(path, "login/"))
	case r.Method == http.MethodGet && strings.HasPrefix(path, "callback/"):
		service.finishOAuth(w, r, strings.TrimPrefix(path, "callback/"))
	case r.Method == http.MethodPost && path == "nostr/challenge":
		service.issueNostrChallenge(w, r)
	case r.Method == http.MethodPost && path == "nostr/login":
		service.loginWithNostr(w, r)
	case r.Method == http.MethodGet && path == "session":
		service.getSession(w, r)
	case r.Method == http.MethodPost && path == "logout":
		service.logout(w, r)
	case r.Method == http.MethodDelete && path == "github":
		service.disconnectGitHub(w, r)
	case r.Method == http.MethodPost && path == "relay/sign-auth":
		service.signRelayAuth(w, r)
	case r.Method == http.MethodPost && path == "nip46/connect":
		service.connectNIP46(w, r)
	case r.Method == http.MethodPost && path == "nip46/switch-relays":
		service.switchNIP46Relays(w, r)
	case r.Method == http.MethodPost && path == "nip46/logout":
		service.logoutNIP46(w, r)
	case r.Method == http.MethodPost && path == "passkeys/register/begin":
		service.beginPasskeyRegistration(w, r)
	case r.Method == http.MethodPost && path == "passkeys/register/finish":
		service.finishPasskeyRegistration(w, r)
	case r.Method == http.MethodPost && path == "custody/transfer/begin":
		service.beginCustodyTransfer(w, r)
	case r.Method == http.MethodPost && path == "custody/transfer/authorize":
		service.authorizeCustodyTransfer(w, r)
	case r.Method == http.MethodPost && path == "custody/transfer/complete":
		service.completeCustodyTransfer(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (service *Service) beginOAuth(w http.ResponseWriter, r *http.Request, providerName string) {
	provider, ok := service.Providers[providerName]
	if !ok {
		writeAuthError(w, http.StatusNotFound, "provider_unavailable")
		return
	}
	intent := r.URL.Query().Get("intent")
	if intent == "" {
		intent = "login"
	}
	flow := OAuthFlow{Provider: providerName, Intent: intent, ReturnTo: safeReturnTo(r.URL.Query().Get("return_to")), ExpiresAt: service.Now().Add(service.Config.ChallengeTTL)}
	if intent == "link" {
		_, account, err := service.Authenticate(r.Context(), r)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		flow.AccountID = account.ID
	} else if intent != "login" {
		writeAuthError(w, http.StatusBadRequest, "invalid_auth_intent")
		return
	}
	state, err := randomToken(32)
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "auth_state_failed")
		return
	}
	flow.StateHash = sha256.Sum256([]byte(state))
	flow.Verifier, err = randomToken(32)
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "auth_state_failed")
		return
	}
	flow.Nonce, err = randomToken(32)
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "auth_state_failed")
		return
	}
	if err := service.Store.BeginOAuthFlow(r.Context(), flow); err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "auth_store_unavailable")
		return
	}
	challenge := sha256.Sum256([]byte(flow.Verifier))
	http.Redirect(w, r, provider.AuthorizationURL(state, flow.Nonce, base64.RawURLEncoding.EncodeToString(challenge[:])), http.StatusFound)
}

func (service *Service) finishOAuth(w http.ResponseWriter, r *http.Request, providerName string) {
	provider, ok := service.Providers[providerName]
	if !ok || r.URL.Query().Get("code") == "" || r.URL.Query().Get("state") == "" {
		writeAuthError(w, http.StatusBadRequest, "invalid_oauth_callback")
		return
	}
	stateHash := sha256.Sum256([]byte(r.URL.Query().Get("state")))
	flow, err := service.Store.ConsumeOAuthFlow(r.Context(), stateHash, service.Now())
	if err != nil || flow.Provider != providerName {
		writeAuthError(w, http.StatusBadRequest, "invalid_oauth_state")
		return
	}
	identity, err := provider.Exchange(r.Context(), r.URL.Query().Get("code"), flow.Verifier, flow.Nonce)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "provider_authentication_failed")
		return
	}
	account, lookupErr := service.Store.FindAccountByIdentity(r.Context(), providerName, identity.ProviderID)
	if flow.Intent == "link" {
		if lookupErr == nil && account.ID != flow.AccountID {
			writeAuthError(w, http.StatusConflict, "identity_already_linked")
			return
		}
		if errors.Is(lookupErr, ErrNotFound) {
			err = service.Store.LinkIdentity(r.Context(), ExternalIdentity{
				AccountID: flow.AccountID, Provider: providerName, ProviderID: identity.ProviderID,
				Login: identity.Login, DisplayName: identity.DisplayName, Email: identity.Email,
			})
			if err != nil {
				writeAuthError(w, http.StatusConflict, "identity_already_linked")
				return
			}
		} else if lookupErr != nil {
			writeAuthError(w, http.StatusServiceUnavailable, "auth_store_unavailable")
			return
		}
		account, err = service.Store.AccountByID(r.Context(), flow.AccountID)
	} else if errors.Is(lookupErr, ErrNotFound) {
		account, err = service.createManagedAccount(r.Context(), identity)
	} else {
		err = lookupErr
	}
	if err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "account_update_failed")
		return
	}
	if providerName == "github" {
		credential, credentialErr := GitHubCredentialFromToken(account.ID, identity.ProviderID, identity.Login, service.Config.GitHubClientID, identity.Token, CredentialCodec{Wrapper: service.Wrapper})
		if credentialErr != nil || service.Store.SaveGitHubCredential(r.Context(), credential) != nil {
			writeAuthError(w, http.StatusServiceUnavailable, "github_credential_store_failed")
			return
		}
	}
	if err := service.establishSession(w, r.Context(), account, providerName); err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "session_store_failed")
		return
	}
	service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: providerName, Operation: "login", Result: "success"})
	http.Redirect(w, r, flow.ReturnTo, http.StatusFound)
}

func (service *Service) createManagedAccount(ctx context.Context, identity ProviderIdentity) (Account, error) {
	accountID := uuid.NewString()
	secret := nostr.Generate()
	defer clear(secret[:])
	key, err := EncryptManagedKey(accountID, secret.Public().Hex(), secret[:], service.Wrapper)
	if err != nil {
		return Account{}, err
	}
	return service.Store.CreateManagedAccount(ctx, ExternalIdentity{
		AccountID: accountID, Provider: identity.Provider, ProviderID: identity.ProviderID,
		Login: identity.Login, DisplayName: identity.DisplayName, Email: identity.Email,
	}, key)
}

func (service *Service) issueNostrChallenge(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Pubkey string `json:"pubkey"`
	}
	if !decodeAuthJSON(w, r, &request) {
		return
	}
	pubkey, err := nostr.PubKeyFromHex(request.Pubkey)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid_nostr_pubkey")
		return
	}
	challenge, err := randomToken(32)
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "challenge_failed")
		return
	}
	hash := sha256.Sum256([]byte(challenge))
	if err := service.Store.SaveNostrChallenge(r.Context(), NostrChallenge{ChallengeHash: hash, Pubkey: pubkey, ExpiresAt: service.Now().Add(service.Config.ChallengeTTL)}); err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "auth_store_unavailable")
		return
	}
	writeAuthJSON(w, http.StatusCreated, map[string]string{"challenge": challenge, "expires_at": service.Now().Add(service.Config.ChallengeTTL).UTC().Format(time.RFC3339)})
}

func (service *Service) loginWithNostr(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAuthBody+1))
	if err != nil || len(body) > maxAuthBody {
		writeAuthError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	var request struct {
		Challenge string `json:"challenge"`
	}
	if json.Unmarshal(body, &request) != nil || request.Challenge == "" {
		writeAuthError(w, http.StatusBadRequest, "invalid_challenge")
		return
	}
	expectedURL := strings.TrimRight(service.Config.PublicURL, "/") + "/api/auth/nostr/login"
	authEvent, err := gitforge.VerifyRequestAuth(r, expectedURL, body, service.Now())
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "invalid_nostr_auth")
		return
	}
	hash := sha256.Sum256([]byte(request.Challenge))
	if err := service.Store.ConsumeNostrChallenge(r.Context(), hash, authEvent.PubKey, service.Now()); err != nil {
		writeAuthError(w, http.StatusUnauthorized, "invalid_challenge")
		return
	}
	account, err := service.Store.AccountByPubkey(r.Context(), authEvent.PubKey)
	if errors.Is(err, ErrNotFound) {
		account, err = service.Store.CreateExternalAccount(r.Context(), authEvent.PubKey)
	}
	if err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "session_store_failed")
		return
	}
	if err := service.establishSession(w, r.Context(), account, "nostr"); err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "session_store_failed")
		return
	}
	service.audit(r.Context(), AuditEvent{AccountID: account.ID, Pubkey: account.Pubkey, AuthProvider: "nostr", Operation: "login", Result: "success"})
	writeAuthJSON(w, http.StatusOK, publicAccount(account))
}

func (service *Service) Authenticate(ctx context.Context, r *http.Request) (Session, Account, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return Session{}, Account{}, ErrNotFound
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	now := service.Now()
	session, account, err := service.Store.SessionByTokenHash(ctx, hash, now)
	if err != nil {
		return Session{}, Account{}, err
	}
	if now.Sub(session.LastSeenAt) >= time.Minute {
		idleExpiry := now.Add(service.Config.SessionIdle)
		if idleExpiry.After(session.ExpiresAt) {
			idleExpiry = session.ExpiresAt
		}
		_ = service.Store.TouchSession(ctx, session.ID, now, idleExpiry)
	}
	return session, account, nil
}

func (service *Service) establishSession(w http.ResponseWriter, ctx context.Context, account Account, provider string) error {
	token, err := randomToken(32)
	if err != nil {
		return err
	}
	now := service.Now()
	session := Session{
		ID: uuid.NewString(), AccountID: account.ID, TokenHash: sha256.Sum256([]byte(token)), AuthProvider: provider,
		ExpiresAt: now.Add(service.Config.SessionLifetime), IdleExpires: now.Add(service.Config.SessionIdle), CreatedAt: now, LastSeenAt: now,
	}
	if session.IdleExpires.After(session.ExpiresAt) {
		session.IdleExpires = session.ExpiresAt
	}
	if err := service.Store.CreateSession(ctx, session); err != nil {
		return err
	}
	publicURL, _ := url.Parse(service.Config.PublicURL)
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: token, Path: "/", Domain: service.Config.CookieDomain,
		HttpOnly: true, Secure: publicURL.Scheme == "https", SameSite: http.SameSiteLaxMode,
		Expires: session.ExpiresAt, MaxAge: int(service.Config.SessionLifetime.Seconds()),
	})
	return nil
}

func (service *Service) getSession(w http.ResponseWriter, r *http.Request) {
	session, account, err := service.Authenticate(r.Context(), r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	identities, err := service.Store.ExternalIdentities(r.Context(), account.ID)
	if err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "auth_store_unavailable")
		return
	}
	credential, credentialErr := service.Store.GitHubCredentialByAccount(r.Context(), account.ID)
	githubStatus := "unlinked"
	if credentialErr == nil {
		githubStatus = credential.Status
	}
	binding, bindingErr := service.Store.SignerBindingByAccount(r.Context(), account.ID)
	if bindingErr != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "auth_store_unavailable")
		return
	}
	passkeys, passkeyErr := service.Store.PasskeysByAccount(r.Context(), account.ID)
	if passkeyErr != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "auth_store_unavailable")
		return
	}
	writeAuthJSON(w, http.StatusOK, map[string]any{
		"account": publicAccount(account), "auth_provider": session.AuthProvider,
		"expires_at": session.ExpiresAt, "identities": publicIdentities(identities), "github_status": githubStatus,
		"signer_type": binding.Type, "signer_status": binding.Status, "passkey_count": len(passkeys),
	})
}

func (service *Service) logout(w http.ResponseWriter, r *http.Request) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	session, _, err := service.Authenticate(r.Context(), r)
	if err == nil {
		_ = service.Store.RevokeSession(r.Context(), session.ID, service.Now())
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1, Expires: time.Unix(1, 0)})
	w.WriteHeader(http.StatusNoContent)
}

func (service *Service) disconnectGitHub(w http.ResponseWriter, r *http.Request) {
	if !service.validOrigin(r) {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	_, account, err := service.Authenticate(r.Context(), r)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	if err := service.Store.DisconnectGitHubCredential(r.Context(), account.ID, service.Now()); err != nil {
		writeAuthError(w, http.StatusServiceUnavailable, "github_disconnect_failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (service *Service) signerForAccount(ctx context.Context, account Account) (Signer, error) {
	binding, err := service.Store.SignerBindingByAccount(ctx, account.ID)
	if err != nil || binding.Status != SignerActive || binding.Pubkey != account.Pubkey {
		return nil, ErrSignerUnavailable
	}
	policy := ApplicationSigningPolicy{RelayURL: service.relayURL()}
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

func validateAndSanitizeBunkerURL(value string) (string, error) {
	if len(value) == 0 || len(value) > 4096 {
		return "", errors.New("invalid bunker URL")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "bunker" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("invalid bunker URL")
	}
	if _, err := nostr.PubKeyFromHex(parsed.Host); err != nil {
		return "", errors.New("invalid bunker public key")
	}
	query := parsed.Query()
	relays := query["relay"]
	if len(relays) == 0 {
		return "", errors.New("bunker relay is required")
	}
	for _, relay := range relays {
		relayURL, err := url.Parse(relay)
		if err != nil || relayURL.User != nil || relayURL.Host == "" || relayURL.Fragment != "" || relayURL.Scheme != "wss" && relayURL.Scheme != "ws" {
			return "", errors.New("invalid bunker relay")
		}
	}
	query.Del("secret")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (service *Service) audit(ctx context.Context, event AuditEvent) {
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = service.Now()
	}
	_ = service.Store.SaveAuditEvent(ctx, event)
}

func (service *Service) validOrigin(r *http.Request) bool {
	origin := strings.TrimRight(r.Header.Get("Origin"), "/")
	configured, err := url.Parse(service.Config.PublicURL)
	if err != nil {
		return false
	}
	return origin == configured.Scheme+"://"+configured.Host
}

func safeReturnTo(value string) string {
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\r\n") {
		return "/"
	}
	return value
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func decodeAuthJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAuthBody+1))
	if err != nil || len(body) > maxAuthBody {
		writeAuthError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeAuthError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

func publicAccount(account Account) map[string]any {
	return map[string]any{"id": account.ID, "nostr_pubkey": account.Pubkey.Hex(), "created_at": account.CreatedAt}
}

func publicIdentities(identities []ExternalIdentity) []map[string]string {
	result := make([]map[string]string, 0, len(identities))
	for _, identity := range identities {
		result = append(result, map[string]string{"provider": identity.Provider, "login": identity.Login, "display_name": identity.DisplayName})
	}
	return result
}

func writeAuthJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAuthError(w http.ResponseWriter, status int, code string) {
	writeAuthJSON(w, status, map[string]string{"error": code})
}
