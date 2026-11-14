# Network Observer Traffic Analytics Implementation Plan

This plan follows the approved design in `docs/superpowers/specs/2026-11-14-ebpf-traffic-dns-geolocation-design.md`.

## Phase 1: additive data contracts and configuration

1. Extend the protobuf observation contract with domain, DNS metadata, ASN/GeoIP, quality, and gap fields. Regenerate Go bindings with a checked-in reproducible command and add conversion tests.
2. Extend `internal/domain` with the same fields plus `TrafficSeriesPoint`, `PresencePoint`, `Enrichment`, and `DNSRelationship` response models.
3. Add `ebpf_mode` (`auto|on|off`) and `dns_metadata_enabled` to Agent configuration, validation, registration defaults, example JSON, Compose, systemd documentation, and config tests.
4. Keep old configurations valid by applying `auto` and `false` defaults when fields are absent.

## Phase 2: endpoint collectors

1. Add a Linux eBPF collector package with a CO-RE object loader, map lifecycle, per-flow counters, loss counters, and capability probing. Isolate platform-specific loading behind an interface so unsupported builds compile and report unavailable.
2. Join eBPF socket-cookie/cgroup counters to procfs/Socket Diag observations, calculate monotonic deltas, and preserve TCP_INFO fallback. UDP receives exact eBPF counters only when the map join succeeds.
3. Add a DNS metadata collector for visible UDP/53 packets. Parse allowlisted query/answer fields, discard packet bytes, and emit only structured metadata when enabled.
4. Add capability and data-gap tests for auto/on/off, missing BTF, denied map access, map loss, DNS disabled, malformed packets, TTL expiry, and no-payload persistence.

## Phase 3: Center storage and enrichment

1. Add ClickHouse migrations for `connection_presence_minute`, `dns_observations`, and an enrichment cache. Add additive columns to `traffic_minute` and preserve existing TTLs/rows.
2. Compute minute traffic dimensions from normalized local/remote roles, DNS validity windows, and offline ASN/GeoIP lookup. Use an injected lookup interface with an unknown-safe implementation when MMDB files are absent.
3. Aggregate presence by normalized flow/IP/domain key, retaining observed minutes, active seconds, first/last seen, bytes, packets, quality, and gaps. Keep endpoint/gateway deduplication before aggregation.
4. Add tests for direction semantics, port roles, presence continuity, TTLs, NAT, sampling estimates, enrichment unknowns, and duplicate observations.

## Phase 4: REST and WebSocket query surface

1. Extend traffic summary query parsing with validated filters for local/remote port, IP, domain, ASN, organization, country, region, city, process/container, direction, and time bounds.
2. Implement `/api/v1/traffic/timeseries`, `/api/v1/presence`, `/api/v1/enrichment`, and `/api/v1/dns/relationships` behind existing admin authentication and bounded parameterized queries.
3. Add handler tests for invalid dimensions, ports, buckets, ranges, limits, authentication, empty data, and quality/gap fields.

## Phase 5: Vue visualizations

1. Add typed API clients and state for filters, time ranges, upload/download series, presence results, enrichment, and DNS relationships.
2. Add port detail view with 24-hour totals, peak/average rates, and an SVG/canvas minute area chart that has an accessible table fallback.
3. Add persistent connections and attribution analysis views with IP/domain/ASN/geography rankings, continuity threshold, and linked filters.
4. Add domain/ASN/geography columns and quality badges to live connections. Preserve manual attribution and WebSocket refresh.
5. Add responsive styles, loading/empty/error states, and URL-preserved filters. Run `npm run build` and browser smoke tests.

## Phase 6: deployment, documentation, and verification

1. Add optional eBPF host mounts/capabilities to Agent Compose and systemd guidance without changing routes, firewall, Docker networks, or conntrack settings.
2. Add MMDB volume/configuration documentation and health/capability reporting; do not bundle licensed databases.
3. Update README and example configs with DNS privacy, skb-byte semantics, unknown behavior, retention, and rollback instructions.
4. Run `go test ./...`, `go vet ./...`, frontend build, Compose config checks, Center/Agent image builds, and focused integration tests. Record unsupported-kernel behavior explicitly.

## Dependency and rollout order

Implement Phases 1 → 3 before the new UI, then Phase 4 → 5, and finish Phase 6. Each phase must retain a runnable Center and Agent. Roll back by setting `ebpf_mode=off`, `dns_metadata_enabled=false`, and deploying the previous Center image; additive tables/columns may remain.

