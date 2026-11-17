package buzzcompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fiatjaf.com/nostr"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

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
