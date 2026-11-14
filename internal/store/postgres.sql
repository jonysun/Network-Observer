CREATE TABLE IF NOT EXISTS devices (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    observer_type TEXT NOT NULL CHECK (observer_type IN ('endpoint', 'gateway')),
    os TEXT NOT NULL DEFAULT '',
    architecture TEXT NOT NULL DEFAULT '',
    agent_version TEXT NOT NULL DEFAULT '',
    capabilities JSONB NOT NULL DEFAULT '[]'::jsonb,
    credential_hash BYTEA NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS admin_users (
    username TEXT PRIMARY KEY,
    password_hash BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS attribution_rules (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 100,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    matcher JSONB NOT NULL,
    attribution JSONB NOT NULL,
    effective_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS attribution_rules_enabled_priority_idx
    ON attribution_rules (enabled, priority, created_at);

CREATE TABLE IF NOT EXISTS attribution_overrides (
    id UUID PRIMARY KEY,
    connection_key TEXT NOT NULL,
    attribution JSONB NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    actor TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS attribution_overrides_connection_idx
    ON attribution_overrides (connection_key, created_at DESC);

CREATE TABLE IF NOT EXISTS audit_log (
    id BIGSERIAL PRIMARY KEY,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ingestion_checkpoints (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    last_sequence BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Durable, short-lived association state used to avoid counting an endpoint
-- and gateway view of the same flow twice. Rows are pruned during ingestion.
CREATE TABLE IF NOT EXISTS traffic_dedup_sightings (
    flow_key TEXT PRIMARY KEY,
    endpoint_seen_at TIMESTAMPTZ,
    endpoint_counted BOOLEAN NOT NULL DEFAULT FALSE,
    gateway_seen_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS traffic_dedup_sightings_updated_idx
    ON traffic_dedup_sightings (updated_at);

