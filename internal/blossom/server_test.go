package blossom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	upstream "fiatjaf.com/nostr/nipb7/blossom"
)

func TestUploadReturnsNIP94Tags(t *testing.T) {
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pixels.Bytes())
	hash := hex.EncodeToString(sum[:])
	descriptor := map[string]any{"url": "https://media.example/" + hash + ".png", "sha256": hash, "type": "image/png", "size": pixels.Len(), "uploaded": 100}
	loads := 0
	server := Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(descriptor)
	}), Options{ServiceURL: "https://media.example", LoadBlob: func(string) (io.ReadCloser, error) {
		loads++
		if loads == 1 {
			return nil, io.EOF
		}
		return io.NopCloser(bytes.NewReader(pixels.Bytes())), nil
	}})
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")
	authorization := nostr.Event{Kind: nostr.KindBlobs, CreatedAt: nostr.Now(), Content: "Upload image", Tags: nostr.Tags{{"t", "upload"}, {"x", hash}, {"expiration", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)}, {"server", "media.example"}}}
	if err := authorization.Sign(secret); err != nil {
		t.Fatal(err)
	}
	authJSON, _ := json.Marshal(authorization)
	request := httptest.NewRequest(http.MethodPut, "/upload", bytes.NewReader(pixels.Bytes()))
	request.Header.Set("Authorization", "Nostr "+base64.RawURLEncoding.EncodeToString(authJSON))
	request.Header.Set("X-SHA-256", hash)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status %d", response.Code)
	}
	var body struct {
		NIP94 [][]string `json:"nip94"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"url", descriptor["url"].(string)}, {"m", "image/png"}, {"x", hash}, {"size", strconv.Itoa(pixels.Len())}, {"dim", "2x3"}}
	if !reflect.DeepEqual(body.NIP94, want) {
		t.Fatalf("nip94 = %#v; want %#v", body.NIP94, want)
	}
}

func TestMaintainedUploadAcceptsZeroByteBlob(t *testing.T) {
	digest := blobDigest(nil)
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")
	auth := nostr.Event{Kind: nostr.KindBlobs, CreatedAt: nostr.Now(), Content: "Upload empty blob", Tags: nostr.Tags{
		{"t", "upload"}, {"x", digest}, {"expiration", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)}, {"server", "media.example"},
	}}
	if err := auth.Sign(secret); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(auth)
	header := "Nostr " + base64.RawURLEncoding.EncodeToString(encoded)
	stored := false
	server := Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), Options{
		ServiceURL: "https://media.example",
		CanUpload:  func(nostr.PubKey) bool { return true },
		LoadBlob: func(string) (io.ReadCloser, error) {
			if stored {
				return io.NopCloser(bytes.NewReader(nil)), nil
			}
			return nil, io.EOF
		},
		StoreUpload: func(_ context.Context, owner nostr.Event, body []byte, mediaType string) (upstream.BlobDescriptor, error) {
			if owner.ID != auth.ID || len(body) != 0 || mediaType != "application/octet-stream" {
				t.Fatalf("unexpected zero-byte upload input")
			}
			stored = true
			return upstream.BlobDescriptor{
				URL: "https://media.example/" + digest + ".bin", SHA256: digest,
				Size: 0, Type: mediaType, Uploaded: nostr.Now(),
			}, nil
		},
	})
	makeUpload := func() *http.Request {
		request := httptest.NewRequest(http.MethodPut, "/upload", bytes.NewReader(nil))
		request.Header.Set("Authorization", header)
		request.Header.Set("X-SHA-256", digest)
		return request
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, makeUpload())
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"size":0`) {
		t.Fatalf("zero-byte upload status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, makeUpload())
	if response.Code != http.StatusOK {
		t.Fatalf("duplicate zero-byte upload status=%d", response.Code)
	}
	preflight := httptest.NewRequest(http.MethodHead, "/upload", nil)
	preflight.Header.Set("Authorization", header)
	preflight.Header.Set("X-SHA-256", digest)
	preflight.Header.Set("X-Content-Length", "0")
	preflight.Header.Set("X-Content-Type", "application/octet-stream")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, preflight)
	if response.Code != http.StatusOK {
		t.Fatalf("zero-byte preflight status=%d", response.Code)
	}
}

