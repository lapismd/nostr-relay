package buzzcompat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

type testPubkeyKey struct{}

func TestQueryAuthenticatesAndDeduplicates(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	secret := nostr.Generate()
	event := signedEvent(t, secret, nostr.KindTextNote, "hello", now)
	queryCalls := 0
	authorizedAsSigner := false
	handler := New(Backend{
		WithAuthenticatedPubkey: func(ctx context.Context, pubkey nostr.PubKey) context.Context {
			return context.WithValue(ctx, testPubkeyKey{}, pubkey)
		},
		AuthorizeQuery: func(ctx context.Context, _ nostr.Filter) (bool, string) {
			got, ok := ctx.Value(testPubkeyKey{}).(nostr.PubKey)
			authorizedAsSigner = ok && got == secret.Public()
			return false, ""
		},
		Query: func(context.Context, nostr.Filter) iter.Seq[nostr.Event] {
			queryCalls++
			return func(yield func(nostr.Event) bool) { yield(event) }
		},
		NIP11: func(http.ResponseWriter, *http.Request) {},
	})
	handler.now = func() time.Time { return now }
	server := httptest.NewServer(handler.Wrap(http.NotFoundHandler()))
	defer server.Close()

	body := []byte(`[{},{}]`)
	request := authenticatedRequest(t, secret, http.MethodPost, server.URL+"/query", body, now)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("query returned %s", response.Status)
	}
	var events []nostr.Event
	if err := json.NewDecoder(response.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if !authorizedAsSigner || queryCalls != 2 || len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("authorized=%v, got %d query calls and events %#v", authorizedAsSigner, queryCalls, events)
	}
}

