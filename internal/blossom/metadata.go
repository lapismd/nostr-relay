package blossom

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nipb7/blossom"
)

type Options struct {
	ServiceURL       string
	CanRead          func(pubkey *nostr.PubKey) bool
	CanList          func(pubkey nostr.PubKey) bool
	LoadBlob         func(hash string) (io.ReadCloser, error)
	LoadDescriptor   func(ctx context.Context, hash string) (*blossom.BlobDescriptor, error)
	SaveReport       func(event nostr.Event) error
	CanReviewReports func(pubkey nostr.PubKey) bool
	ListReports      func(r *http.Request, limit int, cursor string) ([]ReportedBlob, error)
	CanViewHistory   func(actor, owner nostr.PubKey) bool
	ListHistory      func(r *http.Request, owner nostr.PubKey, limit int, cursor string) (AssetHistoryPage, error)
	ListBlobs        func(r *http.Request, owner nostr.PubKey, query ListQuery) ([]ListedBlob, error)
	OnUpload         func(auth nostr.Event, descriptor blossom.BlobDescriptor) error
	CanDelete        func(ctx context.Context, auth nostr.Event, hash string) (bool, string, error)
	OnDelete         func(auth nostr.Event, hash string) error
	StageIndex       func(r *http.Request) *http.Request
	CanUpload        func(pubkey nostr.PubKey) bool
	CanMedia         func(pubkey nostr.PubKey) bool
	StoreUpload      func(ctx context.Context, auth nostr.Event, data []byte, mediaType string) (blossom.BlobDescriptor, error)
	StoreMedia       func(ctx context.Context, auth nostr.Event, data []byte, mediaType string) (blossom.BlobDescriptor, error)
	StoreMirror      func(ctx context.Context, auth nostr.Event, data []byte, mediaType string) (blossom.BlobDescriptor, error)
	MirrorClient     *http.Client
}

type enrichedDescriptor struct {
	blossom.BlobDescriptor
	NIP94 [][]string `json:"nip94"`
}

func enrichDescriptor(next http.Handler, w http.ResponseWriter, r *http.Request, auth nostr.Event, options Options, uploadStatus int) {
	if options.StageIndex != nil {
		r = options.StageIndex(r)
	}
	recorded := httptest.NewRecorder()
	next.ServeHTTP(recorded, r)
	result := recorded.Result()
	defer result.Body.Close()
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		copyResponse(w, result)
		return
	}
	var descriptor blossom.BlobDescriptor
	if err := json.NewDecoder(result.Body).Decode(&descriptor); err != nil || !blobHash.MatchString(descriptor.SHA256) {
		copyResponse(w, recorded.Result())
		return
	}
	if descriptor.Type == "" {
		descriptor.Type = "application/octet-stream"
	}
	if strings.HasSuffix(descriptor.URL, descriptor.SHA256) {
		descriptor.URL += ".bin"
	}
	if options.OnUpload != nil {
		if err := options.OnUpload(auth, descriptor); err != nil {
			http.Error(w, "failed to save upload tags", http.StatusServiceUnavailable)
			return
		}
	}
	status := result.StatusCode
	if uploadStatus != 0 {
		status = uploadStatus
	}
	writeEnrichedDescriptor(w, status, result.Header, descriptor, options)
}

func writeEnrichedDescriptor(w http.ResponseWriter, status int, headers http.Header, descriptor blossom.BlobDescriptor, options Options) {
	writeEnrichedDescriptorWithOriginal(w, status, headers, descriptor, options, "")
}

func writeEnrichedDescriptorWithOriginal(w http.ResponseWriter, status int, headers http.Header, descriptor blossom.BlobDescriptor, options Options, originalHash string) {
	tags := [][]string{{"url", descriptor.URL}, {"m", descriptor.Type}, {"x", descriptor.SHA256}}
	if originalHash != "" {
		tags = append(tags, []string{"ox", originalHash})
	}
	tags = append(tags, []string{"size", fmt.Sprint(descriptor.Size)})
	if options.LoadBlob != nil && strings.HasPrefix(descriptor.Type, "image/") {
		if reader, err := options.LoadBlob(descriptor.SHA256); err == nil {
			if config, _, err := image.DecodeConfig(io.LimitReader(reader, 10<<20)); err == nil && config.Width > 0 && config.Height > 0 {
				tags = append(tags, []string{"dim", fmt.Sprintf("%dx%d", config.Width, config.Height)})
			}
			reader.Close()
		}
	}
	for key, values := range headers {
		w.Header()[key] = append([]string(nil), values...)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Del("Content-Length")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(enrichedDescriptor{descriptor, tags})
}

func copyResponse(w http.ResponseWriter, result *http.Response) {
	for key, values := range result.Header {
		w.Header()[key] = append([]string(nil), values...)
	}
	w.WriteHeader(result.StatusCode)
	_, _ = io.Copy(w, result.Body)
}
