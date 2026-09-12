package zooid

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"github.com/spf13/afero"
	"zooid/internal/khatru/blossom"
)

type BlossomStore struct {
	Config *Config
	Events eventstore.Store
}

func allowBlobRead(config *Config, isMember bool) bool {
	return config.Policy.Open || isMember
}

func allowBlobWrite(config *Config, isMember bool) bool {
	return config.Policy.Open || isMember
}

func blossomPublicURL(config *Config) string {
	if configured := strings.TrimRight(config.Blossom.PublicURL, "/"); configured != "" {
		return configured
	}
	return "https://" + config.Host
}

func (bl *BlossomStore) Enable(instance *Instance) {
	dir := Env("MEDIA")
	fs := afero.NewOsFs()
	publicURL := blossomPublicURL(bl.Config)
	backend := blossom.New(instance.Relay, publicURL)

	backend.Store = blossom.EventStoreBlobIndexWrapper{
		Store:      bl.Events,
		ServiceURL: publicURL,
	}

	backend.StoreBlob = func(ctx context.Context, sha256 string, ext string, body []byte) error {
		file, err := fs.Create(dir + "/" + sha256)
		if err != nil {
			return err
		}

		if _, err := io.Copy(file, bytes.NewReader(body)); err != nil {
			return err
		}

		return nil
	}

	backend.LoadBlob = func(ctx context.Context, sha256 string, ext string) (io.ReadSeeker, *url.URL, error) {
		file, err := fs.Open(dir + "/" + sha256)
		if err != nil {
			return nil, nil, err
		}
		return file, nil, nil
	}

	backend.DeleteBlob = func(ctx context.Context, sha256 string, ext string) error {
		return fs.Remove(dir + "/" + sha256)
	}

	backend.RejectUpload = func(ctx context.Context, auth *nostr.Event, size int, ext string) (bool, string, int) {
		if size > 10*1024*1024 {
			return true, "file too large", 413
		}

		if auth == nil || !allowBlobWrite(instance.Config, instance.Management.IsMember(auth.PubKey)) {
			return true, "unauthorized", 403
		}

		return false, ext, size
	}

	backend.RejectGet = func(ctx context.Context, auth *nostr.Event, sha256 string, ext string) (bool, string, int) {
		pubkey, authenticated := authenticatedPubkey(ctx)
		if auth != nil {
			pubkey = auth.PubKey
			authenticated = true
		}
		isMember := authenticated && instance.Management.IsMember(pubkey)
		if !allowBlobRead(instance.Config, isMember) {
			return true, "unauthorized", 403
		}

		return false, "", 200
	}

	backend.RejectList = func(ctx context.Context, auth *nostr.Event, pubkey nostr.PubKey) (bool, string, int) {
		if auth == nil || !allowBlobWrite(instance.Config, instance.Management.IsMember(auth.PubKey)) {
			return true, "unauthorized", 403
		}

		return false, "", 200
	}

	backend.RejectDelete = func(ctx context.Context, auth *nostr.Event, sha256 string, ext string) (bool, string, int) {
		if auth == nil || !allowBlobWrite(instance.Config, instance.Management.IsMember(auth.PubKey)) {
			return true, "unauthorized", 403
		}

		return false, "", 200
	}
}
