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
    version DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(bucket)
ORDER BY (event_key, dimension_type, dimension_value, direction)
TTL bucket + INTERVAL 90 DAY DELETE;

ALTER TABLE observer.traffic_minute ADD COLUMN IF NOT EXISTS estimated UInt8 DEFAULT 0;

