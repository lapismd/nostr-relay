package zooid

import (
	"context"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"zooid/internal/buzzcompat"
)

type authenticatedPubkeyContextKey struct{}

func withAuthenticatedPubkey(ctx context.Context, pubkey nostr.PubKey) context.Context {
	return context.WithValue(ctx, authenticatedPubkeyContextKey{}, pubkey)
}

func authenticatedPubkey(ctx context.Context) (nostr.PubKey, bool) {
	if pubkey, ok := khatru.GetAuthed(ctx); ok {
		return pubkey, true
	}
	pubkey, ok := ctx.Value(authenticatedPubkeyContextKey{}).(nostr.PubKey)
	return pubkey, ok
}

// enableBuzzCompatibility is the sole integration point between the
// upstream-derived relay and Lapis's Buzz transport adapter.
func enableBuzzCompatibility(instance *Instance) {
	instance.Relay.Info.AddSupportedNIP(98)
	base := instance.Relay.Router()
	handler := buzzcompat.New(buzzcompat.Backend{
		WithAuthenticatedPubkey: withAuthenticatedPubkey,
		AuthorizeQuery:          instance.OnRequest,
		Query:                   instance.QueryStored,
		Publish:                 instance.Relay.AddEvent,
		Broadcast: func(event nostr.Event) {
			instance.Relay.BroadcastEvent(event)
		},
		NIP11: instance.Relay.HandleNIP11,
	})
	instance.Relay.SetRouter(handler.Wrap(base))
}
