package auth

import (
	"errors"
	"fiatjaf.com/nostr"
	"net/http"
	"net/url"
	"strings"
)

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

func (service *Service) validOrigin(r *http.Request) bool {
	_, allowed := service.allowedOrigin(r.Header.Get("Origin"))
	return allowed
}

func (service *Service) validWebAuthnOrigin(r *http.Request) bool {
	rawOrigin := strings.TrimSpace(r.Header.Get("Origin"))
	if rawOrigin == "" {
		publicURL, err := url.Parse(service.Config.PublicURL)
		if err != nil {
			return false
		}
		rawOrigin = publicURL.Scheme + "://" + publicURL.Host
	}
	origin, err := normalizeBrowserOrigin(rawOrigin)
	if err != nil {
		return false
	}
	_, allowed := service.webAuthnOrigins[origin]
	return allowed
}

func (service *Service) allowedOrigin(rawOrigin string) (string, bool) {
	origin, err := normalizeBrowserOrigin(rawOrigin)
	if err != nil {
		return "", false
	}
	return origin, service.originAllowed(origin)
}

func (service *Service) originAllowed(origin string) bool {
	for _, rule := range service.allowedOriginRules {
		if rule.matches(origin) {
			return true
		}
	}
	return false
}

func normalizeBrowserOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("auth browser origin must be an absolute origin")
	}
	hostname := strings.ToLower(parsed.Hostname())
	localhost := isLocalBrowserHost(hostname)
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && localhost) {
		return "", errors.New("auth browser origin must use HTTPS, except for localhost development")
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), nil
}

func parseAllowedBrowserOrigin(value string) (browserOriginRule, string, error) {
	raw := strings.TrimSpace(value)
	if strings.HasSuffix(raw, ":*") {
		base := strings.TrimSuffix(raw, ":*")
		parsed, err := url.Parse(base)
		if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return browserOriginRule{}, "", errors.New("auth browser origin must be an absolute origin")
		}
		hostname := strings.ToLower(parsed.Hostname())
		if parsed.Scheme != "http" || !isLocalBrowserHost(hostname) {
			return browserOriginRule{}, "", errors.New("auth browser origin wildcard must be an HTTP loopback origin")
		}
		return browserOriginRule{scheme: "http", hostname: hostname, loopback: true, loopbackWildcard: true}, strings.ToLower(parsed.Scheme + "://" + parsed.Hostname() + ":*"), nil
	}
	origin, err := normalizeBrowserOrigin(raw)
	if err != nil {
		return browserOriginRule{}, "", err
	}
	return exactBrowserOriginRule(origin), origin, nil
}

func exactBrowserOriginRule(origin string) browserOriginRule {
	parsed, _ := url.Parse(origin)
	return browserOriginRule{exact: origin, loopback: parsed.Scheme == "http" && isLocalBrowserHost(parsed.Hostname())}
}

func (rule browserOriginRule) matches(origin string) bool {
	if rule.exact != "" {
		return origin == rule.exact
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed == nil {
		return false
	}
	return rule.loopbackWildcard && parsed.Scheme == rule.scheme && strings.EqualFold(parsed.Hostname(), rule.hostname) && parsed.Port() != ""
}

func isLocalBrowserHost(hostname string) bool {
	hostname = strings.ToLower(strings.TrimSuffix(hostname, "."))
	return hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" || strings.HasSuffix(hostname, ".localhost")
}
