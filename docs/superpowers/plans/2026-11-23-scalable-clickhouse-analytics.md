# Scalable ClickHouse Analytics Implementation Plan

> **For agentic workers:** Execute this plan task-by-task in this session. Keep each task independently testable; do not alter unrelated user files.

**Goal:** Eliminate the Center-side 200,000-row analytics failure by having ClickHouse perform filtering, grouping, metric ranking, and Top-N, returning only bounded dashboard results while preserving current API semantics and evidence.

**Architecture:** Rework the existing analytics query path rather than replacing the Agent protocol or storage schema in this first implementation. Use ClickHouse SQL to aggregate traffic and connection-presence facts, independently rank each metric for each requested dimension, calculate summary/trends/relationships, and return bounded rows. Keep raw/detail paths and the 200,000 guard only where materializing actual detail is necessary. Use the existing `traffic_minute` and `connection_presence_minute` tables and stable event keys; separately measure whether a canonical storage/rollup migration is warranted after the read-path fix is proven.

**Tech Stack:** Go, `database/sql`, ClickHouse SQL/Native, existing protobuf/domain structs, Go tests.

**Spec:** `docs/superpowers/specs/2026-11-23-lossless-flow-telemetry-design.md`

## Global Constraints

- Do not filter evidence by duration, byte count, packet count, or suspiciousness.
- Preserve exact upload/download totals and current connection-count semantics.
- Each metric's Top-N is independently correct; do not derive upload/download Top-N from total-byte Top-N.
- Do not silently truncate output or label partial data complete.
- Keep existing `/api/v1/overview/rankings` compatibility and current Agent compatibility.
- Do not change unrelated worktree items, including untracked `NUL` and `internal/agent/collector.go.orig`.

---

### Task 1: Characterize analytics result semantics

**Files:**
- Modify: `internal/store/analytics_test.go`
- Modify: `internal/store/clickhouse_queries_test.go`
- Modify: `internal/domain/analytics.go` only if testable response metadata requires an additive field (otherwise leave untouched)

**Interfaces:**
- Inputs/outputs remain `AnalyticsFilter` and `domain.AnalyticsDashboard`.
- Tests become the reference for metric, direction, connection, empty-data, and Top-N behavior.

- [ ] Add adversarial fixture assertions where the highest uploader is not the highest total-byte object and the highest connector has zero bytes.
- [ ] Add assertions for stable tie-breaking, unknown dimension labels, and a device-scoped query result.
- [ ] Run `go test ./internal/store ./internal/server` and verify the tests pass against current behavior or identify the exact behavior gap before refactoring.

### Task 2: Build safe SQL aggregation primitives

**Files:**
- Create: `internal/store/analytics_sql.go`
- Create: `internal/store/analytics_sql_test.go`
- Modify: `internal/store/analytics.go`

**Interfaces:**
- Add private builders that accept validated `AnalyticsFilter`, allowlisted dimension names, trusted IPs, and LAN ranges; they return SQL plus positional arguments.
- All user-derived values remain bound parameters. SQL identifiers/expressions come only from `analyticsColumns` and fixed expressions.
- Builders cover summary, time series, per-dimension metrics, relationship rankings, and device rankings.

- [ ] Add table-driven tests for every allowlisted dimension and filter combination (device, time, trust, hide-internal, detail).
- [ ] Assert generated ranking SQL computes each metric independently and applies `LIMIT` inside ClickHouse for each metric group.
- [ ] Assert generated SQL never requests `analyticsRowLimit+1` aggregate rows and retains explicit query execution-time settings.
- [ ] Run `go test ./internal/store`.

### Task 3: Push summary and time-series aggregation into ClickHouse

**Files:**
- Modify: `internal/store/analytics.go`
- Modify: `internal/store/analytics_sql.go`
- Modify: `internal/store/analytics_test.go`

**Interfaces:**
- `AnalyticsDashboard(ctx, filter)` remains the store entrypoint and returns the same dashboard shape.
- Summary returns one aggregate row; timeseries returns at most the computed bounded bucket count.

