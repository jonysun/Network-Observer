# Network Observer eBPF, DNS, and Traffic Analytics Design

**Status:** Design approved in chat on 2026-11-15; written specification ready for review
**Date:** 2026-11-14 (approval confirmed 2026-11-15)
**Scope:** Linux Agent collection, historical traffic attribution, ASN/GeoIP enrichment, REST queries, and Vue analytics.

## Goals

Extend the existing Network Observer so an administrator can answer:

- How many bytes did a selected local or remote port send and receive during the last 24 hours, and how did that change over time?
- Which IPs or DNS names recur or remain active over a selected period?
- Which ASN, organization, country, region, or city is associated with an observed public IP?
- Which values are exact, estimated, unknown, disabled, or missing because collection degraded?

The implementation remains Linux-first and preserves the current active-connection-only model. It does not add packet payload retention, application-layer inspection, or historical per-connection search.

## Existing system constraints

- Endpoint Agent currently inventories IPv4/IPv6 TCP/UDP sockets from procfs and uses Socket Diag/TCP_INFO for available TCP byte counters. UDP per-flow counters and attached eBPF programs are not implemented.
- Gateway Agent supports conntrack and NetFlow v5, IPFIX, and sFlow inputs; gateway eBPF is not implemented.
- ClickHouse stores active snapshots for a short TTL and one-minute traffic summaries for 90 days. Existing traffic dimensions include device, interface, protocol, source/destination IP and port, process, container, and service.
- There is no DNS relationship store, historical connection-presence model, ASN/GeoIP enrichment, traffic time-series endpoint, or high-resolution charting UI.
- Existing endpoint/gateway deduplication and NAT tuple handling must remain in force.

## Decisions

### Per-Agent collection controls

Each Agent independently controls eBPF and DNS metadata collection. Initial defaults are conservative:

```json
{
  "ebpf_mode": "auto",
  "dns_metadata_enabled": false
}
```

`ebpf_mode` accepts `auto`, `on`, and `off`:

- `auto`: detect kernel, BTF, hook, and permission support; attach when supported and otherwise continue existing collectors with a degraded capability report.
- `on`: require the eBPF collector; report startup failure for an explicitly requested mode that cannot attach, while preserving the non-eBPF `snapshot` diagnostic path.
- `off`: do not load or attach eBPF programs.

`dns_metadata_enabled` is an independent per-Agent boolean. It defaults to false. When enabled, the Agent emits DNS query/response metadata only; it must not persist or forward general packet payloads. Disabled, unavailable, encrypted, and unobserved DNS are represented explicitly and are not interpreted as proof that no domain was used.

### Endpoint eBPF counters

Use a Linux CO-RE-capable eBPF implementation, with a maintained Go eBPF library and generated/embedded program objects. Prefer cgroup socket/SKB hooks for endpoint traffic so ingress and egress can be attributed to a local cgroup and normalized into local/remote direction. Use socket cookies where the running kernel and hook provide them; join to Socket Diag/procfs process and container metadata when possible. Keep Socket Diag as the lifecycle and TCP state source.

The Agent reports packet and byte deltas by flow and collection interval. The byte definition is the observed kernel skb length at the selected hook, including the network packet headers represented by that skb; it is not an application-payload byte count. Do not multiply endpoint eBPF counts by a sampling factor. If maps overflow, events are lost, hooks lack direction/identity, or a flow cannot be joined to a socket, record a gap/degradation reason and retain whatever lower-granularity counter source remains available.

The initial scope is endpoint IPv4/IPv6 TCP/UDP. Gateway eBPF is a later extension; gateways continue to use conntrack and flow exporters in this scope. Exporter sampling remains estimated. Endpoint/gateway deduplication retains the current rule: exact endpoint counters take aggregation precedence when associated, otherwise the gateway remains a fallback.

### DNS metadata and domain relationships

When enabled, inspect visible UDP DNS queries and responses and derive only these persisted fields: domain name, answer IP when present, TTL when present, observation time, and the originating device ID needed to scope the relationship. Do not persist interface, process/container attribution, packet bytes, EDNS contents, unrelated DNS sections, or ordinary application data. Discard packet bytes after parsing. DNS metadata collection must never become general application-payload collection.

