package blossom

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

func signedBlossomHeader(t *testing.T, secret nostr.SecretKey, action, hash string) string {
	t.Helper()
	tags := nostr.Tags{
		{"t", action},
		{"expiration", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)},
		{"server", "media.example"},
	}
	if hash != "" {
		tags = append(tags, nostr.Tag{"x", hash})
	}
	event := nostr.Event{Kind: nostr.KindBlobs, CreatedAt: nostr.Now(), Content: "Asset operation", Tags: tags}
	if err := event.Sign(secret); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return "Nostr " + base64.RawURLEncoding.EncodeToString(encoded)
}

func TestDeleteChecksReferencesBeforeStorage(t *testing.T) {
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")
	hash := strings.Repeat("a", 64)
	called := false
	deleted := false
	allow := false
	server := Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}), Options{
		ServiceURL: "https://media.example",
		CanDelete: func(_ context.Context, auth nostr.Event, gotHash string) (bool, string, error) {
			if auth.PubKey != secret.Public() || gotHash != hash {
				t.Fatalf("unexpected deletion check: %s %s", auth.PubKey, gotHash)
			}
			return allow, "asset is retained by an active profile picture reference", nil
		},
		OnDelete: func(nostr.Event, string) error {
			deleted = true
			return nil
		},
	})
	request := httptest.NewRequest(http.MethodDelete, "/"+hash+".png", nil)
	request.Header.Set("Authorization", signedBlossomHeader(t, secret, "delete", hash))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || called || deleted {
		t.Fatalf("blocked delete: status=%d called=%t deleted=%t", response.Code, called, deleted)
	}
	allow = true
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !called || !deleted {
		t.Fatalf("allowed delete: status=%d called=%t deleted=%t", response.Code, called, deleted)
	}
}

func TestAssetHistoryRequiresOwnerOrAuthorizedRole(t *testing.T) {
	ownerSecret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")
	otherSecret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "2")
	owner := ownerSecret.Public()
	allowRole := false
	called := false
	server := Wrap(http.NotFoundHandler(), Options{
		ServiceURL: "https://media.example",
		CanViewHistory: func(actor, target nostr.PubKey) bool {
			return actor == target || allowRole
		},
		ListHistory: func(_ *http.Request, target nostr.PubKey, limit int, cursor string) (AssetHistoryPage, error) {
			called = true
			if target != owner || limit != 1 || cursor != "" {
				t.Fatalf("unexpected history query: target=%s limit=%d cursor=%q", target, limit, cursor)
			}
			return AssetHistoryPage{
				Items:      []AssetHistoryEntry{{Cursor: "100:2", Owner: owner.Hex(), SHA256: strings.Repeat("a", 64), Action: "uploaded", OccurredAt: 100}},
				NextCursor: "100:2",
			}, nil
		},
	})
	request := httptest.NewRequest(http.MethodGet, "/api/assets/history?limit=1", nil)
	request.Header.Set("Authorization", signedBlossomHeader(t, ownerSecret, "list", ""))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !called || !strings.Contains(response.Body.String(), `"next_cursor":"100:2"`) {
		t.Fatalf("owner history: status=%d called=%t body=%s", response.Code, called, response.Body.String())
	}
	called = false
	request = httptest.NewRequest(http.MethodGet, "/api/assets/history?owner="+owner.Hex()+"&limit=1", nil)
	request.Header.Set("Authorization", signedBlossomHeader(t, otherSecret, "list", ""))
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || called {
		t.Fatalf("unprivileged history: status=%d called=%t", response.Code, called)
	}
	allowRole = true
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !called {
		t.Fatalf("authorized history: status=%d called=%t", response.Code, called)
	}
}
