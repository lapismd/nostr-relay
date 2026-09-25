package buzzcompat

import (
	"context"
	"encoding/json"
	"fiatjaf.com/nostr"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
