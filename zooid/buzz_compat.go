package zooid

import (
	"context"
	"fmt"

	"fiatjaf.com/nostr"
	"zooid/internal/buzzcompat"
	"zooid/internal/khatru"
)

func withAuthenticatedPubkey(ctx context.Context, pubkey nostr.PubKey) context.Context {
	return withAuthenticatedPrincipal(ctx, authenticatedPrincipal{PubKey: pubkey})
}

func authenticatedPubkey(ctx context.Context) (nostr.PubKey, bool) {
	if pubkey, ok := khatru.GetAuthed(ctx); ok {
		return pubkey, true
	}
	principal, ok := principalFromContext(ctx)
	return principal.PubKey, ok
}

// enableBuzzCompatibility is the sole integration point between the
// upstream-derived relay and Lapis's Buzz transport adapter.
func enableBuzzCompatibility(instance *Instance) {
	instance.Relay.Info.AddSupportedNIP("98")
	instance.Relay.Info.AddSupportedNIPs([]string{"29", "44", "AA", "AE", "AM", "AO", "AP", "OA"})
	base := instance.Relay.Router()
	backend := buzzcompat.Backend{
		Authenticate: func(ctx context.Context, auth buzzcompat.HTTPAuth) (context.Context, error) {
			return instance.Agents.AuthenticateHTTP(ctx, auth.PubKey, auth.SignedAt, auth.AuthTag)
		},
		WithAuthenticatedPubkey: withAuthenticatedPubkey,
		AuthorizeQuery:          instance.OnRequest,
		Query:                   instance.QueryStored,
		QueryBefore:             instance.QueryStoredBefore,
		EphemeralQuery: func(ctx context.Context, filters []nostr.Filter) ([]nostr.Event, error) {
			if instance.Presence == nil {
				return nil, fmt.Errorf("error: presence unavailable")
			}
			return instance.Presence.Query(ctx, filters)
		},
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