func TestQueryUsesBeforeIDAndReturnsDeterministicOrder(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	secret := nostr.Generate()
	first := signedEvent(t, secret, nostr.KindTextNote, "first", now)
	second := signedEvent(t, secret, nostr.KindTextNote, "second", now)
	expected := []nostr.Event{first, second}
	if expected[0].ID.Hex() > expected[1].ID.Hex() {
		expected[0], expected[1] = expected[1], expected[0]
	}
	usedBefore := false
	handler := New(Backend{
		WithAuthenticatedPubkey: func(ctx context.Context, _ nostr.PubKey) context.Context { return ctx },
		AuthorizeQuery:          func(context.Context, nostr.Filter) (bool, string) { return false, "" },
		Query:                   func(context.Context, nostr.Filter) iter.Seq[nostr.Event] { return func(func(nostr.Event) bool) {} },
		QueryBefore: func(_ context.Context, filter nostr.Filter, beforeID string) iter.Seq[nostr.Event] {
			usedBefore = filter.Until == nostr.Timestamp(now.Unix()) && filter.Limit == 2 && beforeID == first.ID.Hex()
			return func(yield func(nostr.Event) bool) {
				yield(second)
				yield(first)
			}
		},
		NIP11: func(http.ResponseWriter, *http.Request) {},
	})
	handler.now = func() time.Time { return now }
	server := httptest.NewServer(handler.Wrap(http.NotFoundHandler()))
	defer server.Close()

	body := []byte(fmt.Sprintf(`[{"until":%d,"before_id":%q,"limit":2}]`, now.Unix(), first.ID.Hex()))
	response, err := http.DefaultClient.Do(authenticatedRequest(t, secret, http.MethodPost, server.URL+"/query", body, now))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var events []nostr.Event
	if err := json.NewDecoder(response.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !usedBefore || len(events) != 2 || events[0].ID != expected[0].ID || events[1].ID != expected[1].ID {
		t.Fatalf("status=%s usedBefore=%v events=%#v", response.Status, usedBefore, events)
	}
}

func TestQuerySynthesizesPresenceOnlyWhenEveryFilterQualifies(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	secret := nostr.Generate()
	subjects := []nostr.PubKey{nostr.Generate().Public(), nostr.Generate().Public()}
	presence := signedEvent(t, nostr.Generate(), 20001, "online", now)
	presence.Tags = nostr.Tags{{"p", subjects[0].Hex()}}
	if err := presence.Sign(nostr.Generate()); err != nil {
		t.Fatal(err)
	}
	queryCalls := 0
	presenceCalls := 0
	handler := New(Backend{
		WithAuthenticatedPubkey: func(ctx context.Context, _ nostr.PubKey) context.Context { return ctx },
		AuthorizeQuery:          func(context.Context, nostr.Filter) (bool, string) { return false, "" },
		Query: func(context.Context, nostr.Filter) iter.Seq[nostr.Event] {
			queryCalls++
			return func(func(nostr.Event) bool) {}
		},
		EphemeralQuery: func(_ context.Context, filters []nostr.Filter) ([]nostr.Event, error) {
			presenceCalls++
			if len(filters) != 2 || filters[0].Kinds[0] != 40902 || filters[1].Kinds[0] != 20001 {
				t.Fatalf("unexpected presence filters: %#v", filters)
			}
			return []nostr.Event{presence}, nil
		},
	})
	handler.now = func() time.Time { return now }
	server := httptest.NewServer(handler.Wrap(http.NotFoundHandler()))
	defer server.Close()

	body := []byte(fmt.Sprintf(`[{"kinds":[40902],"authors":[%q]},{"kinds":[20001],"authors":[%q]}]`, subjects[0].Hex(), subjects[1].Hex()))
	response, err := http.DefaultClient.Do(authenticatedRequest(t, secret, http.MethodPost, server.URL+"/query", body, now))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var events []nostr.Event
	if err := json.NewDecoder(response.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || presenceCalls != 1 || queryCalls != 0 || len(events) != 1 {
		t.Fatalf("status=%s presenceCalls=%d queryCalls=%d events=%#v", response.Status, presenceCalls, queryCalls, events)
	}
}

func TestQueryDoesNotSynthesizeMixedPresenceFilters(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	secret := nostr.Generate()
	subject := nostr.Generate().Public()
	queryCalls := 0
	presenceCalls := 0
	handler := New(Backend{
		WithAuthenticatedPubkey: func(ctx context.Context, _ nostr.PubKey) context.Context { return ctx },
		AuthorizeQuery:          func(context.Context, nostr.Filter) (bool, string) { return false, "" },
		Query: func(context.Context, nostr.Filter) iter.Seq[nostr.Event] {
			queryCalls++
			return func(func(nostr.Event) bool) {}
		},
		EphemeralQuery: func(context.Context, []nostr.Filter) ([]nostr.Event, error) {
			presenceCalls++
			return nil, nil
		},
	})
	handler.now = func() time.Time { return now }
	server := httptest.NewServer(handler.Wrap(http.NotFoundHandler()))
	defer server.Close()

	body := []byte(fmt.Sprintf(`[{"kinds":[40902],"authors":[%q]},{"kinds":[1]}]`, subject.Hex()))
	response, err := http.DefaultClient.Do(authenticatedRequest(t, secret, http.MethodPost, server.URL+"/query", body, now))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || presenceCalls != 0 || queryCalls != 2 {
		t.Fatalf("status=%s presenceCalls=%d queryCalls=%d", response.Status, presenceCalls, queryCalls)
	}
}

func TestQueryRejectsInvalidBeforeID(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	secret := nostr.Generate()
	handler := New(Backend{WithAuthenticatedPubkey: func(ctx context.Context, _ nostr.PubKey) context.Context { return ctx }})
	handler.now = func() time.Time { return now }
	server := httptest.NewServer(handler.Wrap(http.NotFoundHandler()))
	defer server.Close()

	for _, body := range [][]byte{
		[]byte(`[{"before_id":"invalid"}]`),
		[]byte(`[{"until":123,"before_id":"invalid"}]`),
	} {
		response, err := http.DefaultClient.Do(authenticatedRequest(t, secret, http.MethodPost, server.URL+"/query", body, now))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s returned %s", body, response.Status)
		}
	}
}

func TestCountDelegatesMultiFilterUnionAndSerializesApproximation(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	secret := nostr.Generate()
	called := false
	handler := New(Backend{
		WithAuthenticatedPubkey: func(ctx context.Context, _ nostr.PubKey) context.Context { return ctx },
		AuthorizeQuery:          func(context.Context, nostr.Filter) (bool, string) { return false, "" },
		CountMany: func(_ context.Context, filters []nostr.Filter) (CountResult, error) {
			called = len(filters) == 2 && filters[0].Limit == 1 && len(filters[1].Kinds) == 1
			return CountResult{Count: 80_000, Approximate: true}, nil
		},
		NIP11: func(http.ResponseWriter, *http.Request) {},
	})
	handler.now = func() time.Time { return now }
	server := httptest.NewServer(handler.Wrap(http.NotFoundHandler()))
	defer server.Close()

	body := []byte(`[{"limit":1},{"kinds":[1]}]`)
	response, err := http.DefaultClient.Do(authenticatedRequest(t, secret, http.MethodPost, server.URL+"/count", body, now))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result CountResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !called || result.Count != 80_000 || !result.Approximate {
		t.Fatalf("status=%s called=%v result=%#v", response.Status, called, result)
	}
}

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

func TestInfoAndMediaAliasDelegate(t *testing.T) {
	var delegatedMethod, delegatedPath string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delegatedMethod = r.Method
		delegatedPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	handler := New(Backend{NIP11: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/nostr+json")
		_, _ = w.Write([]byte(`{"supported_nips":[29,43]}`))
	}})
	server := httptest.NewServer(handler.Wrap(next))
	defer server.Close()

	info, err := http.Get(server.URL + "/info")
	if err != nil {
		t.Fatal(err)
	}
	info.Body.Close()
	if info.StatusCode != http.StatusOK || info.Header.Get("Content-Type") != "application/nostr+json" {
		t.Fatalf("unexpected info response: %s %q", info.Status, info.Header.Get("Content-Type"))
	}

	media, err := http.Get(server.URL + "/media/abc.jpg")
	if err != nil {
		t.Fatal(err)
	}
	media.Body.Close()
	if media.StatusCode != http.StatusNoContent || delegatedPath != "/abc.jpg" {
		t.Fatalf("media alias returned %s and path %q", media.Status, delegatedPath)
	}

	uploadRequest, err := http.NewRequest(http.MethodPut, server.URL+"/media/upload", bytes.NewReader([]byte("blob")))
	if err != nil {
		t.Fatal(err)
	}
	upload, err := http.DefaultClient.Do(uploadRequest)
	if err != nil {
		t.Fatal(err)
	}
	upload.Body.Close()
	if upload.StatusCode != http.StatusNoContent || delegatedMethod != http.MethodPut || delegatedPath != "/upload" {
		t.Fatalf("upload alias returned %s and delegated %s %q", upload.Status, delegatedMethod, delegatedPath)
	}
}

