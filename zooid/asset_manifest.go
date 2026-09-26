package zooid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
)

const maxRegistryManifestSize = 2 << 20

type registryManifestAttachment struct {
	Hash string
	Size int64
}

type registryImageReference struct {
	SHA256 string `json:"sha256"`
}

type registryImageVariants struct {
	Preview registryImageReference `json:"preview"`
	Full    registryImageReference `json:"full"`
	Light   registryImageReference `json:"light"`
	Dark    registryImageReference `json:"dark"`
}

type registryMediaItem struct {
	Images registryImageVariants `json:"images"`
}

type registryManifestDocument struct {
	Schema string `json:"schema"`
	Plugin struct {
		Schema   string `json:"schema"`
		PluginID string `json:"pluginId"`
		Index    struct {
			Appearance struct {
				Logo *registryImageReference `json:"logo"`
			} `json:"appearance"`
		} `json:"index"`
		Detail struct {
			Appearance struct {
				Logo *registryImageReference `json:"logo"`
			} `json:"appearance"`
			Media struct {
				Banner   *registryMediaItem `json:"banner"`
				Overview *registryMediaItem `json:"overview"`
			} `json:"media"`
			Gallery []struct {
				Images registryImageVariants `json:"images"`
			} `json:"gallery"`
		} `json:"detail"`
	} `json:"plugin"`
	Release struct {
		Schema   string `json:"schema"`
		PluginID string `json:"pluginId"`
		Artifact struct {
			SHA256 string `json:"sha256"`
		} `json:"artifact"`
	} `json:"release"`
}

func (store *BlossomStore) releaseManifestAttachment(event nostr.Event) (registryManifestAttachment, error) {
	var metadata nostr.Tag
	for _, tag := range event.Tags {
		if len(tag) > 0 && tag[0] == "imeta" {
			if metadata != nil {
				return registryManifestAttachment{}, errors.New("release must contain one manifest attachment")
			}
			metadata = tag
		}
	}
	if metadata == nil {
		return registryManifestAttachment{}, errors.New("release manifest attachment is missing")
	}
	values := make(map[string]string)
	for _, part := range metadata[1:] {
		key, value, found := strings.Cut(part, " ")
		if !found || key == "" || value == "" || values[key] != "" {
			return registryManifestAttachment{}, errors.New("release manifest attachment is invalid")
		}
		values[key] = value
	}
	hash := values["x"]
	size, err := strconv.ParseInt(values["size"], 10, 64)
	if !assetHash.MatchString(hash) || values["m"] != "application/json" || size < 1 || size > maxRegistryManifestSize || store.localAssetHash(values["url"]) != hash || err != nil {
		return registryManifestAttachment{}, errors.New("release manifest attachment is invalid")
	}
	return registryManifestAttachment{Hash: hash, Size: size}, nil
}

func (store *BlossomStore) registryManifestAssets(ctx context.Context, event nostr.Event, pluginID string) (map[string]string, error) {
	attachment, err := store.releaseManifestAttachment(event)
	if err != nil {
		return nil, err
	}
	owned, err := store.assetOwned(ctx, event.PubKey.Hex(), attachment.Hash)
	if err != nil || !owned {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("release publisher does not own its manifest")
	}
	file, err := os.Open(filepath.Join(store.mediaDir(), attachment.Hash))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	bytes, err := io.ReadAll(io.LimitReader(file, maxRegistryManifestSize+1))
	if err != nil || int64(len(bytes)) != attachment.Size {
		return nil, errors.New("release manifest size does not match its signed attachment")
	}
	digest := sha256.Sum256(bytes)
	if hex.EncodeToString(digest[:]) != attachment.Hash {
		return nil, errors.New("release manifest digest does not match its signed attachment")
	}
	var document registryManifestDocument
	decoder := json.NewDecoder(strings.NewReader(string(bytes)))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode release manifest: trailing data")
	}
	if document.Plugin.Schema != "lapis.registry.plugin/1" || document.Plugin.PluginID != pluginID || document.Release.PluginID != pluginID ||
		(document.Schema != "lapis.registry.nip29-release/1" && document.Schema != "lapis.registry.nip29-release/2") ||
		(document.Release.Schema != "lapis.registry.release/1" && document.Release.Schema != "lapis.registry.release/2") {
		return nil, errors.New("release manifest identity is invalid")
	}
	assets := map[string]string{"manifest": attachment.Hash, "artifact": document.Release.Artifact.SHA256}
	addRegistryImage(assets, "index-logo", document.Plugin.Index.Appearance.Logo)
	addRegistryImage(assets, "detail-logo", document.Plugin.Detail.Appearance.Logo)
	addRegistryVariants(assets, "banner", document.Plugin.Detail.Media.Banner)
	addRegistryVariants(assets, "overview", document.Plugin.Detail.Media.Overview)
	for index := range document.Plugin.Detail.Gallery {
		item := &registryMediaItem{Images: document.Plugin.Detail.Gallery[index].Images}
		addRegistryVariants(assets, fmt.Sprintf("gallery:%04d", index), item)
	}
	for slot, hash := range assets {
		if !assetHash.MatchString(hash) {
			delete(assets, slot)
		}
	}
	return assets, nil
}

func (store *BlossomStore) assetOwned(ctx context.Context, owner, hash string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var owned bool
	err := GetDb().QueryRowContext(ctx, fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE pubkey=$1 AND sha256=$2)", store.Schema.Prefix("blossom_blobs")), owner, hash).Scan(&owned)
	return owned, err
}

func addRegistryImage(assets map[string]string, slot string, image *registryImageReference) {
	if image != nil {
		assets[slot] = image.SHA256
	}
}

func addRegistryVariants(assets map[string]string, slot string, item *registryMediaItem) {
	if item == nil {
		return
	}
	assets[slot+":preview"] = item.Images.Preview.SHA256
	assets[slot+":full"] = item.Images.Full.SHA256
	assets[slot+":light"] = item.Images.Light.SHA256
	assets[slot+":dark"] = item.Images.Dark.SHA256
}
