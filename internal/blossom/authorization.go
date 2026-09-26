package blossom

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
)

var errInvalidToken = errors.New("invalid Blossom authorization")
var declaredTagValue = regexp.MustCompile(`^[a-z0-9]+(?:[-:.][a-z0-9]+)*$`)

func authorize(r *http.Request, serviceURL, action, hash string, requiredHash bool) (*nostr.Event, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return nil, nil
	}
	if !strings.HasPrefix(header, "Nostr ") {
		return nil, errInvalidToken
	}
	encoded := strings.TrimPrefix(header, "Nostr ")
	var raw []byte
	var err error
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding, base64.RawStdEncoding} {
		raw, err = encoding.DecodeString(encoded)
		if err == nil {
			break
		}
	}
	if err != nil || len(raw) > 16<<10 {
		return nil, errInvalidToken
	}
	var event nostr.Event
	if json.Unmarshal(raw, &event) != nil || event.Kind != nostr.KindBlobs || !event.CheckID() || !event.VerifySignature() || strings.TrimSpace(event.Content) == "" {
		return nil, errInvalidToken
	}
	now := time.Now().Unix()
	if int64(event.CreatedAt) > now {
		return nil, errInvalidToken
	}
	expiration := event.Tags.Find("expiration")
	if len(expiration) < 2 {
		return nil, errInvalidToken
	}
	expires, err := strconv.ParseInt(expiration[1], 10, 64)
	if err != nil || expires <= now || event.Tags.FindWithValue("t", action) == nil {
		return nil, errInvalidToken
	}
	parsed, err := url.Parse(serviceURL)
	if err != nil || parsed.Host == "" {
		return nil, errInvalidToken
	}
	hasServer := false
	serverMatched := false
	for tag := range event.Tags.FindAll("server") {
		if len(tag) < 2 {
			return nil, errInvalidToken
		}
		hasServer = true
		candidate := strings.ToLower(tag[1])
		serverMatched = serverMatched || candidate == strings.ToLower(parsed.Host) || candidate == strings.ToLower(parsed.Hostname()) || candidate == strings.ToLower(serviceURL)
	}
	if hasServer && !serverMatched {
		return nil, errInvalidToken
	}
	hasHash := false
	hashMatched := false
	for tag := range event.Tags.FindAll("x") {
		if len(tag) < 2 || !blobHash.MatchString(tag[1]) {
			return nil, errInvalidToken
		}
		hasHash = true
		hashMatched = hashMatched || tag[1] == hash
	}
	if (requiredHash && !hasHash) || (hash != "" && hasHash && !hashMatched) {
		return nil, errInvalidToken
	}
	for _, tag := range event.Tags {
		if len(tag) > 0 && (tag[0] == "lapis-purpose" || tag[0] == "lapis-plugin" || tag[0] == "lapis-slot") {
			if len(tag) != 2 || len(tag[1]) > 128 || !declaredTagValue.MatchString(tag[1]) {
				return nil, errInvalidToken
			}
		}
	}
	// Upstream Khatru currently decodes padded standard Base64 only.
	r.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(raw))
	return &event, nil
}
