package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

type RuleRecord struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Priority      int             `json:"priority"`
	Enabled       bool            `json:"enabled"`
	Matcher       json.RawMessage `json:"matcher"`
	Attribution   json.RawMessage `json:"attribution"`
	EffectiveFrom time.Time       `json:"effective_from"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type EnrollmentToken struct {
	ID            string     `json:"id"`
	ObserverType  string     `json:"observer_type"`
	RequestedName string     `json:"requested_name"`
	ExpiresAt     time.Time  `json:"expires_at"`
	CreatedAt     time.Time  `json:"created_at"`
	ConsumedAt    *time.Time `json:"consumed_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
}

func (s *Store) BootstrapAdmin(ctx context.Context, username string, passwordHash []byte) error {
	_, err := s.Postgres.Exec(ctx,
		`INSERT INTO admin_users (username, password_hash) VALUES ($1, $2) ON CONFLICT (username) DO NOTHING`,
		username, passwordHash,
	)
	return err
}

func (s *Store) AdminPasswordHash(ctx context.Context, username string) ([]byte, error) {
	var hash []byte
	err := s.Postgres.QueryRow(ctx, `SELECT password_hash FROM admin_users WHERE username = $1`, username).Scan(&hash)
	return hash, err
}

func (s *Store) RegisterDevice(ctx context.Context, name string, observerType domain.ObserverType, osName, arch, agentVersion string, credential string) (string, error) {
	id, err := newUUID()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(credential))
	_, err = s.Postgres.Exec(ctx, `
		INSERT INTO devices (id, name, observer_type, os, architecture, agent_version, credential_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, name, observerType, osName, arch, agentVersion, hash[:])
	if err != nil {
		return "", err
	}
	_, err = s.Postgres.Exec(ctx, `INSERT INTO ingestion_checkpoints (device_id) VALUES ($1)`, id)
	if err != nil {
		return "", err
	}
	return id, nil
}

// CreateEnrollmentToken persists only a digest. The caller must return the
// plaintext value immediately; it cannot be recovered from PostgreSQL.
func (s *Store) CreateEnrollmentToken(ctx context.Context, observerType, requestedName, createdBy string, expiresAt time.Time, token string) (EnrollmentToken, error) {
	id, err := newUUID()
	if err != nil {
		return EnrollmentToken{}, err
	}
	hash := sha256.Sum256([]byte(token))
	tx, err := s.Postgres.Begin(ctx)
	if err != nil {
		return EnrollmentToken{}, err
	}
	defer tx.Rollback(ctx)
	var record EnrollmentToken
	err = tx.QueryRow(ctx, `
		INSERT INTO agent_enrollment_tokens (id, token_hash, observer_type, requested_name, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text, observer_type, requested_name, expires_at, created_at`,
		id, hash[:], observerType, requestedName, expiresAt, createdBy).
		Scan(&record.ID, &record.ObserverType, &record.RequestedName, &record.ExpiresAt, &record.CreatedAt)
	if err != nil {
		return EnrollmentToken{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, object_type, object_id, request_id, detail) VALUES ($1, 'create', 'agent_enrollment_token', $2, 'enrollment', jsonb_build_object('observer_type', $3))`, createdBy, record.ID, observerType); err != nil {
		return EnrollmentToken{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EnrollmentToken{}, err
	}
	return record, nil
}

func (s *Store) RevokeEnrollmentToken(ctx context.Context, id, actor, requestID string) error {
	tx, err := s.Postgres.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE agent_enrollment_tokens SET revoked_at = now() WHERE id = $1 AND consumed_at IS NULL AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("enrollment token not found or already consumed")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, object_type, object_id, request_id) VALUES ($1, 'revoke', 'agent_enrollment_token', $2, $3)`, actor, id, requestID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ConsumeEnrollmentTokenAndRegisterDevice locks and consumes a token in the
// same transaction that creates the device and its checkpoint. This prevents
// concurrent registration attempts from using one token twice.
func (s *Store) ConsumeEnrollmentTokenAndRegisterDevice(ctx context.Context, token, name string, observerType domain.ObserverType, osName, arch, agentVersion, credential string) (string, error) {
	hash := sha256.Sum256([]byte(token))
	tx, err := s.Postgres.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var tokenID, tokenType, requestedName string
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT id::text, observer_type, requested_name, expires_at
		FROM agent_enrollment_tokens
		WHERE token_hash = $1
		FOR UPDATE`, hash[:]).Scan(&tokenID, &tokenType, &requestedName, &expiresAt)
	if err != nil {
		return "", fmt.Errorf("invalid enrollment token")
	}
	if tokenType != string(observerType) {
		return "", fmt.Errorf("enrollment token observer type mismatch")
	}
	if !expiresAt.After(time.Now().UTC()) {
		return "", fmt.Errorf("enrollment token expired")
	}
	var consumedAt, revokedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT consumed_at, revoked_at FROM agent_enrollment_tokens WHERE id = $1`, tokenID).Scan(&consumedAt, &revokedAt); err != nil {
		return "", err
	}
	if consumedAt != nil {
		return "", fmt.Errorf("enrollment token already consumed")
	}
	if revokedAt != nil {
		return "", fmt.Errorf("enrollment token revoked")
	}
	if strings.TrimSpace(name) == "" {
		name = requestedName
	}
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("device name is required")
	}
	deviceID, err := newUUID()
	if err != nil {
		return "", err
	}
	credentialHash := sha256.Sum256([]byte(credential))
	if _, err := tx.Exec(ctx, `
		INSERT INTO devices (id, name, observer_type, os, architecture, agent_version, credential_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, deviceID, name, observerType, osName, arch, agentVersion, credentialHash[:]); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ingestion_checkpoints (device_id) VALUES ($1)`, deviceID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_enrollment_tokens SET consumed_at = now() WHERE id = $1`, tokenID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return deviceID, nil
}

