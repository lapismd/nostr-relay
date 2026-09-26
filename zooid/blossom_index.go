package zooid

import (
	"context"
	"iter"
	"net/http"
	"strconv"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	upstream "fiatjaf.com/nostr/nipb7/blossom"
	"zooid/internal/khatru/blossom"
)

type checkedBlobIndex struct {
	blossom.EventStoreBlobIndexWrapper
}

func (index checkedBlobIndex) Keep(ctx context.Context, descriptor upstream.BlobDescriptor, owner nostr.PubKey) error {
	if pending := pendingBlobIndex(ctx); pending != nil {
		pending.descriptor = descriptor
		pending.owner = owner
		return nil
	}
	return index.save(ctx, descriptor, owner)
}

type blobIndexContextKey struct{}

type blobIndexPending struct {
	descriptor upstream.BlobDescriptor
	owner      nostr.PubKey
}

func stageBlobIndex(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), blobIndexContextKey{}, &blobIndexPending{}))
}

func pendingBlobIndex(ctx context.Context) *blobIndexPending {
	pending, _ := ctx.Value(blobIndexContextKey{}).(*blobIndexPending)
	return pending
}

func (index checkedBlobIndex) save(ctx context.Context, descriptor upstream.BlobDescriptor, owner nostr.PubKey) error {
	next, stop := iter.Pull(index.Store.QueryEvents(nostr.Filter{
		Authors: []nostr.PubKey{owner}, Kinds: []nostr.Kind{24242},
		Tags: nostr.TagMap{"x": {descriptor.SHA256}}, Limit: 1,
	}, 1))
	defer stop()
	if _, exists := next(); exists {
		return nil
	}
	event := nostr.Event{
		PubKey: owner, Kind: 24242, CreatedAt: descriptor.Uploaded,
		Tags: nostr.Tags{{"x", descriptor.SHA256}, {"type", descriptor.Type}, {"size", strconv.Itoa(descriptor.Size)}},
	}
	event.ID = event.GetID()
	if err := index.Store.SaveEvent(event); err != nil && err != eventstore.ErrDupEvent {
		return err
	}
	return nil
}
