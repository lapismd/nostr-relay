package gitforge

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fiatjaf.com/nostr"
)

const AuthWindow = 60 * time.Second
const maxAuthorizationHeaderBytes = 96 << 10

// VerifyGitAuth implements Buzz's Git credential profile. Git signs the
// repository root during the initial challenge and reuses that credential for
// subsequent pack requests, so method, payload, and one-shot replay binding do
// not apply to this transport.
func VerifyGitAuth(r *http.Request, expectedRepositoryURL string, now time.Time) (nostr.Event, error) {
	event, err := decodeNostrAuthorization(singleAuthorizationHeader(r))
	if err != nil {
		return event, err
	}
	if err := validateAuthEvent(event, now); err != nil {
		return event, err
	}
	if _, found := tagValueRequired(event.Tags, "method"); !found {
		return event, errors.New("auth event is missing method")
	}
	signedURL, found := tagValueRequired(event.Tags, "u")
	if !found || normalizeAuthURL(signedURL) != normalizeAuthURL(expectedRepositoryURL) {
		return event, errors.New("auth event URL does not match repository")
	}
	return event, nil
}

// VerifyReadAuth validates the exact method and URL used by browser repository
// and PR reads. Empty-body GET/HEAD requests do not require a payload tag.
func VerifyReadAuth(r *http.Request, expectedURL string, now time.Time) (nostr.Event, error) {
	event, err := decodeNostrAuthorization(singleAuthorizationHeader(r))
	if err != nil {
		return event, err
	}
	if err := validateAuthEvent(event, now); err != nil {
		return event, err
	}
	method, found := tagValueRequired(event.Tags, "method")
	if !found || !strings.EqualFold(method, r.Method) {
		return event, errors.New("auth event method does not match request")
	}
	signedURL, found := tagValueRequired(event.Tags, "u")
	if !found || normalizeAuthURL(signedURL) != normalizeAuthURL(expectedURL) {
		return event, errors.New("auth event URL does not match request")
	}
	return event, nil
}

// VerifyRequestAuth validates an exact NIP-98 application request. Mutating
// requests bind the body digest; empty GET and HEAD requests do not require a
// payload tag.
func VerifyRequestAuth(r *http.Request, expectedURL string, body []byte, now time.Time) (nostr.Event, error) {
	event, err := VerifyReadAuth(r, expectedURL, now)
	if err != nil {
		return event, err
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if len(body) != 0 {
			return event, errors.New("read auth request body must be empty")
		}
		return event, nil
	}
	payload, found := tagValueRequired(event.Tags, "payload")
	digest := sha256.Sum256(body)
	if !found || !strings.EqualFold(payload, hex.EncodeToString(digest[:])) {
		return event, errors.New("auth event payload does not match request")
	}
	return event, nil
}

func validateAuthEvent(event nostr.Event, now time.Time) error {
	if event.Kind != nostr.KindHTTPAuth || event.Content != "" || !event.CheckID() || !event.VerifySignature() {
		return errors.New("invalid NIP-98 auth event")
	}
	created := time.Unix(int64(event.CreatedAt), 0)
	if created.Before(now.Add(-AuthWindow)) || created.After(now.Add(AuthWindow)) {
		return errors.New("auth event is outside the allowed time window")
	}
	return nil
}

func decodeNostrAuthorization(header string) (nostr.Event, error) {
	var event nostr.Event
	header = strings.TrimSpace(header)
	if len(header) < 7 || len(header) > maxAuthorizationHeaderBytes || !strings.EqualFold(header[:6], "Nostr ") {
		return event, errors.New("missing Nostr authorization")
	}
	encoded := strings.TrimSpace(header[6:])
	var decoded []byte
	var err error
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err = encoding.DecodeString(encoded)
		if err == nil {
			break
		}
	}
	if err != nil {
		return event, errors.New("invalid base64 auth event")
	}
	if err := json.Unmarshal(decoded, &event); err != nil {
		return event, fmt.Errorf("invalid auth event JSON: %w", err)
	}
	return event, nil
}

func singleAuthorizationHeader(r *http.Request) string {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func normalizeAuthURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path != "/" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}
	return parsed.String()
}

func tagValueRequired(tags nostr.Tags, name string) (string, bool) {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] != "" {
			return tag[1], true
		}
	}
	return "", false
}