func (s *Store) ListEnrollmentTokens(ctx context.Context) ([]EnrollmentToken, error) {
	rows, err := s.Postgres.Query(ctx, `
		SELECT id::text, observer_type, requested_name, expires_at, consumed_at, revoked_at, created_at
		FROM agent_enrollment_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]EnrollmentToken, 0)
	for rows.Next() {
		var item EnrollmentToken
		if err := rows.Scan(&item.ID, &item.ObserverType, &item.RequestedName, &item.ExpiresAt, &item.ConsumedAt, &item.RevokedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) AuthenticateDevice(ctx context.Context, id, credential string) (uint64, error) {
	hash := sha256.Sum256([]byte(credential))
	var saved []byte
	var lastSequence int64
	err := s.Postgres.QueryRow(ctx, `
		SELECT d.credential_hash, c.last_sequence
		FROM devices d
		JOIN ingestion_checkpoints c ON c.device_id = d.id
		WHERE d.id = $1 AND d.revoked_at IS NULL`, id).Scan(&saved, &lastSequence)
	if err != nil {
		return 0, err
	}
	if !equalBytes(saved, hash[:]) {
		return 0, fmt.Errorf("invalid device credential")
	}
	if lastSequence < 0 {
		return 0, fmt.Errorf("invalid ingestion checkpoint")
	}
	return uint64(lastSequence), nil
}

func (s *Store) ActiveDeviceCheckpoint(ctx context.Context, id string) (uint64, error) {
	var lastSequence int64
	err := s.Postgres.QueryRow(ctx, `
		SELECT c.last_sequence
		FROM devices d
		JOIN ingestion_checkpoints c ON c.device_id = d.id
		WHERE d.id = $1 AND d.revoked_at IS NULL`, id).Scan(&lastSequence)
	if err != nil {
		return 0, err
	}
	if lastSequence < 0 {
		return 0, fmt.Errorf("invalid ingestion checkpoint")
	}
	return uint64(lastSequence), nil
}

func (s *Store) TouchDevice(ctx context.Context, id string) error {
	result, err := s.Postgres.Exec(ctx, `UPDATE devices SET last_seen = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("device credential revoked")
	}
	return nil
}

func (s *Store) UpdateCapabilities(ctx context.Context, id string, capabilities []domain.Capability) error {
	data, err := json.Marshal(capabilities)
	if err != nil {
		return err
	}
	result, err := s.Postgres.Exec(ctx, `UPDATE devices SET capabilities = $2::jsonb, last_seen = now() WHERE id = $1 AND revoked_at IS NULL`, id, data)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("device credential revoked")
	}
	return nil
}