func TestMirrorBoundsFetchAndReturnsVerifiedDescriptor(t *testing.T) {
	payload := []byte("mirrored bytes")
	digest := blobDigest(payload)
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")
	auth := nostr.Event{Kind: nostr.KindBlobs, CreatedAt: nostr.Now(), Content: "Mirror file", Tags: nostr.Tags{
		{"t", "upload"}, {"x", digest}, {"expiration", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)}, {"server", "media.example"},
	}}
	if err := auth.Sign(secret); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(auth)
	stored := false
	uploadRecorded := false
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://source.example/file.bin" {
			t.Fatalf("unexpected mirror URL %s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":   []string{"application/octet-stream"},
				"Content-Length": []string{strconv.Itoa(len(payload))},
			},
			ContentLength: int64(len(payload)), Body: io.NopCloser(bytes.NewReader(payload)),
		}, nil
	})}
	server := Wrap(http.NotFoundHandler(), Options{
		ServiceURL: "https://media.example", MirrorClient: client,
		LoadBlob: func(string) (io.ReadCloser, error) {
			if !stored {
				return nil, io.EOF
			}
			return io.NopCloser(bytes.NewReader(payload)), nil
		},
		StoreMirror: func(_ context.Context, owner nostr.Event, body []byte, mediaType string) (upstream.BlobDescriptor, error) {
			if owner.ID != auth.ID || !bytes.Equal(body, payload) || mediaType != "application/octet-stream" {
				t.Fatalf("unexpected mirror store input")
			}
			stored = true
			return upstream.BlobDescriptor{
				URL: "https://media.example/" + digest + ".bin", SHA256: digest,
				Size: len(body), Type: mediaType, Uploaded: nostr.Now(),
			}, nil
		},
		OnUpload: func(owner nostr.Event, descriptor upstream.BlobDescriptor) error {
			uploadRecorded = owner.ID == auth.ID && descriptor.SHA256 == digest
			return nil
		},
	})
	makeRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodPut, "/mirror", strings.NewReader(`{"url":"https://source.example/file.bin"}`))
		request.Header.Set("Authorization", "Nostr "+base64.RawURLEncoding.EncodeToString(encoded))
		return request
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, makeRequest())
	if response.Code != http.StatusCreated || !stored || !uploadRecorded {
		t.Fatalf("mirror result status=%d stored=%t tagged=%t body=%s", response.Code, stored, uploadRecorded, response.Body.String())
	}
	var descriptor enrichedDescriptor
	if err := json.Unmarshal(response.Body.Bytes(), &descriptor); err != nil {
		t.Fatal(err)
	}
	if len(descriptor.NIP94) < 4 || descriptor.SHA256 != digest || !reflect.DeepEqual(descriptor.NIP94[:4], [][]string{
		{"url", descriptor.URL}, {"m", "application/octet-stream"}, {"x", digest}, {"size", strconv.Itoa(len(payload))},
	}) {
		t.Fatalf("unexpected mirror descriptor %#v", descriptor)
	}

	response = httptest.NewRecorder()
	server.ServeHTTP(response, makeRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("duplicate mirror status=%d", response.Code)
	}
}

