package zooid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	upstream "fiatjaf.com/nostr/nipb7/blossom"
)

func registryAssetFixture(t *testing.T) (*BlossomStore, nostr.SecretKey, nostr.SecretKey, nostr.Event, nostr.Event, string) {
	t.Helper()
	events := createTestEventStore()
	if err := events.Init(); err != nil {
		t.Fatal(err)
	}
	events.Config.Blossom.PublicURL = "https://media.example"
	events.Config.Registry.Enabled = true
	events.Config.Registry.ReleaseGroupID = "plugin-releases"
	events.Config.Registry.CuratorRole = "registry-curator"
	store := &BlossomStore{Config: events.Config, Events: events, Schema: events.Schema, MediaDir: t.TempDir()}
	publisher := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")
	curator := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "2")
	artworkHash := strings.Repeat("a", 64)
	artifactHash := strings.Repeat("b", 64)
	image := map[string]any{
		"url": "/registry/assets/demo/" + artworkHash + ".webp", "sourceUrl": "https://assets.example/" + artworkHash + ".webp",
		"sha256": artworkHash, "size": 42, "mediaType": "image/webp", "width": 1200, "height": 800,
	}
	variants := map[string]any{"preview": image, "full": image, "light": image, "dark": image}
	manifest := map[string]any{
		"schema": "lapis.registry.nip29-release/2",
		"plugin": map[string]any{
			"schema": "lapis.registry.plugin/1", "pluginId": "demo", "name": "Demo", "description": "Demo plugin",
			"channel": "official", "status": "active",
			"index": map[string]any{"appearance": map[string]any{"logo": image}},
			"detail": map[string]any{
				"appearance": map[string]any{"logo": image},
				"media": map[string]any{
					"banner":   map[string]any{"images": variants},
					"overview": map[string]any{"images": variants},
				},
				"gallery": []any{map[string]any{"id": "first", "images": variants}},
			},
		},
		"release": map[string]any{
			"schema": "lapis.registry.release/2", "pluginId": "demo", "version": "1.0.0", "releaseSequence": "1",
			"minAppVersion": "0.1.0", "platforms": []string{"web"}, "releasedAt": "2027-01-15T08:00:00Z",
			"artifact": map[string]any{"url": "https://media.example/" + artifactHash + ".bin", "sha256": artifactHash, "size": 99},
			"metadata": map[string]any{},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(manifestBytes)
	manifestHash := hex.EncodeToString(digest[:])
	if err := writeBlobAtomic(store.MediaDir, manifestHash, manifestBytes); err != nil {
		t.Fatal(err)
	}
	for index, hash := range []string{manifestHash, artifactHash, artworkHash} {
		if err := store.saveUploadTags(nostr.Event{PubKey: publisher.Public()}, upstream.BlobDescriptor{
			SHA256: hash, Type: "application/octet-stream", Size: 42, Uploaded: nostr.Timestamp(100 + index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	release := nostr.Event{
		Kind: nostr.KindSimpleGroupChatMessage, CreatedAt: 200, Content: "Demo 1.0.0",
		Tags: nostr.Tags{
			{"h", "plugin-releases"}, {"t", registryReleaseTopic}, {"t", "plugin:demo"},
			{"imeta", "url https://media.example/" + manifestHash + ".json", "m application/json", "x " + manifestHash, fmt.Sprintf("size %d", len(manifestBytes))},
		},
	}
	if err := release.Sign(publisher); err != nil {
		t.Fatal(err)
	}
	if err := events.SaveEvent(release); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Unix(300, 0).UTC()
	headContent := registryHeadContent{
		Schema: "lapis.registry.plugin-head/1", PluginID: "demo", AuthorityEpoch: "epoch-1", HeadRevision: "1",
		ActiveReleaseEventID: release.ID.Hex(),
		Releases: []registryHeadRelease{{
			ReleaseEventID: release.ID.Hex(), ManifestSHA256: manifestHash, Version: "1.0.0",
			ReleaseSequence: "1", State: "approved", DecisionEventID: strings.Repeat("c", 64),
		}},
		IssuedAt: createdAt.Format(time.RFC3339), ValidUntil: createdAt.Add(24 * time.Hour).Format(time.RFC3339),
	}
	content, _ := json.Marshal(headContent)
	head := nostr.Event{
		Kind: nostr.KindSimpleGroupChatMessage, CreatedAt: nostr.Timestamp(createdAt.Unix()), Content: string(content),
		Tags: nostr.Tags{{"h", "plugin-releases"}, {"t", registryHeadTopic}, {"t", "plugin:demo"}},
	}
	if err := head.Sign(curator); err != nil {
		t.Fatal(err)
	}
	if err := events.SaveEvent(head); err != nil {
		t.Fatal(err)
	}
	return store, publisher, curator, release, head, artworkHash
}

func TestRegistryHeadRetainsManifestBundleAndArtwork(t *testing.T) {
	store, publisher, curator, _, head, artworkHash := registryAssetFixture(t)
	if err := store.validateRegistryHeadAssets(context.Background(), head); err != nil {
		t.Fatal(err)
	}
	if err := store.projectRegistryReferences(context.Background(), head); err != nil {
		t.Fatal(err)
	}
	var references int
	if err := GetDb().QueryRow("SELECT COUNT(*) FROM " + store.Schema.Prefix("asset_references") + " WHERE reference_type='registry' AND reference_key='demo'").Scan(&references); err != nil {
		t.Fatal(err)
	}
	if references != 16 {
		t.Fatalf("registry reference count=%d, want 16", references)
	}
	allowed, reason, err := store.canDeleteAsset(context.Background(), nostr.Event{PubKey: publisher.Public()}, artworkHash)
	if err != nil || allowed || !strings.Contains(reason, "registry") {
		t.Fatalf("artwork delete: allowed=%t reason=%q err=%v", allowed, reason, err)
	}
	var first registryHeadContent
	if err := json.Unmarshal([]byte(head.Content), &first); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Unix(301, 0).UTC()
	first.HeadRevision = "2"
	first.PreviousHeadEventID = head.ID.Hex()
	first.ActiveReleaseEventID = ""
	first.IssuedAt = createdAt.Format(time.RFC3339)
	first.ValidUntil = createdAt.Add(24 * time.Hour).Format(time.RFC3339)
	content, _ := json.Marshal(first)
	cleared := nostr.Event{
		Kind: nostr.KindSimpleGroupChatMessage, CreatedAt: nostr.Timestamp(createdAt.Unix()), Content: string(content),
		Tags: nostr.Tags{{"h", "plugin-releases"}, {"t", registryHeadTopic}, {"t", "plugin:demo"}},
	}
	if err := cleared.Sign(curator); err != nil {
		t.Fatal(err)
	}
	if err := store.Events.SaveEvent(cleared); err != nil {
		t.Fatal(err)
	}
	if err := store.projectRegistryReferences(context.Background(), cleared); err != nil {
		t.Fatal(err)
	}
	allowed, _, err = store.canDeleteAsset(context.Background(), nostr.Event{PubKey: publisher.Public()}, artworkHash)
	if err != nil || !allowed {
		t.Fatalf("cleared artwork remained retained: allowed=%t err=%v", allowed, err)
	}
	if err := store.Events.DeleteEvent(cleared.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.rebuildRegistryReferences(context.Background(), cleared); err != nil {
		t.Fatal(err)
	}
	allowed, reason, err = store.canDeleteAsset(context.Background(), nostr.Event{PubKey: publisher.Public()}, artworkHash)
	if err != nil || allowed || !strings.Contains(reason, "registry") {
		t.Fatalf("restored artwork delete: allowed=%t reason=%q err=%v", allowed, reason, err)
	}
}

func TestRegistryReferenceBackfillIsIdempotent(t *testing.T) {
	store, _, _, _, head, _ := registryAssetFixture(t)
	if err := store.backfillRegistryReferences(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillRegistryReferences(context.Background()); err != nil {
		t.Fatal(err)
	}
	var additions int
	if err := GetDb().QueryRow("SELECT COUNT(*) FROM "+store.Schema.Prefix("asset_history")+" WHERE action=$1 AND reference_type='registry'", assetReferenceAdded).Scan(&additions); err != nil {
		t.Fatal(err)
	}
	if additions != 16 {
		t.Fatalf("registry additions=%d, want 16 for head %s", additions, head.ID.Hex())
	}
}

func TestRegistryManifestRejectsTrailingJSON(t *testing.T) {
	store, publisher, _, _, _, _ := registryAssetFixture(t)
	manifestBytes := []byte(`{}{}`)
	digest := sha256.Sum256(manifestBytes)
	manifestHash := hex.EncodeToString(digest[:])
	if err := writeBlobAtomic(store.MediaDir, manifestHash, manifestBytes); err != nil {
		t.Fatal(err)
	}
	if err := store.saveUploadTags(nostr.Event{PubKey: publisher.Public()}, upstream.BlobDescriptor{
		SHA256: manifestHash, Type: "application/json", Size: len(manifestBytes), Uploaded: 400,
	}); err != nil {
		t.Fatal(err)
	}
	release := nostr.Event{
		PubKey: publisher.Public(),
		Tags: nostr.Tags{{
			"imeta",
			"url https://media.example/" + manifestHash + ".json",
			"m application/json",
			"x " + manifestHash,
			fmt.Sprintf("size %d", len(manifestBytes)),
		}},
	}
	_, err := store.registryManifestAssets(context.Background(), release, "demo")
	if err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("registry manifest trailing JSON error=%v", err)
	}
}

func TestRegistryReferenceBackfillDropsInvalidActiveManifest(t *testing.T) {
	store, publisher, _, release, head, artworkHash := registryAssetFixture(t)
	if err := store.projectRegistryReferences(context.Background(), head); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.releaseManifestAttachment(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.MediaDir, attachment.Hash)); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillRegistryReferences(context.Background()); err != nil {
		t.Fatal(err)
	}
	allowed, _, err := store.canDeleteAsset(context.Background(), nostr.Event{PubKey: publisher.Public()}, artworkHash)
	if err != nil || !allowed {
		t.Fatalf("invalid head retained artwork: allowed=%t err=%v", allowed, err)
	}
}

func TestRegistryReferencesDoNotPinForeignBlobs(t *testing.T) {
	store, publisher, _, _, head, _ := registryAssetFixture(t)
	foreign := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "3")
	foreignHash := strings.Repeat("d", 64)
	if err := store.saveUploadTags(nostr.Event{PubKey: foreign.Public()}, upstream.BlobDescriptor{
		SHA256: foreignHash, Type: "image/webp", Size: 42, Uploaded: 500,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.reconcileRegistryReferences(context.Background(), head, "foreign-demo", publisher.Public().Hex(), map[string]string{
		"index-logo": foreignHash,
	}, 500); err != nil {
		t.Fatal(err)
	}
	var references int
	if err := GetDb().QueryRow("SELECT COUNT(*) FROM " + store.Schema.Prefix("asset_references") + " WHERE reference_type='registry' AND reference_key='foreign-demo'").Scan(&references); err != nil {
		t.Fatal(err)
	}
	if references != 0 {
		t.Fatalf("foreign registry references=%d, want 0", references)
	}
}
