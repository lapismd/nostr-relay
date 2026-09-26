package zooid

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	upstream "fiatjaf.com/nostr/nipb7/blossom"
	"github.com/spf13/afero"
	lapisblossom "zooid/internal/blossom"
	"zooid/internal/khatru/blossom"
)

type BlossomStore struct {
	Config *Config
	Events eventstore.Store
	Schema *Schema
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
	storeBytes := func(ctx context.Context, auth nostr.Event, data []byte, mediaType string) (upstream.BlobDescriptor, error) {
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		if err := writeBlobAtomic(dir, hash, data); err != nil {
			return upstream.BlobDescriptor{}, err
		}
		descriptor := upstream.BlobDescriptor{
			URL:    publicURL + "/" + hash + upstream.GetExtension(mediaType),
			SHA256: hash, Size: len(data), Type: mediaType, Uploaded: nostr.Now(),
		}
		if err := backend.Store.Keep(ctx, descriptor, auth.PubKey); err != nil {
			return upstream.BlobDescriptor{}, err
		}
		return descriptor, nil
	}
	baseRouter := instance.Relay.Router()
	outerRouter := http.NewServeMux()
	outerRouter.Handle("/", lapisblossom.Wrap(baseRouter, lapisblossom.Options{
		ServiceURL: publicURL,
		CanRead: func(pubkey *nostr.PubKey) bool {
			return allowBlobRead(instance.Config, pubkey != nil && instance.Management.IsMember(*pubkey))
		},
		CanList: func(pubkey nostr.PubKey) bool {
			return allowBlobWrite(instance.Config, instance.Management.IsMember(pubkey))
		},
		ListBlobs:  bl.listBlobs,
		OnUpload:   bl.saveUploadTags,
		OnDelete:   bl.removeUploadTags,
		StageIndex: stageBlobIndex,
		LoadBlob:   func(hash string) (io.ReadCloser, error) { return fs.Open(dir + "/" + hash) },
		LoadDescriptor: func(ctx context.Context, hash string) (*upstream.BlobDescriptor, error) {
			return backend.Store.Get(ctx, hash)
		},
		SaveReport: bl.saveReport,
		CanReviewReports: func(pubkey nostr.PubKey) bool {
			return instance.Config.CanManage(pubkey) || instance.Management.IsAdmin(pubkey)
		},
		ListReports: bl.listReports,
		CanMedia: func(pubkey nostr.PubKey) bool {
			return allowBlobWrite(instance.Config, instance.Management.IsMember(pubkey))
		},
		StoreMedia:  storeBytes,
		StoreMirror: storeBytes,
	}))
	instance.Relay.SetRouter(outerRouter)

	backend.Store = checkedBlobIndex{blossom.EventStoreBlobIndexWrapper{
		Store:      bl.Events,
		ServiceURL: publicURL,
	}}

	backend.StoreBlob = func(ctx context.Context, sha256 string, ext string, body []byte) error {
		if err := writeBlobAtomic(dir, sha256, body); err != nil {
			return err
		}
		if pending := pendingBlobIndex(ctx); pending != nil {
			if pending.descriptor.SHA256 != sha256 {
				return fmt.Errorf("staged descriptor hash does not match blob")
			}
			return checkedBlobIndex{blossom.EventStoreBlobIndexWrapper{Store: bl.Events, ServiceURL: publicURL}}.save(ctx, pending.descriptor, pending.owner)
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

func writeBlobAtomic(dir, hash string, body []byte) error {
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != hash {
		return fmt.Errorf("blob hash does not match content")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, hash)
	file, err := os.CreateTemp(dir, ".blossom-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := io.Copy(file, bytes.NewReader(body)); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
