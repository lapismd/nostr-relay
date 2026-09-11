package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

const maxProviderResponse = 1 << 20

type ProviderIdentity struct {
	Provider    string
	ProviderID  string
	Login       string
	DisplayName string
	Email       string
	Token       *oauth2.Token
}

type OAuthProvider interface {
	Name() string
	AuthorizationURL(state, nonce, codeChallenge string) string
	Exchange(context.Context, string, string, string) (ProviderIdentity, error)
}

type GitHubProvider struct {
	config *oauth2.Config
	client *http.Client
}

func NewGitHubProvider(clientID, clientSecret, redirectURL string, client *http.Client) (*GitHubProvider, error) {
	if clientID == "" || clientSecret == "" || redirectURL == "" {
		return nil, errors.New("GitHub OAuth configuration is incomplete")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &GitHubProvider{config: &oauth2.Config{
		ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
		Endpoint: github.Endpoint, Scopes: []string{"repo"},
	}, client: client}, nil
}

func (*GitHubProvider) Name() string { return "github" }

func (provider *GitHubProvider) AuthorizationURL(state, _ string, codeChallenge string) string {
	return provider.config.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

func (provider *GitHubProvider) Exchange(ctx context.Context, code, verifier, _ string) (ProviderIdentity, error) {
	token, err := provider.config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return ProviderIdentity{}, errors.New("GitHub authorization code exchange failed")
	}
	client := oauth2.NewClient(context.WithValue(ctx, oauth2.HTTPClient, provider.client), oauth2.StaticTokenSource(token))
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	response, err := client.Do(request)
	if err != nil {
		return ProviderIdentity{}, errors.New("GitHub identity request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ProviderIdentity{}, fmt.Errorf("GitHub identity request returned %d", response.StatusCode)
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxProviderResponse)).Decode(&user); err != nil || user.ID <= 0 || user.Login == "" {
		return ProviderIdentity{}, errors.New("GitHub returned an invalid identity")
	}
	return ProviderIdentity{
		Provider: "github", ProviderID: strconv.FormatInt(user.ID, 10), Login: user.Login,
		DisplayName: user.Name, Email: strings.TrimSpace(user.Email), Token: token,
	}, nil
}

func (provider *GitHubProvider) Refresh(ctx context.Context, token *oauth2.Token) (*oauth2.Token, error) {
	if token == nil || token.RefreshToken == "" {
		return nil, errors.New("GitHub refresh token is missing")
	}
	refreshed, err := provider.config.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, provider.client), token).Token()
	if err != nil || refreshed.AccessToken == "" {
		return nil, errors.New("GitHub token refresh failed")
	}
	return refreshed, nil
}

type GoogleProvider struct {
	config   *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func NewGoogleProvider(ctx context.Context, clientID, clientSecret, redirectURL string) (*GoogleProvider, error) {
	if clientID == "" || clientSecret == "" || redirectURL == "" {
		return nil, errors.New("Google OIDC configuration is incomplete")
	}
	provider, err := oidc.NewProvider(ctx, "https://accounts.google.com")
	if err != nil {
		return nil, fmt.Errorf("initialize Google OIDC: %w", err)
	}
	return &GoogleProvider{
		config: &oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
			Endpoint: provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
	}, nil
}

func (*GoogleProvider) Name() string { return "google" }

func (provider *GoogleProvider) AuthorizationURL(state, nonce, codeChallenge string) string {
	return provider.config.AuthCodeURL(state,
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

func (provider *GoogleProvider) Exchange(ctx context.Context, code, verifier, expectedNonce string) (ProviderIdentity, error) {
	token, err := provider.config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return ProviderIdentity{}, errors.New("Google authorization code exchange failed")
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return ProviderIdentity{}, errors.New("Google response did not contain an ID token")
	}
	idToken, err := provider.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return ProviderIdentity{}, errors.New("Google ID token verification failed")
	}
	var claims struct {
		Subject       string `json:"sub"`
		Nonce         string `json:"nonce"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil || claims.Subject == "" || claims.Nonce != expectedNonce || !claims.EmailVerified {
		return ProviderIdentity{}, errors.New("Google ID token claims are invalid")
	}
	return ProviderIdentity{
		Provider: "google", ProviderID: claims.Subject, DisplayName: claims.Name,
		Email: strings.TrimSpace(strings.ToLower(claims.Email)), Token: token,
	}, nil
}
