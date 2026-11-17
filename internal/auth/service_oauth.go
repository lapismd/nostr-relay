package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"net/http"
	"net/url"
	"time"
	"zooid/internal/forge"
)

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
	returnTo, ok := service.safeReturnTo(r.URL.Query().Get("return_to"))
	if !ok {
		writeAuthError(w, http.StatusBadRequest, "invalid_return_url")
		return
	}
	flow := OAuthFlow{Provider: providerName, Intent: intent, ReturnTo: returnTo, ExpiresAt: service.Now().Add(service.Config.ChallengeTTL)}
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
	if !ok || r.URL.Query().Get("state") == "" {
		writeAuthError(w, http.StatusBadRequest, "invalid_oauth_callback")
		return
	}
	stateHash := sha256.Sum256([]byte(r.URL.Query().Get("state")))
	flow, err := service.Store.ConsumeOAuthFlow(r.Context(), stateHash, service.Now())
	if err != nil || flow.Provider != providerName {
		writeAuthError(w, http.StatusBadRequest, "invalid_oauth_state")
		return
	}
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		redirectOAuthFailure(w, r, flow.ReturnTo, "provider_authentication_failed")
		return
	}
	identity, err := provider.Exchange(r.Context(), r.URL.Query().Get("code"), flow.Verifier, flow.Nonce)
	if err != nil {
		redirectOAuthFailure(w, r, flow.ReturnTo, "provider_authentication_failed")
		return
	}
	account, lookupErr := service.Store.FindAccountByIdentity(r.Context(), providerName, identity.ProviderID)
	if flow.Intent == "link" {
		if lookupErr == nil && account.ID != flow.AccountID {
			redirectOAuthFailure(w, r, flow.ReturnTo, "identity_already_linked")
			return
		}
		if errors.Is(lookupErr, ErrNotFound) {
			err = service.Store.LinkIdentity(r.Context(), ExternalIdentity{
				AccountID: flow.AccountID, Provider: providerName, ProviderID: identity.ProviderID,
				Login: identity.Login, DisplayName: identity.DisplayName, Email: identity.Email,
			})
			if err != nil {
				redirectOAuthFailure(w, r, flow.ReturnTo, "identity_already_linked")
				return
			}
		} else if lookupErr != nil {
			redirectOAuthFailure(w, r, flow.ReturnTo, "auth_store_unavailable")
			return
		}
		account, err = service.Store.AccountByID(r.Context(), flow.AccountID)
	} else if errors.Is(lookupErr, ErrNotFound) {
		account, err = service.createManagedAccount(r.Context(), identity)
	} else {
		err = lookupErr
	}
	if err != nil {
		redirectOAuthFailure(w, r, flow.ReturnTo, "account_update_failed")
		return
	}
	if providerName == "github" {
		credential, credentialErr := GitHubCredentialFromToken(account.ID, identity.ProviderID, identity.Login, service.Config.GitHubClientID, identity.Token, CredentialCodec{Wrapper: service.Wrapper})
		if credentialErr != nil || service.Store.SaveGitHubCredential(r.Context(), credential) != nil {
			redirectOAuthFailure(w, r, flow.ReturnTo, "github_credential_store_failed")
			return
		}
	}
	if err := service.establishSession(w, r.Context(), account, providerName, service.sessionCookieSameSiteForReturnTo(flow.ReturnTo)); err != nil {
		redirectOAuthFailure(w, r, flow.ReturnTo, "session_store_failed")
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

func redirectOAuthFailure(w http.ResponseWriter, r *http.Request, returnTo, code string) {
	target, err := url.Parse(returnTo)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, code)
		return
	}
	query := target.Query()
	query.Set("auth_error", code)
	target.RawQuery = query.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}
