package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"zooid/internal/gitforge"
)

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
	if err := service.establishSession(w, r.Context(), account, "nostr", service.sessionCookieSameSiteForOrigin(r.Header.Get("Origin"))); err != nil {
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

func (service *Service) establishSession(w http.ResponseWriter, ctx context.Context, account Account, provider string, sameSite http.SameSite) error {
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
		HttpOnly: true, Secure: publicURL.Scheme == "https", SameSite: sameSite,
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
	publicURL, _ := url.Parse(service.Config.PublicURL)
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", Domain: service.Config.CookieDomain, HttpOnly: true, Secure: publicURL.Scheme == "https", SameSite: service.sessionCookieSameSiteForOrigin(r.Header.Get("Origin")), MaxAge: -1, Expires: time.Unix(1, 0)})
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

func (service *Service) safeReturnTo(value string) (string, bool) {
	if value == "" {
		return "/", true
	}
	if strings.ContainsAny(value, "\r\n") || strings.HasPrefix(value, "//") {
		return "", false
	}
	if strings.HasPrefix(value, "/") {
		return value, true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}
	origin := strings.ToLower(parsed.Scheme + "://" + parsed.Host)
	return value, service.originAllowed(origin)
}

func (service *Service) sessionCookieSameSiteForOrigin(rawOrigin string) http.SameSite {
	publicURL, err := url.Parse(service.Config.PublicURL)
	if err != nil || publicURL.Scheme != "https" {
		return http.SameSiteLaxMode
	}
	origin, err := normalizeBrowserOrigin(rawOrigin)
	if err != nil {
		return http.SameSiteLaxMode
	}
	for _, rule := range service.allowedOriginRules {
		if rule.matches(origin) && rule.loopback {
			return http.SameSiteNoneMode
		}
	}
	return http.SameSiteLaxMode
}

func (service *Service) sessionCookieSameSiteForReturnTo(returnTo string) http.SameSite {
	if strings.HasPrefix(returnTo, "/") || strings.ContainsAny(returnTo, "\r\n") {
		return http.SameSiteLaxMode
	}
	parsed, err := url.Parse(returnTo)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return http.SameSiteLaxMode
	}
	return service.sessionCookieSameSiteForOrigin(parsed.Scheme + "://" + parsed.Host)
}