The first implementation covers UDP DNS on port 53. TCP DNS, DoT, DoH, DNS-over-QUIC, cache hits, and queries outside the Agent's interfaces are not guaranteed to be visible. They produce unknown domain attribution, with a reason where the system can identify the limitation. Do not use reverse DNS as authoritative domain attribution.

The Center keeps DNS observations for seven days by default to resolve time-bounded IP-to-domain relationships. When multiple names map to one IP during overlapping TTL windows, retain candidates with source and validity intervals; show multiple candidates and a confidence/ambiguity state instead of selecting an arbitrary name. Historical traffic may keep the domain association resolved at ingestion time, but expiry or later DNS observations do not rewrite already aggregated traffic.

### ASN and geolocation

Enrich public IPs in the Center using optional, operator-supplied offline MMDB files for ASN and city-level location. The Center must not send observed IPs to a third-party lookup service. A configured database path and its version/update timestamp are retained with the enrichment result. Private, loopback, link-local, reserved, and unmatched addresses return an explicit unknown result. City-level data is approximate and must be labelled as database-derived, not as physical-device location.

The deployment documentation must state that the operator supplies and updates licensed database files. Missing databases degrade enrichment only; ingestion and traffic queries continue to work with unknown ASN/location values.

### Historical storage

Keep `traffic_minute` as the durable 90-day traffic store. Extend each aggregate with explicit local/remote port roles, domain, ASN, organization, country, region, city, enrichment database version, estimate/confidence, and gap reason where known. Store the two normalized directions separately as upload/download relative to the observed endpoint; preserve protocol and observation perspective to prevent ambiguous directional labels. Existing source/destination dimensions remain queryable for compatibility.

Add `connection_presence_minute` for historical persistence analysis. It stores a normalized flow key and minute bucket with first/last seen, observed/active seconds, observation count, directional bytes/packets, and available domain/enrichment/ownership dimensions. Retention is 90 days, aligned with traffic summaries. Presence records are not historical connection records and do not expose a reconstructable event-by-event connection timeline.

Add `dns_observations` with a seven-day TTL, retaining only parsed DNS metadata and source/validity information. Add a bounded/cacheable Center-side IP enrichment representation keyed by IP and MMDB version; avoid high-cardinality labels in Prometheus.

No schema migration may require rewriting retained traffic. Existing rows without new dimensions are returned with empty/unknown enrichment and unavailable historical presence.

### Query API

Preserve `GET /api/v1/traffic/summary` and extend its filters to combine device, interface, protocol, local/remote port, source/destination IP, domain, ASN, organization, country, region, city, process, container, direction, and time bounds. Port roles must be explicit; a request must not silently mix local and remote port semantics. Response values include separate upload/download totals, estimated flags, confidence, and data-gap metadata.

Add:

- `GET /api/v1/traffic/timeseries`: time-bucketed upload/download bytes and packets, with supported buckets of 1 minute, 5 minutes, and 1 hour.
- `GET /api/v1/presence`: flow/IP/domain persistence rankings for a time range, including first/last seen, observed minutes, active seconds, continuity ratio, directional bytes, and enrichment.
- `GET /api/v1/enrichment`: ASN/organization/geography, database version/time, and match/unknown reason for one or more IPs.
- `GET /api/v1/dns/relationships`: time-valid domain-to-IP relationships with source, TTL, device, and ambiguity/confidence.

Endpoints remain behind the existing administrator authentication. Validate ranges, limits, dimension names, bucket sizes, port numbers, country codes, and IP address syntax. Use parameterized ClickHouse queries and bounded result sizes.

### Web console

Extend the Vue console with four traffic views:

1. **Port detail:** device/interface/protocol/local-or-remote port/time filters; 24-hour upload/download totals, peak and average rates, a minute series, and related IP/domain/process/container values.
2. **Persistent connections:** IP/domain, protocol/port, first/last seen, observed minutes, active seconds, continuity ratio, directional bytes, ASN/organization, and location. Support a continuity threshold such as 80%.
3. **Attribution analysis:** ranked IP, domain, ASN, organization, country, region, and city results. Selecting a row filters its time series and related observations. Unknown values remain a visible group.
4. **Live connections:** add domain and available ASN/location to existing live rows, preserving unknown and quality indicators. Continue to show active connections only and do not add per-connection historical search.

