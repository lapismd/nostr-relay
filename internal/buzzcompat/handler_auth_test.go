package buzzcompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fiatjaf.com/nostr"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNIP98RejectsPayloadMismatchAndReplay(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	secret := nostr.Generate()
	handler := New(Backend{
		WithAuthenticatedPubkey: func(ctx context.Context, _ nostr.PubKey) context.Context { return ctx },
		AuthorizeQuery:          func(context.Context, nostr.Filter) (bool, string) { return false, "" },
		Query:                   func(context.Context, nostr.Filter) iter.Seq[nostr.Event] { return func(func(nostr.Event) bool) {} },
		NIP11:                   func(http.ResponseWriter, *http.Request) {},
	})
	handler.now = func() time.Time { return now }
	server := httptest.NewServer(handler.Wrap(http.NotFoundHandler()))
	defer server.Close()

	goodBody := []byte(`[]`)
	authenticated := authenticatedRequest(t, secret, http.MethodPost, server.URL+"/query", goodBody, now)
	tamperedBody := []byte(`[{}]`)
	request := authenticated.Clone(context.Background())
	request.Body = io.NopCloser(bytes.NewReader(tamperedBody))
	request.ContentLength = int64(len(tamperedBody))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("payload mismatch returned %s", response.Status)
	}

	first, err := http.DefaultClient.Do(authenticatedRequest(t, secret, http.MethodPost, server.URL+"/query", goodBody, now))
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first request returned %s", first.Status)
	}

	sharedAuth := authenticatedRequest(t, secret, http.MethodPost, server.URL+"/query", goodBody, now).Header.Get("Authorization")
	requestOne, _ := http.NewRequest(http.MethodPost, server.URL+"/query", bytes.NewReader(goodBody))
	requestOne.Header.Set("Authorization", sharedAuth)
	responseOne, err := http.DefaultClient.Do(requestOne)
	if err != nil {
		t.Fatal(err)
	}
	responseOne.Body.Close()
	requestTwo, _ := http.NewRequest(http.MethodPost, server.URL+"/query", bytes.NewReader(goodBody))
	requestTwo.Header.Set("Authorization", sharedAuth)
	responseTwo, err := http.DefaultClient.Do(requestTwo)
	if err != nil {
		t.Fatal(err)
	}
	responseTwo.Body.Close()
	if responseOne.StatusCode != http.StatusOK || responseTwo.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay statuses were %s then %s", responseOne.Status, responseTwo.Status)
	}
}

func TestEventsUsesPublishPipeline(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	secret := nostr.Generate()
	event := signedEvent(t, secret, nostr.KindProfileMetadata, `{}`, now)
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	published := false
	broadcast := false
	handler := New(Backend{
		WithAuthenticatedPubkey: func(ctx context.Context, pubkey nostr.PubKey) context.Context {
			return context.WithValue(ctx, testPubkeyKey{}, pubkey)
		},
		Publish: func(ctx context.Context, got nostr.Event) (bool, error) {
			pubkey, _ := ctx.Value(testPubkeyKey{}).(nostr.PubKey)
			published = pubkey == event.PubKey && got.ID == event.ID
			return false, nil
		},
		Broadcast: func(got nostr.Event) { broadcast = got.ID == event.ID },
		NIP11:     func(http.ResponseWriter, *http.Request) {},
	})
	handler.now = func() time.Time { return now }
	server := httptest.NewServer(handler.Wrap(http.NotFoundHandler()))
	defer server.Close()

	response, err := http.DefaultClient.Do(authenticatedRequest(t, secret, http.MethodPost, server.URL+"/events", body, now))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !published || !broadcast {
		t.Fatalf("status=%s published=%v broadcast=%v", response.Status, published, broadcast)
	}
	var result submitResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Accepted || result.EventID != event.ID {
		t.Fatalf("unexpected response %#v", result)
	}
}

func TestEventsRejectsBuzzEphemeralKindsOverHTTP(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	secret := nostr.Generate()
	for _, kind := range []nostr.Kind{20001, 20002, 24200} {
		t.Run(strconv.Itoa(int(kind)), func(t *testing.T) {
			event := signedEvent(t, secret, kind, "online", now)
			body, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			published := false
			handler := New(Backend{
				WithAuthenticatedPubkey: func(ctx context.Context, _ nostr.PubKey) context.Context { return ctx },
				Publish:                 func(context.Context, nostr.Event) (bool, error) { published = true; return false, nil },
			})
			handler.now = func() time.Time { return now }
			server := httptest.NewServer(handler.Wrap(http.NotFoundHandler()))
			defer server.Close()
			response, err := http.DefaultClient.Do(authenticatedRequest(t, secret, http.MethodPost, server.URL+"/events", body, now))
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusBadRequest || published {
				t.Fatalf("status=%s published=%v", response.Status, published)
			}
		})
	}
}

func TestAuthenticatePassesSignedTimestampAndSingleOwnerTag(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	secret := nostr.Generate()
	called := false
	handler := New(Backend{
		Authenticate: func(ctx context.Context, auth HTTPAuth) (context.Context, error) {
			called = auth.PubKey == secret.Public() && auth.SignedAt == uint64(now.Unix()) && auth.AuthTag == `["auth","owner","","sig"]`
			return ctx, nil
		},
		AuthorizeQuery: func(context.Context, nostr.Filter) (bool, string) { return false, "" },
		Query:          func(context.Context, nostr.Filter) iter.Seq[nostr.Event] { return func(func(nostr.Event) bool) {} },
	})
	handler.now = func() time.Time { return now }
	server := httptest.NewServer(handler.Wrap(http.NotFoundHandler()))
	defer server.Close()
	request := authenticatedRequest(t, secret, http.MethodPost, server.URL+"/query", []byte(`[]`), now)
	request.Header.Set("X-Auth-Tag", `["auth","owner","","sig"]`)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !called {
		t.Fatalf("status=%s called=%v", response.Status, called)
	}
}

func TestOwnerAuthHeaderMustBeSingleAndBounded(t *testing.T) {
	if _, err := extractAuthTagHeader([]string{"one", "two"}); err == nil {
		t.Fatal("multiple X-Auth-Tag headers were accepted")
	}
	if _, err := extractAuthTagHeader([]string{strings.Repeat("x", maxAuthTagSize+1)}); err == nil {
		t.Fatal("oversized X-Auth-Tag header was accepted")
	}
}
