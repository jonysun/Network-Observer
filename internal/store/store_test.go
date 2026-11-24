package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSplitStatementsIgnoresTrailingWhitespace(t *testing.T) {
	statements := migrationStatements("CREATE DATABASE observer;\n\nCREATE TABLE sample (id UInt8);\n")
	if len(statements) != 2 {
		t.Fatalf("expected two SQL statements, got %d", len(statements))
	}
	if statements[0] != "CREATE DATABASE observer" {
		t.Fatalf("unexpected first statement: %q", statements[0])
	}
	if statements[1] != "CREATE TABLE sample (id UInt8)" {
		t.Fatalf("unexpected second statement: %q", statements[1])
	}
}

func TestCreateEnrollmentTokenPersistsTokenAndAudit(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()

	schemaID, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "observer_test_" + strings.ReplaceAll(schemaID, "-", "")
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer adminPool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx, `
		CREATE TABLE agent_enrollment_tokens (
			id UUID PRIMARY KEY,
			token_hash BYTEA NOT NULL,
			observer_type TEXT NOT NULL,
			requested_name TEXT NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			created_by TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			dns_metadata_enabled BOOLEAN
		)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE TABLE audit_log (
			actor TEXT NOT NULL,
			action TEXT NOT NULL,
			object_type TEXT NOT NULL,
			object_id TEXT NOT NULL,
			request_id TEXT NOT NULL,
			detail JSONB NOT NULL
		)`)
	if err != nil {
		t.Fatal(err)
	}

	const plaintext = "one-time-enrollment-token"
	store := &Store{Postgres: pool}
	dnsEnabled := true
	record, err := store.CreateEnrollmentToken(ctx, "endpoint", "lab-node", "admin", "request-1", time.Now().Add(time.Hour), plaintext, &dnsEnabled)
	if err != nil {
		t.Fatalf("create enrollment token: %v", err)
	}
	if record.ID == "" || record.ObserverType != "endpoint" || record.RequestedName != "lab-node" || record.DNSMetadataEnabled == nil || !*record.DNSMetadataEnabled {
		t.Fatalf("unexpected enrollment token record: %+v", record)
	}

	wantHash := sha256.Sum256([]byte(plaintext))
	var savedHash []byte
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM agent_enrollment_tokens WHERE id = $1`, record.ID).Scan(&savedHash); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(savedHash, wantHash[:]) {
		t.Fatal("enrollment token digest was not persisted")
	}

	var actor, action, objectType, objectID, requestID, observerType string
	if err := pool.QueryRow(ctx, `
		SELECT actor, action, object_type, object_id, request_id, detail->>'observer_type'
		FROM audit_log`).Scan(&actor, &action, &objectType, &objectID, &requestID, &observerType); err != nil {
		t.Fatal(err)
	}
	if actor != "admin" || action != "create" || objectType != "agent_enrollment_token" || objectID != record.ID || requestID != "request-1" || observerType != "endpoint" {
		t.Fatalf("unexpected audit record: actor=%q action=%q type=%q id=%q request=%q observer_type=%q", actor, action, objectType, objectID, requestID, observerType)
	}
}
