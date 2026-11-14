package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/network-observer/network-observer/internal/domain"
	"github.com/network-observer/network-observer/internal/enrichment"
)

//go:embed postgres.sql clickhouse.sql
var migrations embed.FS

type Store struct {
	Postgres         *pgxpool.Pool
	ClickHouse       *sql.DB
	ClickHouseNative clickhouse.Conn
	Enricher         *enrichment.Lookup
	dnsMu            sync.RWMutex
	dnsCache         map[string][]dnsCacheEntry
}

type dnsCacheEntry struct{ item domain.DNSObservation }

func Open(ctx context.Context, postgresDSN, clickhouseDSN string, retentionDays uint32) (*Store, error) {
	pg, err := pgxpool.New(ctx, postgresDSN)
	if err != nil {
		return nil, fmt.Errorf("configure PostgreSQL: %w", err)
	}
	if err := pg.Ping(ctx); err != nil {
		pg.Close()
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}

	chOptions, err := clickhouse.ParseDSN(clickhouseDSN)
	if err != nil {
		pg.Close()
		return nil, fmt.Errorf("parse ClickHouse DSN: %w", err)
	}
	ch := clickhouse.OpenDB(chOptions)
	ch.SetMaxOpenConns(10)
	ch.SetMaxIdleConns(5)
	ch.SetConnMaxLifetime(5 * time.Minute)
	if err := ch.PingContext(ctx); err != nil {
		ch.Close()
		pg.Close()
		return nil, fmt.Errorf("connect ClickHouse: %w", err)
	}
	chNative, err := clickhouse.Open(chOptions)
	if err != nil {
		ch.Close()
		pg.Close()
		return nil, fmt.Errorf("open ClickHouse connection: %w", err)
	}

	if err := runMigrations(ctx, pg, ch, retentionDays); err != nil {
		_ = chNative.Close()
		ch.Close()
		pg.Close()
		return nil, err
	}
	store := &Store{Postgres: pg, ClickHouse: ch, ClickHouseNative: chNative, Enricher: &enrichment.Lookup{}, dnsCache: make(map[string][]dnsCacheEntry)}
	if err := store.loadDNSCache(ctx); err != nil {
		store.Close()
		return nil, fmt.Errorf("load unexpired DNS relationships: %w", err)
	}
	return store, nil
}

func (s *Store) SetEnricher(lookup *enrichment.Lookup) {
	if lookup != nil {
		s.Enricher = lookup
	}
}

func (s *Store) Close() {
	if s.Enricher != nil {
		_ = s.Enricher.Close()
	}
	if s.ClickHouse != nil {
		_ = s.ClickHouse.Close()
	}
	if s.ClickHouseNative != nil {
		_ = s.ClickHouseNative.Close()
	}
	if s.Postgres != nil {
		s.Postgres.Close()
	}
}

func (s *Store) Ready(ctx context.Context) error {
	if s.Postgres == nil || s.ClickHouse == nil {
		return errors.New("storage is not initialized")
	}
	if err := s.Postgres.Ping(ctx); err != nil {
		return err
	}
	return s.ClickHouse.PingContext(ctx)
}

func runMigrations(ctx context.Context, pg *pgxpool.Pool, ch *sql.DB, retentionDays uint32) error {
	pgSQL, err := migrations.ReadFile("postgres.sql")
	if err != nil {
		return err
	}
	if _, err := pg.Exec(ctx, string(pgSQL)); err != nil {
		return fmt.Errorf("migrate PostgreSQL: %w", err)
	}

	chSQL, err := migrations.ReadFile("clickhouse.sql")
	if err != nil {
		return err
	}
	for _, statement := range migrationStatements(string(chSQL)) {
		if _, err := ch.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate ClickHouse: %w", err)
		}
	}
	if retentionDays != 90 {
		if _, err := ch.ExecContext(ctx, fmt.Sprintf("ALTER TABLE observer.traffic_minute MODIFY TTL bucket + INTERVAL %d DAY DELETE", retentionDays)); err != nil {
			return fmt.Errorf("configure traffic retention: %w", err)
		}
		if _, err := ch.ExecContext(ctx, fmt.Sprintf("ALTER TABLE observer.connection_presence_minute MODIFY TTL bucket + INTERVAL %d DAY DELETE", retentionDays)); err != nil {
			return fmt.Errorf("configure presence retention: %w", err)
		}
	}
	return nil
}

func migrationStatements(script string) []string {
	statements := make([]string, 0, 4)
	for _, statement := range splitStatements(script) {
		if statement = strings.TrimSpace(statement); statement != "" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func splitStatements(script string) []string {
	statements := make([]string, 0, 4)
	start := 0
	for i, r := range script {
		if r == ';' {
			statements = append(statements, script[start:i])
			start = i + 1
		}
	}
	if start < len(script) {
		statements = append(statements, script[start:])
	}
	return statements
}