Show clear quality states for exact, estimated, unknown, disabled, and collection gaps. Keep the existing manual attribution mechanism and filters. Encode selected filters in the URL where practical. Prometheus labels remain low-cardinality and exclude IPs, domains, ports, process, and container identifiers.

## Failure and degradation behavior

- Missing BTF, unsupported helpers/hooks, insufficient privileges, map initialization failure, or map/event loss must be visible in Agent capability status and per-observation quality metadata. `auto` continues with Socket Diag/procfs or gateway collectors.
- DNS capture failure must not interrupt flow collection. A disabled DNS switch means no DNS-derived domain data is produced by that Agent.
- Missing or stale MMDB data returns unknown enrichment and an explicit database status; it must not block ingestion.
- ClickHouse migrations add tables/columns and TTLs without discarding existing data. Center restarts preserve WAL, traffic, DNS relationships within TTL, and device configuration.
- If collection stops or gaps occur, continuity metrics use observed duration and the UI surfaces the missing interval instead of treating it as continuous inactivity.

## Security and privacy

- DNS collection is opt-in per Agent and off by default. Configuration changes are local to that Agent and are reported as a capability.
- Never store ordinary application payloads. DNS parser emits only allowlisted metadata fields and discards packet content after parsing.
- Do not query external geolocation services. Operators provide offline database files.
- Preserve existing TLS/token modes, WAL protections, credential revocation, authenticated APIs, and audit behavior.
- Avoid DNS names, IPs, ports, and process identities in Prometheus labels or default application logs.

## Rollout

1. Add schema/API compatibility and offline enrichment with unknown-safe behavior.
2. Add Agent eBPF mode, capabilities, endpoint counters, and measurable fallback.
3. Add opt-in DNS metadata and time-valid domain relationships.
4. Add timeseries/presence/enrichment APIs and the traffic console views.
5. Validate on one Linux endpoint and Center, then a gateway plus endpoint, then the 10-endpoint/1-gateway target.

Each collector can be turned off per Agent. Rollback means disabling the new Agent collectors and reverting the Center image while retaining additive database columns/tables. Do not require routing, firewall, conntrack parameter, or Docker network changes.

## Acceptance criteria

- `auto/on/off` behavior is testable with absent BTF, absent privileges, unsupported hooks, and attach failures. In `auto`, the non-eBPF collectors still function.
- On supported Linux kernels, endpoint IPv4/IPv6 TCP/UDP per-flow counters are bidirectional, have documented skb-byte semantics, report loss/overflow, and stay within 5% of a controlled host-level reference over the agreed test traffic. Unsupported kernels report degraded/unavailable rather than zero-as-exact.
- DNS disabled means no DNS metadata or derived domain is emitted by that Agent. DNS enabled maps visible UDP DNS answers to flows within TTL; ambiguous names, expiry, encrypted DNS, and missing observations are represented correctly. No packet payload is found in WAL, logs, or persisted rows.
- Port queries return explicit local/remote role and separate upload/download totals for the last 24 hours; timeseries totals reconcile with the corresponding summary within bucket boundaries.
- Presence correctly computes observed minutes, active seconds, and continuity ratios across start/end boundaries and missing-data intervals.
- Endpoint/gateway duplicates are counted once according to existing precedence, gateway samples stay estimated, and NAT before/after tuples remain available when supplied.
- Public IPs return reproducible ASN/country/region/city for the mounted database version; private/reserved/unmatched addresses and absent databases return unknown without failing ingestion.
- The UI exposes port, IP, domain, ASN, and geography filters/rankings with quality and gap indicators. Empty datasets and old rows render safely.
- Validate database TTL behavior (90-day traffic/presence, seven-day DNS), authenticated access, bounded query behavior, Center restart recovery, and the existing 10-device/1-gateway/10,000-active-flow load target.

## Out of scope

- Windows/macOS Agents, gateway eBPF, DNS/TCP stream reassembly, DoH/DoT decryption, TLS SNI extraction, packet payload storage, third-party live IP lookups, alerts/notifications, and historical per-connection reconstruction.

