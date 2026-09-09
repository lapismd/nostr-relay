# Observability

The relay exposes Prometheus metrics at `GET /metrics` on the main listener.
Metrics are refreshed in the background. Cache-derived group and membership
counts use the in-memory projections; event totals use bounded database
queries or estimates. Private and hidden group IDs are excluded from
per-group metrics.

All metrics carry an `instance` label derived from the configured relay schema.

| Metric | Type | Meaning |
| --- | --- | --- |
| `zooid_groups_total` | Gauge | Total groups. |
| `zooid_groups_private` | Gauge | Private groups. |
| `zooid_groups_hidden` | Gauge | Hidden groups. |
| `zooid_groups_closed` | Gauge | Closed groups. |
| `zooid_group_members` | Gauge | Members per public group, bounded to 1,000 groups. |
| `zooid_group_messages` | Gauge | Chat messages per public group. |
| `zooid_group_members_total` | Gauge | Distinct members across groups. |
| `zooid_groups_tracked` | Gauge | Groups represented in per-group metrics. |
| `zooid_relay_members_total` | Gauge | Relay members. |
| `zooid_banned_pubkeys_total` | Gauge | Banned pubkeys. |
| `zooid_banned_events_total` | Gauge | Banned events. |
| `zooid_events_total` | Gauge | Estimated stored events. |
| `zooid_messages_total` | Gauge | Stored kinds `9` and `10`. |
| `zooid_query_duration_seconds` | Histogram | Database query and scan time. |
| `zooid_retention_deleted_total` | Counter | Messages deleted by retention. |
| `zooid_retention_run_duration_seconds` | Histogram | Retention pass duration. |

The optional [docker-compose.metrics.yml](../../../docker-compose.metrics.yml)
starts Grafana Alloy with [alloy/config.alloy](../../../alloy/config.alloy).
Supply the Grafana remote-write URL, username, and API token only through the
runtime environment. To scrape a host-run relay, point Alloy at
`host.docker.internal:3334`; the Compose relay target is `relay:3334`.

The query-performance integration test can expose a temporary metrics listener
with `METRICS_PORT` and keep it open with `METRICS_WAIT` for an external
scraper.

Set `PPROF_ADDR`, for example `127.0.0.1:6060`, only on a private interface.
Use SSH or an equivalent private port forward for profiling; pprof must not be
published through the relay ingress.
