package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/network-observer/network-observer/internal/domain"
)

func (s *Store) IngestDockerNetworkSnapshot(ctx context.Context, observerID string, networks []domain.DockerNetwork, complete bool, observedAt time.Time) error {
	tx, err := s.Postgres.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if complete {
		if _, err := tx.Exec(ctx, `DELETE FROM docker_network_sightings WHERE observer_device_id = $1`, observerID); err != nil {
			return err
		}
	}
	for _, item := range networks {
		containers, err := json.Marshal(item.Containers)
		if err != nil {
			return fmt.Errorf("encode Docker network containers: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO docker_network_sightings (
				observer_device_id, docker_network_id, network_name, driver, scope, subnet_cidrs, containers, last_seen
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (observer_device_id, docker_network_id) DO UPDATE SET
				network_name = EXCLUDED.network_name, driver = EXCLUDED.driver, scope = EXCLUDED.scope,
				subnet_cidrs = EXCLUDED.subnet_cidrs, containers = EXCLUDED.containers, last_seen = EXCLUDED.last_seen`,
			observerID, item.ID, item.Name, item.Driver, item.Scope, item.SubnetCIDRs, containers, observedAt)
		if err != nil {
			return fmt.Errorf("upsert Docker network %s: %w", item.ID, err)
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListDockerNetworks(ctx context.Context) ([]domain.DockerNetwork, error) {
	rows, err := s.Postgres.Query(ctx, `
		SELECT n.observer_device_id::text, COALESCE(d.name, ''), n.docker_network_id,
		       n.network_name, n.driver, n.scope, n.subnet_cidrs, n.containers, n.last_seen
		FROM docker_network_sightings AS n
		JOIN devices AS d ON d.id = n.observer_device_id AND d.revoked_at IS NULL
		WHERE n.last_seen >= now() - interval '30 days'
		ORDER BY d.name, n.network_name, n.docker_network_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.DockerNetwork, 0)
	for rows.Next() {
		var item domain.DockerNetwork
		var containers []byte
		if err := rows.Scan(&item.OwnerAgentID, &item.OwnerAgentName, &item.ID, &item.Name, &item.Driver, &item.Scope, &item.SubnetCIDRs, &containers, &item.LastSeen); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(containers, &item.Containers); err != nil {
			return nil, fmt.Errorf("decode Docker network %s containers: %w", item.ID, err)
		}
		if item.SubnetCIDRs == nil {
			item.SubnetCIDRs = []string{}
		}
		if item.Containers == nil {
			item.Containers = []domain.DockerContainer{}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) LANTopologyLayout(ctx context.Context) (domain.LANTopologyLayout, error) {
	var raw []byte
	if err := s.Postgres.QueryRow(ctx, `SELECT layout FROM lan_topology_layout WHERE singleton = TRUE`).Scan(&raw); err != nil {
		return domain.LANTopologyLayout{}, err
	}
	var layout domain.LANTopologyLayout
	if err := json.Unmarshal(raw, &layout); err != nil {
		return domain.LANTopologyLayout{}, err
	}
	if layout.Positions == nil {
		layout.Positions = make(map[string]domain.LANTopologyPoint)
	}
	if layout.Infrastructure == nil {
		layout.Infrastructure = []domain.LANTopologyInfrastructure{}
	}
	if layout.ConfirmedLinks == nil {
		layout.ConfirmedLinks = []domain.LANTopologyLink{}
	}
	return layout, nil
}

func (s *Store) SaveLANTopologyLayout(ctx context.Context, layout domain.LANTopologyLayout, actor string) error {
	raw, err := json.Marshal(layout)
	if err != nil {
		return err
	}
	_, err = s.Postgres.Exec(ctx, `UPDATE lan_topology_layout SET layout = $1, updated_by = $2, updated_at = now() WHERE singleton = TRUE`, raw, actor)
	return err
}

func (s *Store) LANTopologySettings(ctx context.Context) (domain.LANTopologySettings, error) {
	var settings domain.LANTopologySettings
	err := s.Postgres.QueryRow(ctx, `SELECT snapshot_interval_minutes, retention_days FROM lan_topology_settings WHERE singleton = TRUE`).Scan(&settings.SnapshotIntervalMinutes, &settings.RetentionDays)
	return settings, err
}

func (s *Store) SaveLANTopologySettings(ctx context.Context, settings domain.LANTopologySettings, actor string) error {
	_, err := s.Postgres.Exec(ctx, `UPDATE lan_topology_settings SET snapshot_interval_minutes = $1, retention_days = $2, updated_by = $3, updated_at = now() WHERE singleton = TRUE`, settings.SnapshotIntervalMinutes, settings.RetentionDays, actor)
	return err
}

func (s *Store) CreateLANTopologySnapshotIfDue(ctx context.Context, topology json.RawMessage) (*domain.LANTopologySnapshot, error) {
	tx, err := s.Postgres.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(746219381)`); err != nil {
		return nil, err
	}
	var intervalMinutes, retentionDays int
	if err := tx.QueryRow(ctx, `SELECT snapshot_interval_minutes, retention_days FROM lan_topology_settings WHERE singleton = TRUE`).Scan(&intervalMinutes, &retentionDays); err != nil {
		return nil, err
	}
	var lastCaptured pgtype.Timestamptz
	err = tx.QueryRow(ctx, `SELECT captured_at FROM lan_topology_snapshots ORDER BY captured_at DESC LIMIT 1`).Scan(&lastCaptured)
	if err == nil && lastCaptured.Valid && time.Since(lastCaptured.Time) < time.Duration(intervalMinutes)*time.Minute {
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	var snapshot domain.LANTopologySnapshot
	err = tx.QueryRow(ctx, `INSERT INTO lan_topology_snapshots (topology) VALUES ($1) RETURNING id, captured_at`, topology).Scan(&snapshot.ID, &snapshot.CapturedAt)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM lan_topology_snapshots WHERE captured_at < now() - ($1 * interval '1 day')`, retentionDays); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *Store) ListLANTopologySnapshots(ctx context.Context, limit int) ([]domain.LANTopologySnapshot, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.Postgres.Query(ctx, `SELECT id, captured_at FROM lan_topology_snapshots ORDER BY captured_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.LANTopologySnapshot, 0)
	for rows.Next() {
		var item domain.LANTopologySnapshot
		if err := rows.Scan(&item.ID, &item.CapturedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) LANTopologySnapshot(ctx context.Context, id int64) (json.RawMessage, domain.LANTopologySnapshot, error) {
	var raw []byte
	var snapshot domain.LANTopologySnapshot
	err := s.Postgres.QueryRow(ctx, `SELECT id, captured_at, topology FROM lan_topology_snapshots WHERE id = $1`, id).Scan(&snapshot.ID, &snapshot.CapturedAt, &raw)
	return json.RawMessage(raw), snapshot, err
}
