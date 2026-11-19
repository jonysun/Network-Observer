CREATE DATABASE IF NOT EXISTS observer;

CREATE TABLE IF NOT EXISTS observer.live_connections (
    device_id UUID,
    flow_id String,
    observer_type LowCardinality(String),
    observer_id String,
    local_is_source UInt8,
    observed_at DateTime64(3, 'UTC'),
    interface String,
    network_namespace String,
    protocol LowCardinality(String),
    state String,
    source_ip String,
    source_port UInt16,
    destination_ip String,
    destination_port UInt16,
    nat_source_ip String,
    nat_source_port UInt16,
    nat_destination_ip String,
    nat_destination_port UInt16,
    bytes_source_to_dest UInt64,
    bytes_dest_to_source UInt64,
    rate_bytes_source_to_dest UInt64,
    rate_bytes_dest_to_source UInt64,
    pid UInt32,
    process_name String,
    container_id String,
    container_name String,
    service_name String,
    attribution_source LowCardinality(String),
    attribution_reason String,
    sampling_rate UInt32,
    counter_source LowCardinality(String),
    confidence LowCardinality(String),
    domain String,
    domain_source LowCardinality(String),
    asn UInt32,
    organization String,
    country LowCardinality(String),
    region String,
    city String,
    latitude Float64,
    longitude Float64,
    enrichment_version String,
    gap_reason String,
    active UInt8,
    version DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(version)
PARTITION BY toDate(observed_at)
ORDER BY (device_id, flow_id, observer_id)
TTL observed_at + INTERVAL 15 MINUTE DELETE;

CREATE TABLE IF NOT EXISTS observer.traffic_minute (
    event_key String,
    bucket DateTime('UTC'),
    dimension_type LowCardinality(String),
    dimension_value String,
    device_id UUID,
    interface String,
    protocol LowCardinality(String),
    direction LowCardinality(String),
    bytes UInt64,
    packets UInt64,
    estimated UInt8 DEFAULT 0,
    source_ip String DEFAULT '',
    destination_ip String DEFAULT '',
    local_port UInt16 DEFAULT 0,
    remote_port UInt16 DEFAULT 0,
    domain String DEFAULT '',
    asn UInt32 DEFAULT 0,
    organization String DEFAULT '',
    country LowCardinality(String) DEFAULT '',
    region String DEFAULT '',
    city String DEFAULT '',
    process_name String DEFAULT '',
    container_name String DEFAULT '',
    confidence LowCardinality(String) DEFAULT '',
    gap_reason String DEFAULT '',
    remote_ip String DEFAULT '',
    version DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(bucket)
ORDER BY (event_key, dimension_type, dimension_value, direction)
TTL bucket + INTERVAL 90 DAY DELETE;

ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS estimated UInt8 DEFAULT 0;
ALTER TABLE observer.live_connections ADD COLUMN IF NOT EXISTS domain String DEFAULT '';
ALTER TABLE observer.live_connections ADD COLUMN IF NOT EXISTS domain_source LowCardinality(String) DEFAULT '';
ALTER TABLE observer.live_connections ADD COLUMN IF NOT EXISTS asn UInt32 DEFAULT 0;
ALTER TABLE observer.live_connections ADD COLUMN IF NOT EXISTS organization String DEFAULT '';
ALTER TABLE observer.live_connections ADD COLUMN IF NOT EXISTS country LowCardinality(String) DEFAULT '';
ALTER TABLE observer.live_connections ADD COLUMN IF NOT EXISTS region String DEFAULT '';
ALTER TABLE observer.live_connections ADD COLUMN IF NOT EXISTS city String DEFAULT '';
ALTER TABLE observer.live_connections ADD COLUMN IF NOT EXISTS latitude Float64 DEFAULT 0;
ALTER TABLE observer.live_connections ADD COLUMN IF NOT EXISTS longitude Float64 DEFAULT 0;
ALTER TABLE observer.live_connections ADD COLUMN IF NOT EXISTS enrichment_version String DEFAULT '';
ALTER TABLE observer.live_connections ADD COLUMN IF NOT EXISTS gap_reason String DEFAULT '';
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS source_ip String DEFAULT '';
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS destination_ip String DEFAULT '';
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS local_port UInt16 DEFAULT 0;
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS remote_port UInt16 DEFAULT 0;
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS domain String DEFAULT '';
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS asn UInt32 DEFAULT 0;
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS organization String DEFAULT '';
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS country LowCardinality(String) DEFAULT '';
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS region String DEFAULT '';
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS city String DEFAULT '';
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS process_name String DEFAULT '';
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS container_name String DEFAULT '';
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS confidence LowCardinality(String) DEFAULT '';
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS gap_reason String DEFAULT '';
ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS remote_ip String DEFAULT '';

CREATE TABLE IF NOT EXISTS observer.connection_presence_minute (
    event_key String,
    bucket DateTime('UTC'),
    device_id UUID,
    flow_id String,
    protocol LowCardinality(String),
    source_ip String,
    source_port UInt16,
    destination_ip String,
    destination_port UInt16,
    domain String,
    first_seen DateTime64(3, 'UTC'),
    last_seen DateTime64(3, 'UTC'),
    observed_minutes UInt32,
    active_seconds UInt32,
    upload_bytes UInt64,
    download_bytes UInt64,
    asn UInt32,
    organization String,
    country LowCardinality(String),
    region String,
    city String,
    estimated UInt8,
    gap_reason String,
    interface String DEFAULT '',
    local_port UInt16 DEFAULT 0,
    remote_ip String DEFAULT '',
    process_name String DEFAULT '',
    container_name String DEFAULT '',
    ranking_dimensions_available UInt8 DEFAULT 0,
    version DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(bucket)
ORDER BY (device_id, bucket, event_key)
TTL bucket + INTERVAL 90 DAY DELETE;

ALTER TABLE observer.connection_presence_minute ADD COLUMN IF NOT EXISTS interface String DEFAULT '';
ALTER TABLE observer.connection_presence_minute ADD COLUMN IF NOT EXISTS local_port UInt16 DEFAULT 0;
ALTER TABLE observer.connection_presence_minute ADD COLUMN IF NOT EXISTS remote_ip String DEFAULT '';
ALTER TABLE observer.connection_presence_minute ADD COLUMN IF NOT EXISTS process_name String DEFAULT '';
ALTER TABLE observer.connection_presence_minute ADD COLUMN IF NOT EXISTS container_name String DEFAULT '';
ALTER TABLE observer.connection_presence_minute ADD COLUMN IF NOT EXISTS ranking_dimensions_available UInt8 DEFAULT 0;

CREATE TABLE IF NOT EXISTS observer.dns_observations (
    device_id UUID,
    observed_at DateTime64(3, 'UTC'),
    interface String,
    domain String,
    ip String,
    ttl UInt32,
    source LowCardinality(String),
    expires_at DateTime64(3, 'UTC'),
    version DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(version)
PARTITION BY toDate(observed_at)
ORDER BY (device_id, ip, domain, observed_at, interface)
TTL observed_at + INTERVAL 7 DAY DELETE;

