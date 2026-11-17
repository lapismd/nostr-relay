package zooid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	upstream "fiatjaf.com/nostr/nipb7/blossom"
	"zooid/internal/khatru/blossom"
)

type failingBlobIndexStore struct {
	eventstore.Store
	saves int
	err   error
}

func (store *failingBlobIndexStore) QueryEvents(nostr.Filter, int) iter.Seq[nostr.Event] {
	return func(func(nostr.Event) bool) {}
}

func (store *failingBlobIndexStore) SaveEvent(nostr.Event) error {
	store.saves++
	return store.err
}

func TestCheckedBlobIndexStagesUntilByteWrite(t *testing.T) {
	store := &failingBlobIndexStore{err: errors.New("database unavailable")}
	index := checkedBlobIndex{blossom.EventStoreBlobIndexWrapper{Store: store}}
	request := stageBlobIndex(httptest.NewRequest(http.MethodPut, "/upload", nil))
	owner := nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1").Public()
	descriptor := upstream.BlobDescriptor{SHA256: strings.Repeat("a", 64), Type: "image/png", Size: 3, Uploaded: nostr.Now()}
	if err := index.Keep(request.Context(), descriptor, owner); err != nil || store.saves != 0 {
		t.Fatalf("descriptor committed before bytes: saves=%d err=%v", store.saves, err)
	}
	pending := pendingBlobIndex(request.Context())
	if pending == nil || pending.descriptor.SHA256 != descriptor.SHA256 || pending.owner != owner {
		t.Fatalf("missing staged descriptor: %+v", pending)
	}
	if err := index.save(request.Context(), pending.descriptor, pending.owner); !errors.Is(err, store.err) || store.saves != 1 {
		t.Fatalf("index error was lost: saves=%d err=%v", store.saves, err)
	}
	store.err = nil
	if err := index.Keep(context.Background(), descriptor, owner); err != nil || store.saves != 2 {
		t.Fatalf("direct index save failed: saves=%d err=%v", store.saves, err)
	}
}

func TestWriteBlobAtomic(t *testing.T) {
	dir := t.TempDir()
	data := []byte("blossom image bytes")
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if err := writeBlobAtomic(dir, hash, data); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, hash)
	if err := os.WriteFile(path, []byte("truncated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeBlobAtomic(dir, hash, data); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil || string(stored) != string(data) {
		t.Fatalf("stored=%q err=%v", stored, err)
	}
	if err := writeBlobAtomic(dir, hash, []byte("wrong")); err == nil {
		t.Fatal("accepted bytes with a different hash")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected temporary files: %v %v", entries, err)
	}
}
