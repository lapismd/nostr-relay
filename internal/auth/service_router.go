package auth

import (
	"net/http"
	"sort"
	"strings"
)

func (service *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if service.handleCORS(w, r) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/auth/")
	switch {
	case r.Method == http.MethodGet && path == "config":
		service.getConfig(w, r)
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
	case r.Method == http.MethodPost && path == "events/sign":
		service.signApplicationEvent(w, r)
	case r.Method == http.MethodPost && path == "nip44/encrypt":
		service.nip44Encrypt(w, r)
	case r.Method == http.MethodPost && path == "nip44/decrypt":
		service.nip44Decrypt(w, r)
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

func (service *Service) handleCORS(w http.ResponseWriter, r *http.Request) bool {
	rawOrigin := strings.TrimSpace(r.Header.Get("Origin"))
	if rawOrigin == "" {
		return false
	}
	w.Header().Add("Vary", "Origin")
	origin, allowed := service.allowedOrigin(rawOrigin)
	if !allowed {
		writeAuthError(w, http.StatusForbidden, "invalid_origin")
		return true
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	if r.Method != http.MethodOptions {
		return false
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Correlation-ID")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (service *Service) getConfig(w http.ResponseWriter, r *http.Request) {
	providers := make([]string, 0, len(service.Providers))
	for name := range service.Providers {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	writeAuthJSON(w, http.StatusOK, map[string]any{
		"providers": providers,
		"capabilities": map[string]bool{
			"github_linking":       service.Providers["github"] != nil,
			"managed_signing":      true,
			"nip44":                true,
			"nip46":                true,
			"nostr_login":          true,
			"passkey_registration": service.validWebAuthnOrigin(r),
			"github_disconnect":    service.Providers["github"] != nil,
			"nip46_management":     true,
			"custody_transfer":     service.validWebAuthnOrigin(r),
		},
	})
}
