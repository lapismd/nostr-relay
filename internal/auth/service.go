package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"zooid/internal/forge"
)

const (
	SessionCookieName = "lapis_session"
	maxAuthBody       = 128 << 10
)

type ServiceConfig struct {
	PublicURL             string
	CookieDomain          string
	SessionLifetime       time.Duration
	SessionIdle           time.Duration
	ChallengeTTL          time.Duration
	GitHubClientID        string
	AllowedBrowserOrigins []string
	WebAuthnRPID          string
	WebAuthnOrigins       []string
}

type Service struct {
	Config             ServiceConfig
	Store              Store
	Wrapper            KeyWrapper
	Providers          map[string]OAuthProvider
	Now                func() time.Time
	refreshMu          sync.Mutex
	NIP46              NIP46Connector
	WebAuthn           *webauthn.WebAuthn
	allowedOrigins     map[string]struct{}
	allowedOriginRules []browserOriginRule
	webAuthnOrigins    map[string]struct{}
}

type browserOriginRule struct {
	exact            string
	scheme           string
	hostname         string
	loopback         bool
	loopbackWildcard bool
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
	allowedOrigins := make(map[string]struct{}, len(config.AllowedBrowserOrigins)+1)
	allowedOriginRules := make([]browserOriginRule, 0, len(config.AllowedBrowserOrigins)+1)
	publicOrigin := strings.ToLower(publicURL.Scheme + "://" + publicURL.Host)
	allowedOrigins[publicOrigin] = struct{}{}
	allowedOriginRules = append(allowedOriginRules, exactBrowserOriginRule(publicOrigin))
	for _, value := range config.AllowedBrowserOrigins {
		rule, key, err := parseAllowedBrowserOrigin(value)
		if err != nil {
			return nil, err
		}
		allowedOrigins[key] = struct{}{}
		allowedOriginRules = append(allowedOriginRules, rule)
	}
	webAuthnRPID := strings.ToLower(strings.TrimSpace(config.WebAuthnRPID))
	if webAuthnRPID == "" {
		webAuthnRPID = strings.ToLower(publicURL.Hostname())
	}
	configuredWebAuthnOrigins := config.WebAuthnOrigins
	if len(configuredWebAuthnOrigins) == 0 {
		configuredWebAuthnOrigins = []string{publicOrigin}
	}
	webAuthnOrigins := make(map[string]struct{}, len(configuredWebAuthnOrigins))
	rpOrigins := make([]string, 0, len(configuredWebAuthnOrigins))
	for _, value := range configuredWebAuthnOrigins {
		origin, err := normalizeBrowserOrigin(value)
		if err != nil {
			return nil, err
		}
		if _, allowed := allowedOrigins[origin]; !allowed {
			return nil, errors.New("WebAuthn origins must also be allowed browser origins")
		}
		parsed, _ := url.Parse(origin)
		hostname := strings.ToLower(parsed.Hostname())
		if hostname != webAuthnRPID && !strings.HasSuffix(hostname, "."+webAuthnRPID) {
			return nil, errors.New("WebAuthn origin is not compatible with RP ID")
		}
		if _, duplicate := webAuthnOrigins[origin]; duplicate {
			return nil, errors.New("WebAuthn origins must be unique")
		}
		webAuthnOrigins[origin] = struct{}{}
		rpOrigins = append(rpOrigins, origin)
	}
	webAuthn, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "Lapis", RPID: webAuthnRPID, RPOrigins: rpOrigins,
	})
	if err != nil {
		return nil, err
	}
	service := &Service{Config: config, Store: store, Wrapper: wrapper, Providers: make(map[string]OAuthProvider), Now: time.Now, NIP46: RemoteNIP46Connector{}, WebAuthn: webAuthn, allowedOrigins: allowedOrigins, allowedOriginRules: allowedOriginRules, webAuthnOrigins: webAuthnOrigins}
	for _, provider := range providers {
		if provider != nil {
			service.Providers[provider.Name()] = provider
		}
	}
	return service, nil
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