func TestBlossomUploadNormalizesBuzzAuthorization(t *testing.T) {
	secret := nostr.Generate()
	hash := strings.Repeat("a", 64)
	event, raw, authorization := blossomAuthorization(t, secret, "upload", nostr.Tags{{"x", hash}})
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/upload" {
			t.Errorf("delegated path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Nostr "+base64.StdEncoding.EncodeToString(raw) {
			t.Errorf("authorization was not normalized: %q", got)
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Authorization"), "Nostr "))
		if err != nil {
			t.Errorf("normalized token is not standard Base64: %v", err)
		}
		var got nostr.Event
		if err := json.Unmarshal(decoded, &got); err != nil || got.ID != event.ID {
			t.Errorf("normalized event mismatch: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := New(Backend{})
	server := httptest.NewServer(handler.Wrap(next))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPut, server.URL+"/upload", bytes.NewReader([]byte("image")))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("X-SHA-256", hash)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || !called {
		t.Fatalf("status=%s called=%v", response.Status, called)
	}
}

func TestBlossomReadAcceptsBuzzAuthorityScope(t *testing.T) {
	secret := nostr.Generate()
	hash := strings.Repeat("b", 64)
	var authorization string
	called := false
	handler := New(Backend{
		WithAuthenticatedPubkey: func(ctx context.Context, pubkey nostr.PubKey) context.Context {
			return context.WithValue(ctx, testPubkeyKey{}, pubkey)
		},
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/"+hash+".png" || r.Header.Get("Authorization") != "" {
			t.Errorf("delegated path/header = %q/%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if pubkey, ok := r.Context().Value(testPubkeyKey{}).(nostr.PubKey); !ok || pubkey != secret.Public() {
			t.Error("Buzz Blossom signer was not propagated")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(handler.Wrap(next))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _, authorization = blossomAuthorization(t, secret, "get", nostr.Tags{{"server", serverURL.Host}})

	request, err := http.NewRequest(http.MethodGet, server.URL+"/media/"+hash+".png", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", authorization)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || !called {
		t.Fatalf("status=%s called=%v", response.Status, called)
	}
}

func TestBlossomReadRejectsWrongServerScope(t *testing.T) {
	secret := nostr.Generate()
	hash := strings.Repeat("c", 64)
	_, _, authorization := blossomAuthorization(t, secret, "get", nostr.Tags{{"server", "other.example"}})
	called := false
	handler := New(Backend{WithAuthenticatedPubkey: func(ctx context.Context, _ nostr.PubKey) context.Context { return ctx }})
	server := httptest.NewServer(handler.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })))
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/"+hash+".png", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", authorization)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden || called {
		t.Fatalf("status=%s called=%v", response.Status, called)
	}
}

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
