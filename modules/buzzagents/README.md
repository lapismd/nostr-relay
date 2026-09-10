# Buzz agent protocol module

This independent Go module owns the transport-neutral subset of Buzz's agent
protocol used by Lapis Relay: NIP-OA credential verification, NIP-AA condition
evaluation, event-kind/storage classification, private result visibility,
durable and observer-envelope validation, and the in-memory presence
projection.

The module must not import Zooid, Khatru, PostgreSQL, HTTP, Git/forge code, or an
agent runtime. Relay integration stays in `zooid/agent_policy.go`; HTTP bridge
integration stays in `internal/buzzcompat`.

`UPSTREAM_VERSION` pins the exact `block/buzz` source audited for this module.
`UPSTREAM_FILES.sha256` pins the NIPs and executable relay paths that define the
contract. Run `../../scripts/update-buzz-agent-upstream.sh --check` to verify the
pin. To begin a refresh, run the script with a full Buzz commit SHA, review every
changed pinned file, update this module and the canonical specification, then
run the module, root, integration, and image validation lanes.
