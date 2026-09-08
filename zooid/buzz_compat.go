package zooid

import (
	"context"

	"fiatjaf.com/nostr"
	"zooid/internal/buzzcompat"
	"zooid/internal/khatru"
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
	instance.Relay.Info.AddSupportedNIP("98")
	base := instance.Relay.Router()
	backend := buzzcompat.Backend{
		WithAuthenticatedPubkey: withAuthenticatedPubkey,
		AuthorizeQuery:          instance.OnRequest,
		Query:                   instance.QueryStored,
		QueryBefore:             instance.QueryStoredBefore,
		CountMany: func(ctx context.Context, filters []nostr.Filter) (buzzcompat.CountResult, error) {
			result, err := instance.CountStored(ctx, filters)
			return buzzcompat.CountResult{
				Count:       result.Count,
				Approximate: result.Approximate,
				HyperLogLog: nostr.HexEncodeToString(result.HyperLogLog),
			}, err
		},
		Publish: instance.Relay.AddEvent,
		Broadcast: func(event nostr.Event) {
			instance.Relay.BroadcastEvent(event)
		},
		NIP11: instance.Relay.HandleNIP11,
	}
	if instance.GitForge != nil {
		backend.GitHTTP = instance.GitForge.ServeGitHTTP
		backend.RepositoryAPI = instance.GitForge.ServeRepositoryAPI
		backend.PullRequestAPI = instance.GitForge.ServePullRequestAPI
		backend.WalGitEvents = instance.GitForge.ServeWalGitEvents
		backend.ForgeAPI = instance.GitForge.ServeForgeAPI
		backend.GitHubWebhook = instance.GitForge.ServeGitHubWebhook
	}
	handler := buzzcompat.New(backend)
	instance.Relay.SetRouter(handler.Wrap(base))
}