func (s *Store) ListDevices(ctx context.Context) ([]domain.Device, error) {
	rows, err := s.Postgres.Query(ctx, `
		SELECT id::text, name, observer_type, os, architecture, agent_version,
		       capabilities, COALESCE(last_seen, created_at)
		FROM devices WHERE revoked_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	devices := make([]domain.Device, 0)
	for rows.Next() {
		var item domain.Device
		var raw []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.Type, &item.OS, &item.Architecture, &item.AgentVersion, &raw, &item.LastSeen); err != nil {
			return nil, err
		}
		item.Online = time.Since(item.LastSeen) < 45*time.Second
		if err := json.Unmarshal(raw, &item.Capabilities); err != nil {
			return nil, err
		}
		devices = append(devices, item)
	}
	return devices, rows.Err()
}

func (s *Store) RevokeDevice(ctx context.Context, id, actor, requestID string) error {
	tx, err := s.Postgres.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE devices SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("device not found")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, object_type, object_id, request_id) VALUES ($1, 'revoke', 'device_credential', $2, $3)`, actor, id, requestID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CreateRule(ctx context.Context, record RuleRecord, actor, requestID string) (RuleRecord, error) {
	if record.ID == "" {
		id, err := newUUID()
		if err != nil {
			return record, err
		}
		record.ID = id
	}
	if record.EffectiveFrom.IsZero() {
		record.EffectiveFrom = time.Now().UTC()
	}
	_, err := s.Postgres.Exec(ctx, `
		INSERT INTO attribution_rules (id, name, priority, enabled, matcher, attribution, effective_from)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7)`,
		record.ID, record.Name, record.Priority, record.Enabled, record.Matcher, record.Attribution, record.EffectiveFrom)
	if err != nil {
		return record, err
	}
	if err := s.audit(ctx, actor, "create", "attribution_rule", record.ID, requestID, nil); err != nil {
		return record, err
	}
	return record, nil
}

func (s *Store) UpdateRule(ctx context.Context, record RuleRecord, actor, requestID string) error {
	result, err := s.Postgres.Exec(ctx, `
		UPDATE attribution_rules SET name = $2, priority = $3, enabled = $4,
		       matcher = $5::jsonb, attribution = $6::jsonb, effective_from = now(), updated_at = now()
		WHERE id = $1`, record.ID, record.Name, record.Priority, record.Enabled, record.Matcher, record.Attribution)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("attribution rule not found")
	}
	return s.audit(ctx, actor, "update", "attribution_rule", record.ID, requestID, nil)
}

func (s *Store) DeleteRule(ctx context.Context, id, actor, requestID string) error {
	result, err := s.Postgres.Exec(ctx, `DELETE FROM attribution_rules WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("attribution rule not found")
	}
	return s.audit(ctx, actor, "delete", "attribution_rule", id, requestID, nil)
}

func (s *Store) ListRules(ctx context.Context) ([]RuleRecord, error) {
	rows, err := s.Postgres.Query(ctx, `
		SELECT id::text, name, priority, enabled, matcher, attribution, effective_from, created_at, updated_at
		FROM attribution_rules ORDER BY priority, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]RuleRecord, 0)
	for rows.Next() {
		var item RuleRecord
		if err := rows.Scan(&item.ID, &item.Name, &item.Priority, &item.Enabled, &item.Matcher, &item.Attribution, &item.EffectiveFrom, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) CreateOverride(ctx context.Context, connectionKey string, attribution json.RawMessage, reason, actor, requestID string) (string, error) {
	id, err := newUUID()
	if err != nil {
		return "", err
	}
	_, err = s.Postgres.Exec(ctx, `
		INSERT INTO attribution_overrides (id, connection_key, attribution, reason, actor)
		VALUES ($1, $2, $3::jsonb, $4, $5)`, id, connectionKey, attribution, reason, actor)
	if err != nil {
		return "", err
	}
	if err := s.audit(ctx, actor, "create", "attribution_override", id, requestID, map[string]string{"connection_key": connectionKey}); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) DeleteOverride(ctx context.Context, id, actor, requestID string) (string, error) {
	tx, err := s.Postgres.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var connectionKey string
	if err := tx.QueryRow(ctx, `DELETE FROM attribution_overrides WHERE id = $1 RETURNING connection_key`, id).Scan(&connectionKey); err != nil {
		return "", fmt.Errorf("attribution override not found: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, object_type, object_id, request_id) VALUES ($1, 'delete', 'attribution_override', $2, $3)`, actor, id, requestID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return connectionKey, nil
}

func (s *Store) ActiveOverrides(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.Postgres.Query(ctx, `
		SELECT DISTINCT ON (connection_key) connection_key, attribution
		FROM attribution_overrides WHERE expires_at IS NULL OR expires_at > now()
		ORDER BY connection_key, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]json.RawMessage)
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, rows.Err()
}

func (s *Store) audit(ctx context.Context, actor, action, objectType, objectID, requestID string, detail any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	data, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = s.Postgres.Exec(ctx, `
		INSERT INTO audit_log (actor, action, object_type, object_id, request_id, detail)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)`, actor, action, objectType, objectID, requestID, data)
	return err
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[0:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" + hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:16]), nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
