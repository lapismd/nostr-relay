package agentbunker

import (
	"errors"
	"net/url"
	"strings"
)

func managedBrowserOriginAllowed(rawOrigin string, allowedOrigins []string) (string, bool) {
	origin, err := normalizeManagedBrowserOrigin(rawOrigin)
	if err != nil {
		return "", false
	}
	for _, allowed := range allowedOrigins {
		if managedBrowserOriginMatches(allowed, origin) {
			return origin, true
		}
	}
	return "", false
}

func managedBrowserOriginMatches(rawAllowed, origin string) bool {
	allowed := strings.TrimSpace(rawAllowed)
	if strings.HasSuffix(allowed, ":*") {
		base := strings.TrimSuffix(allowed, ":*")
		parsedAllowed, err := url.Parse(base)
		if err != nil || parsedAllowed == nil || parsedAllowed.Scheme != "http" || !isManagedLocalBrowserHost(parsedAllowed.Hostname()) {
			return false
		}
		parsedOrigin, err := url.Parse(origin)
		return err == nil && parsedOrigin != nil && parsedOrigin.Scheme == "http" && parsedOrigin.Port() != "" && strings.EqualFold(parsedOrigin.Hostname(), parsedAllowed.Hostname())
	}
	normalized, err := normalizeManagedBrowserOrigin(allowed)
	return err == nil && normalized == origin
}

func normalizeManagedBrowserOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("browser origin must be an absolute origin")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isManagedLocalBrowserHost(parsed.Hostname())) {
		return "", errors.New("browser origin must use HTTPS, except for localhost development")
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), nil
}

func isManagedLocalBrowserHost(hostname string) bool {
	hostname = strings.ToLower(strings.TrimSuffix(hostname, "."))
	return hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" || strings.HasSuffix(hostname, ".localhost")
}
