# Lossless Flow Telemetry and Scalable Analytics

**Status:** Draft for user approval
**Date:** 2026-11-23
**Scope:** Agent capture/reporting, WAL/protocol, Center ingestion, ClickHouse storage and analytics.

## Intent

Fix the 200,000-row query_too_large failure at its architectural source while retaining short, tiny, failed, suspicious, and long-lived flows. Fast capture must not imply one durable/report row per poll. Dashboard summaries and Top-N must not require moving large intermediate result sets from ClickHouse into Center memory.

## Goals

- Preserve observed flow evidence; do not discard records by duration, byte/packet threshold, or suspiciousness score.
- Reduce repeated reports and dimension/direction storage amplification without losing counter accuracy.
- Make common summaries, trends, independent metric rankings, and relationships aggregate inside ClickHouse and return bounded results.
- Keep exact drill-down evidence for supported ranges, expose query/coverage/rollup lag honestly, and support rebuilding derived data.
- Retain legacy Agent compatibility and the existing 90-day evidence-retention target, subject to capacity validation.

## Non-goals

- Raising or removing the 200,000-row guard as the main fix.
- Claiming polling can observe flows that begin and end wholly between polls.
- Replacing lossless source evidence with approximate-only dashboards.
- Guaranteeing arbitrary combinations of every dimension have instant exact answers without bounded query cost.

## Current constraints

- Agent polls at approximately one-second cadence by default. The existing historical aggregator persists active traffic at minute granularity; a one-hour continuous flow normally produces about 60 history observations, not 3,600.
- Realtime state may still be transmitted frequently when sampled counters change.
- Center ppendTraffic expands traffic across multiple dimensions/directions. Analytics groups large intermediate sets and has an application-level 200,000-row guard; this is not a ClickHouse hard capacity limit.
- ClickHouse ReplacingMergeTree replacement is asynchronous and cannot by itself provide a synchronous uniqueness barrier for additive materialized sums. Retries must be made idempotent before rollup accumulation.

## Approaches considered

1. **Increase poll/report interval or discard low-value flows:** reduces volume but can miss short/small flows; rejected.
2. **Raise the 200,000-row limit / return more rows:** postpones memory and latency failure while leaving amplification intact; rejected as root fix.
3. **Fast capture + coalesced flow updates + canonical facts + pre-aggregation and bounded Top-N:** preserves evidence while moving computation to ClickHouse; recommended.

## Design

### 1. Capture and report cadence

- Keep the available capture/poll cadence independent from transport cadence (one-second default where currently supported). Do not repurpose history snapshot configuration as a transport knob.
- Add a separate realtime report cadence, default 15 seconds and configurable from 5–30 seconds.
- Emit lifecycle transitions promptly: new/attempted/established/failed/closed as supported, plus final counters at close. UDP flow lifecycle may be observed but must not be labeled a confirmed TCP open.
- Coalesce redundant unchanged samples between reports, but preserve every observed flow start/end and lifecycle transition, all byte/packet deltas, and every meaningful metadata/attribution change as an ordered update (or equivalent versioned evidence). Counter totals must reconcile exactly after WAL replay/restart; no intermediate flow, lifecycle, or metadata transition may disappear.
- Lifecycle events take priority under pressure; counter data must be durably queued or produce explicit loss/gap evidence, never be silently dropped. Reconnect/full-state reconciliation repairs live state.
- Center's realtime expiry lease is independent and must exceed three report intervals (60-second default for a 15-second cadence).
- No size/duration filtering. Keep zero-byte and tiny observed flows searchable. Polling-only capture must expose its blind window; event-capable capture must expose kernel/queue loss counters.
- Unknown process/container/domain attribution stays explicitly unknown; do not guess via nearby timestamps or approximate tuple matching.

### 2. Canonical facts, idempotency, and dimension modeling

- Give each immutable event/update a stable event ID distinct from its flow ID. WAL replay preserves event ID and sequence.
- Introduce a durable idempotency boundary before any additive rollup: persist the event and its deduplication key atomically in Center's durable ingest/WAL or equivalent, then acknowledge Agent only after durable acceptance. A retry with the same ID must not apply counters twice. ClickHouse async replacement alone is insufficient.
- Store one canonical fact per accepted event/counter delta, with dimensions as columns and upload/download counters together. Do not copy each observation into separate rows for every dimension and direction.
- Model additive counter deltas separately from lifecycle/open events. Count confirmed opens from unique, deduplicated TCP-established transition events; count observed flows from their stable flow identity under explicitly documented semantics. Never infer connection count from minute samples.
- Preserve timestamp, device/agent, flow/event IDs, protocol, local/remote endpoints, process/container/service, interface/network namespace, domain/ASN/geography when known, trust/internal flags, estimate/quality, source, and gap reason.
- Maintain raw canonical evidence for at least 90 days if capacity/load tests support it; partition by event time and apply retention explicitly.