func TestMirrorRejectsUpstreamFailureAndHashMismatch(t *testing.T) {
	payload := []byte("remote bytes")
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")
	auth := nostr.Event{Kind: nostr.KindBlobs, CreatedAt: nostr.Now(), Content: "Mirror file", Tags: nostr.Tags{
		{"t", "upload"}, {"x", strings.Repeat("a", 64)}, {"expiration", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)},
	}}
	if err := auth.Sign(secret); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(auth)
	status := http.StatusOK
	contentLength := int64(len(payload))
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, ContentLength: contentLength, Body: io.NopCloser(bytes.NewReader(payload))}, nil
	})}
	server := Wrap(http.NotFoundHandler(), Options{ServiceURL: "https://media.example", MirrorClient: client})
	makeRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodPut, "/mirror", strings.NewReader(`{"url":"https://source.example/file"}`))
		request.Header.Set("Authorization", "Nostr "+base64.RawURLEncoding.EncodeToString(encoded))
		return request
	}
	status = http.StatusNotFound
	response := httptest.NewRecorder()
	server.ServeHTTP(response, makeRequest())
	if response.Code != http.StatusBadGateway {
		t.Fatalf("upstream failure status=%d", response.Code)
	}
	status = http.StatusOK
	contentLength = maxUploadBytes + 1
	response = httptest.NewRecorder()
	server.ServeHTTP(response, makeRequest())
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized mirror status=%d", response.Code)
	}
	contentLength = int64(len(payload))
	response = httptest.NewRecorder()
	server.ServeHTTP(response, makeRequest())
	if response.Code != http.StatusConflict {
		t.Fatalf("hash mismatch status=%d", response.Code)
	}
	if _, err := decodeMirrorRequest(strings.NewReader(`{"url":"https://source.example","extra":true}`)); err == nil {
		t.Fatal("unknown mirror request field was accepted")
	}
}

func TestBlobReadMethodsExposeConsistentCacheHeaders(t *testing.T) {
	hash := strings.Repeat("a", 64)
	server := Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), Options{
		CanRead: func(*nostr.PubKey) bool { return true },
		LoadBlob: func(string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("blob")), nil
		},
		LoadDescriptor: func(context.Context, string) (*upstream.BlobDescriptor, error) {
			return &upstream.BlobDescriptor{Type: "application/octet-stream", Size: 4}, nil
		},
	})
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(method, "/"+hash+".bin", nil))
		if response.Code != http.StatusOK || response.Header().Get("ETag") != hash || response.Header().Get("Accept-Ranges") != "bytes" || response.Header().Get("Cache-Control") == "" || response.Header().Get("Content-Length") != "4" || response.Header().Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("%s headers=%v status=%d", method, response.Header(), response.Code)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestReportValidatesAndPersistsSignedEvent(t *testing.T) {
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")
	event := nostr.Event{Kind: nostr.KindReporting, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"x", strings.Repeat("b", 64), "spam"}}, Content: "report"}
	if err := event.Sign(secret); err != nil {
		t.Fatal(err)
	}
	var saved []nostr.Event
	server := Wrap(http.NotFoundHandler(), Options{SaveReport: func(value nostr.Event) error { saved = append(saved, value); return nil }})
	requestBody, _ := json.Marshal(event)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/report", bytes.NewReader(requestBody)))
	if response.Code != http.StatusNoContent || len(saved) != 1 || saved[0].ID != event.ID {
		t.Fatalf("status=%d saved=%v", response.Code, saved)
	}
	event.Tags[0][1] = "bad"
	requestBody, _ = json.Marshal(event)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/report", bytes.NewReader(requestBody)))
	if response.Code != http.StatusBadRequest || len(saved) != 1 {
		t.Fatalf("tampered event status=%d saved=%d", response.Code, len(saved))
	}
}

