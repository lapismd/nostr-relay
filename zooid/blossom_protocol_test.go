package zooid

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	upstream "fiatjaf.com/nostr/nipb7/blossom"
	lapisblossom "zooid/internal/blossom"
	vendorblossom "zooid/internal/khatru/blossom"
)

func TestBlossomTagFilteringAndReportPersistence(t *testing.T) {
	events := createTestEventStore()
	if err := events.Init(); err != nil {
		t.Fatal(err)
	}
	store := &BlossomStore{Config: events.Config, Events: events, Schema: events.Schema}
	owner := nostr.Generate().Public()
	index := vendorblossom.EventStoreBlobIndexWrapper{Store: events, ServiceURL: "https://test.com"}
	avatarHash := strings.Repeat("a", 64)
	pluginHash := strings.Repeat("b", 64)
	for _, item := range []struct {
		hash     string
		uploaded nostr.Timestamp
	}{{avatarHash, 100}, {pluginHash, 200}} {
		if err := index.Keep(context.Background(), upstream.BlobDescriptor{
			URL: "https://test.com/" + item.hash + ".png", SHA256: item.hash,
			Size: 42, Type: "image/png", Uploaded: item.uploaded,
		}, owner); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.saveUploadTags(nostr.Event{PubKey: owner, Tags: nostr.Tags{{"lapis-purpose", "avatar"}}}, upstream.BlobDescriptor{SHA256: avatarHash}); err != nil {
		t.Fatal(err)
	}
	if err := store.saveUploadTags(nostr.Event{PubKey: owner, Tags: nostr.Tags{{"lapis-purpose", "plugin"}, {"lapis-plugin", "notes"}}}, upstream.BlobDescriptor{SHA256: pluginHash}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/list/"+owner.Hex(), nil)
	listed, err := store.listBlobs(request, owner, lapisblossom.ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].SHA256 != pluginHash || listed[1].SHA256 != avatarHash {
		t.Fatalf("ordered list: %#v", listed)
	}
	listed, err = store.listBlobs(request, owner, lapisblossom.ListQuery{Limit: 10, Tags: map[string][]string{"purpose": {"avatar"}}})
	if err != nil || len(listed) != 1 || listed[0].SHA256 != avatarHash {
		t.Fatalf("avatar filter: %#v, %v", listed, err)
	}
	listed, err = store.listBlobs(request, owner, lapisblossom.ListQuery{Limit: 1, Cursor: pluginHash})
	if err != nil || len(listed) != 1 || listed[0].SHA256 != avatarHash {
		t.Fatalf("cursor: %#v, %v", listed, err)
	}
	_, err = store.listBlobs(request, owner, lapisblossom.ListQuery{Limit: 1, Cursor: strings.Repeat("c", 64)})
	if !errors.Is(err, lapisblossom.ErrCursorNotFound) {
		t.Fatalf("missing cursor: %v", err)
	}
	report := nostr.Event{Kind: nostr.KindReporting, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"x", avatarHash, "spam"}}}
	if err := report.Sign(nostr.Generate()); err != nil {
		t.Fatal(err)
	}
	if err := store.saveReport(report); err != nil {
		t.Fatal(err)
	}
	if err := store.saveReport(report); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := GetDb().QueryRow("SELECT COUNT(*) FROM " + events.Schema.Prefix("blossom_reports")).Scan(&count); err != nil || count != 1 {
		t.Fatalf("report count=%d error=%v", count, err)
	}
}
