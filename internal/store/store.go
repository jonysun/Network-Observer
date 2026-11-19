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
	dedupPruneMu     sync.Mutex
	dedupPrunedAt    time.Time
	ptrMu            sync.Mutex
	ptrPending       map[string]bool
	ptrQueue         chan string
	ptrContext       context.Context
	ptrCancel        context.CancelFunc
	ptrWorkers       sync.WaitGroup
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
	ptrContext, ptrCancel := context.WithCancel(context.Background())
	store := &Store{
		Postgres: pg, ClickHouse: ch, ClickHouseNative: chNative, Enricher: &enrichment.Lookup{},
		dnsCache: make(map[string][]dnsCacheEntry), ptrPending: make(map[string]bool), ptrQueue: make(chan string, 128),
		ptrContext: ptrContext, ptrCancel: ptrCancel,
	}
	for range 2 {
		store.ptrWorkers.Add(1)
		go store.runPTRWorker()
	}
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
	if s.ptrCancel != nil {
		s.ptrCancel()
		s.ptrWorkers.Wait()
	}
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
	if err := migratePresenceReplacingMergeTree(ctx, ch); err != nil {
		return fmt.Errorf("migrate connection presence engine: %w", err)
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

func migratePresenceReplacingMergeTree(ctx context.Context, ch *sql.DB) error {
	var engine string
	err := ch.QueryRowContext(ctx, `SELECT engine FROM system.tables WHERE database = 'observer' AND name = 'connection_presence_minute'`).Scan(&engine)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.HasPrefix(strings.ToLower(engine), "replacingmergetree") {
		return nil
	}
	if !strings.EqualFold(engine, "MergeTree") {
		return fmt.Errorf("unsupported existing engine %q", engine)
	}

	// Existing v0.3.x deployments used MergeTree, so repeated cumulative rows
	// were never collapsed. Copy the table before swapping names and retain the
	// original as a rollback backup. The temporary table is safe to recreate if
	// a restart occurs before the rename.
	temporary := "observer.connection_presence_minute_replacing"
	if _, err := ch.ExecContext(ctx, "DROP TABLE IF EXISTS "+temporary); err != nil {
		return err
	}
	createSQL := `CREATE TABLE ` + temporary + ` (
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
	TTL bucket + INTERVAL 90 DAY DELETE`
	if _, err := ch.ExecContext(ctx, createSQL); err != nil {
		return err
	}
	columns := "event_key, bucket, device_id, flow_id, protocol, source_ip, source_port, destination_ip, destination_port, domain, first_seen, last_seen, observed_minutes, active_seconds, upload_bytes, download_bytes, asn, organization, country, region, city, estimated, gap_reason, interface, local_port, remote_ip, process_name, container_name, ranking_dimensions_available, version"
	if _, err := ch.ExecContext(ctx, "INSERT INTO "+temporary+" ("+columns+") SELECT "+columns+" FROM observer.connection_presence_minute"); err != nil {
		return err
	}
	backup := fmt.Sprintf("observer.connection_presence_minute_legacy_%d", time.Now().UTC().UnixNano())
	if _, err := ch.ExecContext(ctx, "RENAME TABLE observer.connection_presence_minute TO "+backup+", "+temporary+" TO observer.connection_presence_minute"); err != nil {
		return err
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
