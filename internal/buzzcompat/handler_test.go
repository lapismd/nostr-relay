package buzzcompat

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fiatjaf.com/nostr"
	"net/http"
	"strconv"
	"testing"
	"time"
)

type testPubkeyKey struct{}

func authenticatedRequest(t *testing.T, secret nostr.SecretKey, method, target string, body []byte, now time.Time) *http.Request {
	t.Helper()
	payload := sha256.Sum256(body)
	auth := nostr.Event{
		CreatedAt: nostr.Timestamp(now.Unix()),
		Kind:      nostr.KindHTTPAuth,
		Tags: nostr.Tags{
			{"u", target},
			{"method", method},
			{"payload", hex.EncodeToString(payload[:])},
			{"nonce", nostr.Generate().Hex()},
		},
	}
	if err := auth.Sign(secret); err != nil {
		t.Fatal(err)
	}
	authJSON, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(authJSON))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func signedEvent(t *testing.T, secret nostr.SecretKey, kind nostr.Kind, content string, now time.Time) nostr.Event {
	t.Helper()
	event := nostr.Event{CreatedAt: nostr.Timestamp(now.Unix()), Kind: kind, Content: content}
	if err := event.Sign(secret); err != nil {
		t.Fatal(err)
	}
	return event
}

func blossomAuthorization(t *testing.T, secret nostr.SecretKey, operation string, extraTags nostr.Tags) (nostr.Event, []byte, string) {
	t.Helper()
	event := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      nostr.KindBlobs,
		Content:   operation + " buzz-media",
		Tags: append(nostr.Tags{
			{"t", operation},
			{"expiration", strconv.FormatInt(time.Now().Add(5*time.Minute).Unix(), 10)},
		}, extraTags...),
	}
	if err := event.Sign(secret); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return event, raw, "Nostr " + base64.RawURLEncoding.EncodeToString(raw)
}
