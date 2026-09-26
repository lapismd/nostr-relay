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
