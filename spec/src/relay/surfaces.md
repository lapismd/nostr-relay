# Relay surfaces

## Nostr transport

The root HTTP endpoint upgrades ordinary Nostr WebSocket connections and
supports authenticated `REQ`, `EVENT`, and related standard relay messages.
All reads and writes pass through the same virtual-relay and NIP-29 policy
hooks, regardless of transport.

## Buzz-compatible HTTP

The compatibility adapter exposes:

| Route | Purpose |
| --- | --- |
| `POST /query` | Query one or more Nostr filters. |
| `POST /events` | Submit a signed event through the normal validation and storage path. |
| `POST /count` | Count unique events matching one or more filters. |
| `GET /info` | Return the same NIP-11 document as `GET /` with the Nostr JSON accept header. |
| `GET\|HEAD /media/{sha256_ext}` | Read a Blossom object through Buzz's media alias. |
| `PUT /media/upload` | Internal alias for the standard `PUT /upload` Blossom handler. |

The POST routes require a fresh NIP-98 authorization event whose URL, method,
and payload tags match the request. An authorization is accepted only inside
the configured short time window and cannot be replayed in the same process.
Buzz Blossom tokens use unpadded Base64URL and authority-scoped BUD-11
`server` tags; the adapter verifies them before delegating to Blossom policy.

## Repository and forge HTTP

Repository browsing, pull-request comparison, Smart HTTP, provider operations,
and webhooks are described by the [forge model](../forge/model.md), [capability
architecture](../forge/capabilities.md), and provider chapters. The stable
route families are:

- `/git/<owner>/<repo>.git` for backends with relay Smart HTTP;
- `/api/git/<owner>/<repo>/...` for authenticated browser reads;
- `/api/pull-requests/<event-id>/diff` for reduced pull-request comparison;
- `/api/forge/...` for authenticated capability, administration, operation,
  migration, and CI endpoints;
- `/internal/walgit/events` for authenticated private-sidecar ref events; and
- `/webhooks/github` for verified, deduplicated GitHub deliveries.

## Service endpoints

`GET /metrics` exposes Prometheus metrics. An optional `PPROF_ADDR` opens a
separate profiling listener; it must bind to localhost or another private
interface and must never be exposed publicly.
