CREATE TABLE IF NOT EXISTS devices (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    registered_name TEXT NOT NULL DEFAULT '',
    observer_type TEXT NOT NULL CHECK (observer_type IN ('endpoint', 'gateway')),
    os TEXT NOT NULL DEFAULT '',
    architecture TEXT NOT NULL DEFAULT '',
    agent_version TEXT NOT NULL DEFAULT '',
    capabilities JSONB NOT NULL DEFAULT '[]'::jsonb,
    device_category TEXT NOT NULL DEFAULT 'unknown',
    credential_hash BYTEA NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen TIMESTAMPTZ,
    dns_metadata_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    config_revision BIGINT NOT NULL DEFAULT 0
);

-- One-time enrollment credentials are stored only as SHA-256 digests. The
-- plaintext token is returned by the API once and is consumed atomically
-- with creation of the device credential and ingestion checkpoint.
CREATE TABLE IF NOT EXISTS agent_enrollment_tokens (
    id UUID PRIMARY KEY,
    token_hash BYTEA NOT NULL UNIQUE,
    observer_type TEXT NOT NULL CHECK (observer_type IN ('endpoint', 'gateway')),
    requested_name TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_by TEXT NOT NULL DEFAULT 'admin',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    dns_metadata_enabled BOOLEAN
);

CREATE INDEX IF NOT EXISTS agent_enrollment_tokens_active_idx
    ON agent_enrollment_tokens (expires_at, consumed_at, revoked_at);

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

CREATE TABLE IF NOT EXISTS project_todos (
    id UUID PRIMARY KEY,
    phase TEXT NOT NULL DEFAULT '未分类',
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'todo' CHECK (status IN ('todo', 'in_progress', 'done', 'blocked')),
    priority TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('low', 'medium', 'high')),
    progress INTEGER NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
    blocker TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'user' CHECK (source IN ('plan', 'user')),
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS project_todos_status_priority_idx
    ON project_todos (status, priority, updated_at DESC);

CREATE TABLE IF NOT EXISTS lan_neighbor_sightings (
    observer_device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    network_id TEXT NOT NULL,
    network_prefix TEXT NOT NULL DEFAULT '',
    ip_address TEXT NOT NULL,
    mac_address TEXT NOT NULL DEFAULT '',
    interface_name TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT '',
    is_local BOOLEAN NOT NULL DEFAULT FALSE,
    last_seen TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (observer_device_id, network_id, ip_address, mac_address, interface_name)
);

ALTER TABLE lan_neighbor_sightings ADD COLUMN IF NOT EXISTS network_prefix TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS lan_neighbor_sightings_network_idx
    ON lan_neighbor_sightings (network_id, mac_address, last_seen DESC);
CREATE INDEX IF NOT EXISTS lan_neighbor_sightings_ip_idx
    ON lan_neighbor_sightings (network_id, ip_address, last_seen DESC);

CREATE TABLE IF NOT EXISTS docker_network_sightings (
    observer_device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    docker_network_id TEXT NOT NULL,
    network_name TEXT NOT NULL DEFAULT '',
    driver TEXT NOT NULL DEFAULT '',
    scope TEXT NOT NULL DEFAULT '',
    subnet_cidrs TEXT[] NOT NULL DEFAULT '{}',
    containers JSONB NOT NULL DEFAULT '[]'::jsonb,
    last_seen TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (observer_device_id, docker_network_id)
);

CREATE INDEX IF NOT EXISTS docker_network_sightings_last_seen_idx
    ON docker_network_sightings (last_seen DESC);

CREATE TABLE IF NOT EXISTS lan_topology_layout (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    layout JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_by TEXT NOT NULL DEFAULT 'system',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO lan_topology_layout (singleton) VALUES (TRUE) ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS lan_topology_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    snapshot_interval_minutes INTEGER NOT NULL DEFAULT 5 CHECK (snapshot_interval_minutes BETWEEN 1 AND 1440),
    retention_days INTEGER NOT NULL DEFAULT 7 CHECK (retention_days BETWEEN 1 AND 365),
    updated_by TEXT NOT NULL DEFAULT 'system',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO lan_topology_settings (singleton) VALUES (TRUE) ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS lan_topology_snapshots (
    id BIGSERIAL PRIMARY KEY,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    topology JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS lan_topology_snapshots_captured_idx
    ON lan_topology_snapshots (captured_at DESC);

CREATE TABLE IF NOT EXISTS lan_device_aliases (
    network_id TEXT NOT NULL,
    mac_address TEXT NOT NULL,
    alias TEXT NOT NULL,
    device_category TEXT NOT NULL DEFAULT 'unknown',
    updated_by TEXT NOT NULL DEFAULT 'admin',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (network_id, mac_address)
);

ALTER TABLE devices ADD COLUMN IF NOT EXISTS device_category TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE devices ADD COLUMN IF NOT EXISTS registered_name TEXT NOT NULL DEFAULT '';
UPDATE devices SET registered_name = name WHERE registered_name = '';
ALTER TABLE devices ADD COLUMN IF NOT EXISTS latitude DOUBLE PRECISION;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS longitude DOUBLE PRECISION;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS dns_metadata_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS config_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_enrollment_tokens ADD COLUMN IF NOT EXISTS dns_metadata_enabled BOOLEAN;
ALTER TABLE lan_device_aliases ADD COLUMN IF NOT EXISTS device_category TEXT NOT NULL DEFAULT 'unknown';

CREATE TABLE IF NOT EXISTS ip_metadata_overrides (
    ip_address INET PRIMARY KEY,
    domain TEXT,
    asn BIGINT,
    organization TEXT,
    country TEXT,
    region TEXT,
    city TEXT,
    latitude DOUBLE PRECISION,
    longitude DOUBLE PRECISION,
    trusted BOOLEAN NOT NULL DEFAULT FALSE,
    updated_by TEXT NOT NULL DEFAULT 'admin',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (asn IS NULL OR (asn > 0 AND asn <= 4294967295)),
    CHECK (latitude IS NULL OR (latitude >= -90 AND latitude <= 90)),
    CHECK (longitude IS NULL OR (longitude >= -180 AND longitude <= 180)),
    CHECK ((latitude IS NULL) = (longitude IS NULL))
);

CREATE TABLE IF NOT EXISTS ip_ptr_cache (
    ip_address INET PRIMARY KEY,
    hostname TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('matched', 'no_record', 'error')),
    reason TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ip_ptr_cache_expiry_idx
    ON ip_ptr_cache (expires_at);