### 3. ClickHouse analytics and rollups — root fix for 200k

- Remove the analytics pattern that fetches up to 200,000 grouped rows to Center and aggregates/sorts them there. Keep a defensive limit for detailed rows, but do not apply it to aggregate scan volume.
- Push filtering, grouping, metric calculation, ordering, and LIMIT N into ClickHouse. Each metric is ranked independently: connection count, upload bytes, download bytes, and total bytes must each produce its true Top N, not a Top N prefiltered by another metric.
- Build minute/hour rollups for common dashboard grain and dimensions/relationships (device, remote IP, process, domain, port, protocol, interface, container/geography, process→IP, process→domain). Rollups return bounded Top-N/trend rows; raw canonical facts remain available for drill-down.
- Keep dimensions as columns on a shared rollup grain where practical; avoid a Cartesian dimension expansion. Use narrowly scoped aggregate tables only where measured query plans prove they are needed.
- Ensure rollup input is deduplicated before additive aggregation. Select one explicit design during implementation: (a) durable dedup/outbox that applies accepted deltas once, or (b) rebuildable bucket aggregates derived from deduplicated canonical facts. Do not use non-idempotent incremental sums over retryable raw inserts.
- Late events and corrected attribution must be handled through deterministic bucket rebuild/reconciliation or explicit compensating revisions; publish rollup watermark/lag. Every rollup needs a defined rebuild path and parity checks against canonical facts.
- Common dashboard queries should scan rollups only. Uncommon filters may query raw canonical facts with exact filters, bounded execution time/bytes, pagination for detail, and explicit partial/timeout status. Never silently truncate and claim completeness.
- Keep 200,000 as a protective ceiling for raw/detail result materialization if needed, not as a ceiling on rows scanned internally by ClickHouse or on the number of events represented by an aggregate response.

### 4. API behavior and coverage

- Overview APIs return summary, time series, per-dimension rankings, relationship rankings, capability/coverage metadata, and rollup freshness. Responses are bounded by requested dimensions/limit.
- Include upload, download, total bytes, observed connection count, confirmed TCP opens, estimated flag, gap reason, availability, and coverage/watermark where applicable.
- Maintain independent Top-N semantics and stable direction rules regardless of endpoint roles.
- Distinguish no data, no data in range, unsupported capability, unavailable historical dimension, degraded attribution/capture, partial coverage, loading, timeout, and query failure.
- Validate range, dimensions, limit, trust/internal filters; apply query time/memory limits and cancellation. Preserve old API and old Agent behavior during migration.

### 5. Migration and rollout

- Add schema/protocol compatibly; accept old and new Agent messages.
- Backfill canonical facts/rollups where source history permits. Dual-write only with explicit idempotency and byte/event parity checks; do not double-count old and new paths.
- Compare old/new totals and independent rankings on representative windows, including sparse short-flow and high-cardinality workloads.
- Switch dashboard reads to rollups only after parity and latency criteria pass; retain rollback switch and document behavior of historical windows lacking required source dimensions.
- Observe ingest rate, dedup conflicts, WAL backlog, event loss, canonical bytes, rollup lag/rebuilds, query scanned rows/bytes, memory, timeout rate, and p50/p95 latency.

## Acceptance criteria

- One-second capture can be retained while redundant unchanged samples are coalesced (default 15-second report cadence); all flow/lifecycle/meaningful attribution transitions and exact counters survive WAL replay, with lifecycle changes reported promptly.
- Short, one-packet, zero/tiny-byte, failed, and suspicious test flows are retained when the capture mechanism observes them; none are removed by threshold.
- Agent retries/restarts do not inflate event/open/byte counts; WAL delivery is idempotent.
- Directional byte accounting is exact; app/domain attribution is not fabricated.
- Each metric's Top N matches an independent exact reference query, including objects outside another metric's Top N.
- Dashboard query result size remains bounded even for high-cardinality ranges; no 200k intermediate-result failure for supported rollup queries.
- Common query p95 target and fleet/storage budget are established from baseline/load tests before release; rollup freshness and partial coverage are visible.
- Rollups can be rebuilt/reconciled from canonical evidence, and totals match within documented late-arrival/correction semantics.
- Legacy Agents ingest normally; unsupported capabilities are not represented as zero.

## Decisions left to implementation

- Exact Linux hook set and definitions for observed flow vs confirmed TCP-established event.
- Durable dedup/outbox mechanism compatible with current Agent ACK/WAL and Center deployment topology.
- Exact rollup schema, ClickHouse engine/order keys, aggregation states, correction strategy, and backfill scheduling based on query plans and cardinality measurements.
- Per-Agent cadence configuration vs Center policy; full-state reconciliation cadence.
- Measured p95 latency, 90-day storage budget, and supported exact raw-drill-down limits.