- [ ] Query summary and trends from ClickHouse directly, including upload/download, observed connections, devices with traffic, and nullable confirmed TCP-open capability.
- [ ] Preserve the existing UTC bucket sizing and filtering rules.
- [ ] Add SQL-result tests for empty ranges and totals crossing bucket boundaries.
- [ ] Run `go test ./internal/store ./internal/server`.

### Task 4: Push dimension and relationship rankings into ClickHouse

**Files:**
- Modify: `internal/store/analytics.go`
- Modify: `internal/store/analytics_sql.go`
- Modify: `internal/store/analytics_test.go`
- Modify: `internal/store/clickhouse_queries_test.go`

**Interfaces:**
- Keep the JSON fields `dimension_rankings`, `device_rankings`, `device_connection_rankings`, and `relationship_rankings` unchanged.
- Return at most `filter.Limit` rows per dimension/metric/relationship from ClickHouse; Center only maps bounded rows into domain values.

- [ ] Aggregate traffic by one requested dimension at a time in ClickHouse and return the independent upload, download, and total-byte Top-N sets.
- [ ] Aggregate distinct observed connections from `connection_presence_minute` in ClickHouse and return the independent connections Top-N, including zero-byte flows.
- [ ] Aggregate relationship pairs (`process→remote_ip`, `process→domain`, `remote_ip→remote_port`, `device→remote_ip`, `protocol→remote_ip`) inside ClickHouse; preserve current filters and deterministic ties.
- [ ] Ensure no unrelated dimension is added to a `GROUP BY`; do not create the cross-product grouping that currently drives intermediate cardinality.
- [ ] Test each metric against fixtures where the winners differ, and verify per-group row counts are bounded by the requested limit.
- [ ] Run `go test ./internal/store ./internal/server`.

### Task 5: Integrate bounded queries and remove aggregate result guard

**Files:**
- Modify: `internal/store/analytics.go`
- Modify: `internal/server/analytics.go`
- Modify: `internal/store/analytics_test.go`
- Modify: `internal/server/server_test.go`

**Interfaces:**
- HTTP request parameters and response JSON remain backward-compatible.
- `ErrAnalyticsResultLimit` remains only for explicitly bounded raw/detail operations, not dashboard aggregates.

- [ ] Replace the current wide grouped-row reads and Go-side full-map ranking path with bounded ClickHouse query results.
- [ ] Keep the separate live-connection query scoped and bounded; add query cancellation/timeout handling to all dashboard SQL.
- [ ] Ensure the dashboard endpoint no longer returns `query_too_large` solely because aggregate input cardinality exceeds 200,000.
- [ ] Preserve timeout/busy/invalid-filter statuses and avoid treating partial results as complete.
- [ ] Run `go test ./internal/store ./internal/server`.

### Task 6: Verify cardinality behavior and full repository

**Files:**
- Modify: `internal/store/analytics_test.go` or create `internal/store/analytics_benchmark_test.go`
- No production-file changes unless verification finds a defect.

- [ ] Add a benchmark or high-cardinality query fixture representing more than 200,000 source groups while asserting only bounded result rows are materialized in Center.
- [ ] Compare all returned totals and Top-N against an exact reference aggregation on adversarial fixtures.
- [ ] Run `go test ./...`.
- [ ] Run `go vet ./...`.
- [ ] Run `npm run build`.
- [ ] Run `docker compose config`.
- [ ] Record any unavailable ClickHouse integration/load test explicitly; do not claim performance targets without measuring on the deployment-sized dataset.

## Post-implementation measurement

Before declaring the latency issue fully resolved, run against representative ClickHouse data and record p50/p95 latency, rows/bytes scanned, peak memory, and result rows for 24-hour/7-day/30-day/90-day ranges, global and device-scoped, with trust/internal filters. If ClickHouse scan cost remains high, use the design spec's deduplicated canonical facts and rebuildable rollups as the next migration; do not restore Center-side full-result aggregation or merely raise limits.
