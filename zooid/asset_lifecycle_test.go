package zooid

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	upstream "fiatjaf.com/nostr/nipb7/blossom"
)

func signedProfile(t *testing.T, secret nostr.SecretKey, created nostr.Timestamp, content string) nostr.Event {
	t.Helper()
	event := nostr.Event{Kind: nostr.KindProfileMetadata, CreatedAt: created, Content: content}
	if err := event.Sign(secret); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestProfileReferencesProtectOwnedAssetsAndRecordHistory(t *testing.T) {
	events := createTestEventStore()
	if err := events.Init(); err != nil {
		t.Fatal(err)
	}
	events.Config.Blossom.PublicURL = "https://media.example/assets"
	store := &BlossomStore{Config: events.Config, Events: events, Schema: events.Schema}
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")
	owner := secret.Public()
	firstHash := strings.Repeat("a", 64)
	secondHash := strings.Repeat("b", 64)
	for index, hash := range []string{firstHash, secondHash} {
		if err := store.saveUploadTags(nostr.Event{PubKey: owner}, upstream.BlobDescriptor{
			SHA256: hash, Type: "image/png", Size: 42, Uploaded: nostr.Timestamp(100 + index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	first := signedProfile(t, secret, 1000, `{"picture":"https://media.example/assets/`+firstHash+`.png"}`)
	if _, err := events.ReplaceEvent(first); err != nil {
		t.Fatal(err)
	}
	if err := store.projectProfileReferences(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	allowed, reason, err := store.canDeleteAsset(context.Background(), nostr.Event{PubKey: owner}, firstHash)
	if err != nil || allowed || !strings.Contains(reason, "profile picture") {
		t.Fatalf("referenced delete: allowed=%t reason=%q err=%v", allowed, reason, err)
	}
	second := signedProfile(t, secret, 1001, `{"picture":"blossom:`+secondHash+`.png?xs=media.example"}`)
	if _, err := events.ReplaceEvent(second); err != nil {
		t.Fatal(err)
	}
	if err := store.projectProfileReferences(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := store.projectProfileReferences(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	allowed, _, err = store.canDeleteAsset(context.Background(), nostr.Event{PubKey: owner}, firstHash)
	if err != nil || !allowed {
		t.Fatalf("replaced asset remained retained: allowed=%t err=%v", allowed, err)
	}
	allowed, _, err = store.canDeleteAsset(context.Background(), nostr.Event{PubKey: owner}, secondHash)
	if err != nil || allowed {
		t.Fatalf("current asset was not retained: allowed=%t err=%v", allowed, err)
	}
	otherSecret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "2")
	other := signedProfile(t, otherSecret, 1002, `{"picture":"https://media.example/assets/`+secondHash+`.png"}`)
	if _, err := events.ReplaceEvent(other); err != nil {
		t.Fatal(err)
	}
	if err := store.projectProfileReferences(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	var otherReferences int
	if err := GetDb().QueryRow("SELECT COUNT(*) FROM "+events.Schema.Prefix("asset_references")+" WHERE owner_pubkey=$1", other.PubKey.Hex()).Scan(&otherReferences); err != nil || otherReferences != 0 {
		t.Fatalf("non-owner retained asset: count=%d err=%v", otherReferences, err)
	}
	empty := signedProfile(t, secret, 1003, `{}`)
	if _, err := events.ReplaceEvent(empty); err != nil {
		t.Fatal(err)
	}
	if err := store.projectProfileReferences(context.Background(), empty); err != nil {
		t.Fatal(err)
	}
	allowed, _, err = store.canDeleteAsset(context.Background(), nostr.Event{PubKey: owner}, secondHash)
	if err != nil || !allowed {
		t.Fatalf("removed asset remained retained: allowed=%t err=%v", allowed, err)
	}
	if err := store.removeUploadTags(nostr.Event{PubKey: owner}, firstHash); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/assets/history", nil)
	page, err := store.listAssetHistory(request, owner, 2, "")
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("first history page: %#v err=%v", page, err)
	}
	next, err := store.listAssetHistory(request, owner, 20, page.NextCursor)
	if err != nil || len(next.Items) < 5 {
		t.Fatalf("second history page: %#v err=%v", next, err)
	}
	actions := make(map[string]int)
	for _, item := range append(page.Items, next.Items...) {
		actions[item.Action]++
	}
	for _, action := range []string{assetUploaded, assetReferenceAdded, assetReferenceRemoved, assetDeleteBlocked, assetDeleted} {
		if actions[action] == 0 {
			t.Fatalf("missing %s history: %#v", action, actions)
		}
	}
	if _, err := store.listAssetHistory(request, owner, 1, "1:999999"); err == nil {
		t.Fatal("missing history cursor was accepted")
	}
}

func TestProfileReferenceBackfillIsIdempotent(t *testing.T) {
	events := createTestEventStore()
	if err := events.Init(); err != nil {
		t.Fatal(err)
	}
	events.Config.Blossom.PublicURL = "https://media.example"
	store := &BlossomStore{Config: events.Config, Events: events, Schema: events.Schema}
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "3")
	hash := strings.Repeat("c", 64)
	if err := store.saveUploadTags(nostr.Event{PubKey: secret.Public()}, upstream.BlobDescriptor{
		SHA256: hash, Type: "image/png", Size: 7, Uploaded: 100,
	}); err != nil {
		t.Fatal(err)
	}
	profile := signedProfile(t, secret, 200, `{"banner":"https://media.example/`+hash+`.png"}`)
	if _, err := events.ReplaceEvent(profile); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillProfileReferences(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillProfileReferences(context.Background()); err != nil {
		t.Fatal(err)
	}
	var references, additions int
	if err := GetDb().QueryRow("SELECT COUNT(*) FROM " + events.Schema.Prefix("asset_references")).Scan(&references); err != nil {
		t.Fatal(err)
	}
	if err := GetDb().QueryRow("SELECT COUNT(*) FROM "+events.Schema.Prefix("asset_history")+" WHERE action=$1", assetReferenceAdded).Scan(&additions); err != nil {
		t.Fatal(err)
	}
	if references != 1 || additions != 1 {
		t.Fatalf("backfill references=%d additions=%d", references, additions)
	}
}