func TestMediaPreflightAndStaticImageProcessing(t *testing.T) {
	var source bytes.Buffer
	if err := png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	original := source.Bytes()
	sum := sha256.Sum256(original)
	hash := hex.EncodeToString(sum[:])
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")
	auth := nostr.Event{Kind: nostr.KindBlobs, CreatedAt: nostr.Now(), Content: "Optimize image", Tags: nostr.Tags{
		{"t", "media"}, {"x", hash}, {"expiration", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)}, {"server", "media.example"},
	}}
	if err := auth.Sign(secret); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(auth)
	header := "Nostr " + base64.RawURLEncoding.EncodeToString(encoded)
	var stored []byte
	server := Wrap(http.NotFoundHandler(), Options{
		ServiceURL: "https://media.example",
		StoreMedia: func(_ context.Context, owner nostr.Event, data []byte, mediaType string) (upstream.BlobDescriptor, error) {
			if owner.ID != auth.ID || mediaType != "image/png" {
				t.Fatalf("unexpected media owner or type: %s %s", owner.ID, mediaType)
			}
			stored = append([]byte(nil), data...)
			processedHash := sha256.Sum256(data)
			return upstream.BlobDescriptor{
				URL:    "https://media.example/" + hex.EncodeToString(processedHash[:]) + ".png",
				SHA256: hex.EncodeToString(processedHash[:]), Size: len(data), Type: mediaType, Uploaded: nostr.Now(),
			}, nil
		},
		LoadBlob: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(stored)), nil },
	})
	preflight := httptest.NewRequest(http.MethodHead, "/media", nil)
	preflight.Header.Set("Authorization", header)
	preflight.Header.Set("X-SHA-256", hash)
	preflight.Header.Set("X-Content-Length", strconv.Itoa(len(original)))
	preflight.Header.Set("X-Content-Type", "image/png")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, preflight)
	if response.Code != http.StatusOK {
		t.Fatalf("preflight status=%d body=%s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(http.MethodPut, "/media", bytes.NewReader(original))
	request.Header.Set("Authorization", header)
	request.Header.Set("X-SHA-256", hash)
	request.Header.Set("Content-Type", "image/png")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	var descriptor enrichedDescriptor
	if err := json.Unmarshal(response.Body.Bytes(), &descriptor); err != nil {
		t.Fatal(err)
	}
	if len(stored) == 0 || descriptor.SHA256 != descriptor.NIP94[2][1] || !reflect.DeepEqual(descriptor.NIP94[len(descriptor.NIP94)-1], []string{"dim", "3x2"}) {
		t.Fatalf("invalid descriptor: %#v", descriptor)
	}
	badAuth := httptest.NewRequest(http.MethodPut, "/media", bytes.NewReader(original))
	badAuth.Header.Set("Authorization", strings.Replace(header, "Nostr ", "Bearer ", 1))
	badAuth.Header.Set("X-SHA-256", hash)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, badAuth)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid auth status=%d", response.Code)
	}
}

func TestReportQueueRequiresOperatorAuthorization(t *testing.T) {
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")
	auth := nostr.Event{Kind: nostr.KindBlobs, CreatedAt: nostr.Now(), Content: "Review blob reports", Tags: nostr.Tags{
		{"t", "list"}, {"expiration", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)}, {"server", "media.example"},
	}}
	if err := auth.Sign(secret); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(auth)
	called := false
	allow := false
	server := Wrap(http.NotFoundHandler(), Options{
		ServiceURL:       "https://media.example",
		CanReviewReports: func(pubkey nostr.PubKey) bool { return allow && pubkey == auth.PubKey },
		ListReports: func(_ *http.Request, limit int, cursor string) ([]ReportedBlob, error) {
			called = true
			if limit != 1 || cursor != "" {
				t.Fatalf("unexpected query: limit=%d cursor=%s", limit, cursor)
			}
			return []ReportedBlob{{Status: "open"}}, nil
		},
	})
	request := httptest.NewRequest(http.MethodGet, "/api/assets/reports?limit=1", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || called {
		t.Fatalf("anonymous request: status=%d called=%t", response.Code, called)
	}
	request.Header.Set("Authorization", "Nostr "+base64.RawURLEncoding.EncodeToString(encoded))
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || called {
		t.Fatalf("non-admin request: status=%d called=%t", response.Code, called)
	}
	allow = true
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !called || !strings.Contains(response.Body.String(), `"status":"open"`) {
		t.Fatalf("admin request: status=%d called=%t body=%s", response.Code, called, response.Body.String())
	}
}
