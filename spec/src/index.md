# Lapis Relay

Lapis Relay is the Lapis-maintained NIP-29 relay image. It is based on the
[Unicity Relay](https://github.com/unicitynetwork/unicity-relay) fork of
[Zooid](https://github.com/coracle-social/zooid) and retains the upstream MIT
license and history.

This specification is the canonical description of the relay's implemented
behavior. It brings the protocol model, access policy, plugin registry, Buzz
compatibility, forge integrations, deployment guidance, operational behavior,
and deferred work into one linked mdBook.

The relay has three firm boundaries:

- signed Nostr events remain authoritative for native collaboration;
- NIP-29 permissions are enforced by the relay on every relay-hosted surface;
- Git storage and external-provider automation are capability-selected
  sidecars, not alternate sources of Nostr identity or authorization.

The Lapis defaults allow anonymous reads of public, non-hidden groups and let
authenticated users participate in existing public groups. Group creation is
restricted to configured relay administrators with
`groups.admin_create_only = true`.

Start with [Architecture](architecture.md), then use the sections in the
[summary](SUMMARY.md) as the functional index. Current deployment guidance is
under [Operations](operations/deployment.md). The old AWS migration document is
retained only as an explicitly labelled historical record.
