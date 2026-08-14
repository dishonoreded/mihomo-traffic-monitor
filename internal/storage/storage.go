package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/traffic"
	_ "modernc.org/sqlite"
)

const currentSchemaVersion = 2

type Store struct {
	database *sql.DB
	path     string
}

type Info struct {
	Healthy       bool
	Error         string
	JournalMode   string
	SchemaVersion int
	SizeBytes     int64
}

type AttributionTotals struct {
	Observed     int64 `json:"observed"`
	Residual     int64 `json:"residual"`
	GapRecovered int64 `json:"gapRecovered"`
	Total        int64 `json:"total"`
}

type Leader struct {
	Name     string `json:"name"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
	Total    int64  `json:"total"`
}

type TrafficSummary struct {
	Upload   AttributionTotals
	Download AttributionTotals
	Total    AttributionTotals
	Coverage float64
	Apps     []Leader
	Hosts    []Leader
}

type leaderQuery struct {
	name      string
	statement string
}

var appLeaders = leaderQuery{
	name: "App",
	statement: `
		SELECT dimension.name, SUM(traffic.upload_bytes), SUM(traffic.download_bytes)
		FROM minute_traffic AS traffic
		JOIN apps AS dimension ON dimension.id = traffic.app_id
		WHERE traffic.minute >= ? AND traffic.minute < ? AND traffic.attribution_class = 'observed'
		GROUP BY dimension.id, dimension.name
		ORDER BY SUM(traffic.upload_bytes + traffic.download_bytes) DESC, dimension.name ASC
		LIMIT 5
	`,
}

var hostLeaders = leaderQuery{
	name: "Host",
	statement: `
		SELECT dimension.host, SUM(traffic.upload_bytes), SUM(traffic.download_bytes)
		FROM minute_traffic AS traffic
		JOIN endpoints AS dimension ON dimension.id = traffic.endpoint_id
		WHERE traffic.minute >= ? AND traffic.minute < ? AND traffic.attribution_class = 'observed'
		GROUP BY dimension.id, dimension.host
		ORDER BY SUM(traffic.upload_bytes + traffic.download_bytes) DESC, dimension.host ASC
		LIMIT 5
	`,
}

func Open(path string) (*Store, error) {
	dataDirectory := filepath.Dir(path)
	if err := ensurePrivateDataDirectory(dataDirectory); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close database file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure database: %w", err)
	}

	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	store := &Store{database: database, path: path}
	if err := store.initialize(); err != nil {
		_ = database.Close()
		return nil, err
	}
	return store, nil
}

func ensurePrivateDataDirectory(path string) error {
	fileInfo, err := os.Stat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create data directory: %w", err)
		}
		return os.Chmod(path, 0o700)
	}
	if err != nil {
		return fmt.Errorf("inspect data directory: %w", err)
	}
	if !fileInfo.IsDir() {
		return fmt.Errorf("data directory path is not a directory")
	}
	if fileInfo.Mode().Perm() != 0o700 {
		return fmt.Errorf("existing data directory must have permissions 0700; refusing to change it automatically")
	}
	return nil
}

func (store *Store) initialize() error {
	store.database.SetConnMaxLifetime(0)
	store.database.SetMaxIdleConns(2)
	store.database.SetMaxOpenConns(4)
	if err := store.database.Ping(); err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := store.database.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		return fmt.Errorf("enable sqlite WAL: %w", err)
	}
	if _, err := store.database.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	if _, err := store.database.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		return fmt.Errorf("set sqlite busy timeout: %w", err)
	}
	return store.migrate()
}

func (store *Store) migrate() error {
	transaction, err := store.database.Begin()
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	if _, err := transaction.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	var version int
	if err := transaction.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("database schema %d is newer than supported schema %d", version, currentSchemaVersion)
	}
	if version < 1 {
		if _, err := transaction.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(1, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record schema version 1: %w", err)
		}
	}
	if version < 2 {
		if _, err := transaction.Exec(`
			CREATE TABLE apps (
				id INTEGER PRIMARY KEY,
				name TEXT NOT NULL UNIQUE
			);
			CREATE TABLE endpoints (
				id INTEGER PRIMARY KEY,
				host TEXT NOT NULL,
				registrable_domain TEXT NOT NULL DEFAULT '',
				UNIQUE(host, registrable_domain)
			);
			CREATE TABLE minute_traffic (
				minute INTEGER NOT NULL,
				attribution_class TEXT NOT NULL CHECK(attribution_class IN ('observed', 'residual', 'gap_recovered')),
				app_id INTEGER REFERENCES apps(id),
				endpoint_id INTEGER REFERENCES endpoints(id),
				upload_bytes INTEGER NOT NULL CHECK(upload_bytes >= 0),
				download_bytes INTEGER NOT NULL CHECK(download_bytes >= 0),
				CHECK(
					(attribution_class = 'observed' AND app_id IS NOT NULL AND endpoint_id IS NOT NULL)
					OR (attribution_class != 'observed' AND app_id IS NULL AND endpoint_id IS NULL)
				)
			);
			CREATE UNIQUE INDEX minute_traffic_identity
				ON minute_traffic(minute, attribution_class, COALESCE(app_id, 0), COALESCE(endpoint_id, 0));
			CREATE INDEX minute_traffic_range ON minute_traffic(minute);
		`); err != nil {
			return fmt.Errorf("create minute traffic schema: %w", err)
		}
		if _, err := transaction.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(2, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record schema version 2: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func (store *Store) AddTraffic(records []traffic.Record) error {
	if len(records) == 0 {
		return nil
	}
	transaction, err := store.database.Begin()
	if err != nil {
		return fmt.Errorf("begin traffic transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	for _, record := range records {
		if err := validateRecord(record); err != nil {
			return err
		}
		var appID any
		var endpointID any
		if record.Class == traffic.Observed {
			resolvedAppID, err := upsertApp(transaction, record.App)
			if err != nil {
				return err
			}
			resolvedEndpointID, err := upsertEndpoint(transaction, record.Host, record.RegistrableDomain)
			if err != nil {
				return err
			}
			appID = resolvedAppID
			endpointID = resolvedEndpointID
		}
		if _, err := transaction.Exec(`
			INSERT INTO minute_traffic(minute, attribution_class, app_id, endpoint_id, upload_bytes, download_bytes)
			VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT DO UPDATE SET
				upload_bytes = upload_bytes + excluded.upload_bytes,
				download_bytes = download_bytes + excluded.download_bytes
		`, record.Minute.UTC().Truncate(time.Minute).Unix(), record.Class, appID, endpointID, record.Upload, record.Download); err != nil {
			return fmt.Errorf("upsert minute traffic: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit traffic transaction: %w", err)
	}
	return nil
}

func validateRecord(record traffic.Record) error {
	if record.Minute.IsZero() {
		return fmt.Errorf("traffic minute is required")
	}
	if record.Upload < 0 || record.Download < 0 {
		return fmt.Errorf("traffic counters must be non-negative")
	}
	switch record.Class {
	case traffic.Observed:
		if record.App == "" || record.Host == "" {
			return fmt.Errorf("observed traffic requires App and Host dimensions")
		}
	case traffic.Residual, traffic.GapRecovered:
		if record.App != "" || record.Host != "" || record.RegistrableDomain != "" {
			return fmt.Errorf("%s traffic cannot have dimensions", record.Class)
		}
	default:
		return fmt.Errorf("unsupported attribution class %q", record.Class)
	}
	return nil
}

func upsertApp(transaction *sql.Tx, name string) (int64, error) {
	if _, err := transaction.Exec(`INSERT INTO apps(name) VALUES(?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
		return 0, fmt.Errorf("upsert App: %w", err)
	}
	var id int64
	if err := transaction.QueryRow(`SELECT id FROM apps WHERE name = ?`, name).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve App: %w", err)
	}
	return id, nil
}

func upsertEndpoint(transaction *sql.Tx, host, registrableDomain string) (int64, error) {
	if _, err := transaction.Exec(`
		INSERT INTO endpoints(host, registrable_domain) VALUES(?, ?)
		ON CONFLICT(host, registrable_domain) DO NOTHING
	`, host, registrableDomain); err != nil {
		return 0, fmt.Errorf("upsert endpoint: %w", err)
	}
	var id int64
	if err := transaction.QueryRow(`SELECT id FROM endpoints WHERE host = ? AND registrable_domain = ?`, host, registrableDomain).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve endpoint: %w", err)
	}
	return id, nil
}

func (store *Store) Summary(start, end time.Time) (TrafficSummary, error) {
	if !end.After(start) {
		return TrafficSummary{}, fmt.Errorf("summary end must be after start")
	}
	result := TrafficSummary{Apps: []Leader{}, Hosts: []Leader{}}
	rows, err := store.database.Query(`
		SELECT attribution_class, COALESCE(SUM(upload_bytes), 0), COALESCE(SUM(download_bytes), 0)
		FROM minute_traffic
		WHERE minute >= ? AND minute < ?
		GROUP BY attribution_class
	`, unixCeiling(start), unixCeiling(end))
	if err != nil {
		return TrafficSummary{}, fmt.Errorf("query traffic summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var class traffic.Class
		var upload int64
		var download int64
		if err := rows.Scan(&class, &upload, &download); err != nil {
			return TrafficSummary{}, fmt.Errorf("scan traffic summary: %w", err)
		}
		applyClassTotals(&result.Upload, class, upload)
		applyClassTotals(&result.Download, class, download)
		applyClassTotals(&result.Total, class, upload+download)
	}
	if err := rows.Err(); err != nil {
		return TrafficSummary{}, fmt.Errorf("iterate traffic summary: %w", err)
	}
	finalizeTotals(&result.Upload)
	finalizeTotals(&result.Download)
	finalizeTotals(&result.Total)
	if result.Total.Total > 0 {
		result.Coverage = float64(result.Total.Observed) / float64(result.Total.Total)
	}
	result.Apps, err = store.leaders(start, end, appLeaders)
	if err != nil {
		return TrafficSummary{}, err
	}
	result.Hosts, err = store.leaders(start, end, hostLeaders)
	if err != nil {
		return TrafficSummary{}, err
	}
	return result, nil
}

func applyClassTotals(totals *AttributionTotals, class traffic.Class, value int64) {
	switch class {
	case traffic.Observed:
		totals.Observed = value
	case traffic.Residual:
		totals.Residual = value
	case traffic.GapRecovered:
		totals.GapRecovered = value
	}
}

func finalizeTotals(totals *AttributionTotals) {
	totals.Total = totals.Observed + totals.Residual + totals.GapRecovered
}

func (store *Store) leaders(start, end time.Time, query leaderQuery) ([]Leader, error) {
	rows, err := store.database.Query(query.statement, unixCeiling(start), unixCeiling(end))
	if err != nil {
		return nil, fmt.Errorf("query %s leaders: %w", query.name, err)
	}
	defer rows.Close()
	leaders := []Leader{}
	for rows.Next() {
		var leader Leader
		if err := rows.Scan(&leader.Name, &leader.Upload, &leader.Download); err != nil {
			return nil, fmt.Errorf("scan %s leader: %w", query.name, err)
		}
		leader.Total = leader.Upload + leader.Download
		leaders = append(leaders, leader)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s leaders: %w", query.name, err)
	}
	return leaders, nil
}

func unixCeiling(value time.Time) int64 {
	value = value.UTC()
	seconds := value.Unix()
	if value.Nanosecond() > 0 {
		seconds++
	}
	return seconds
}

func (store *Store) Info() Info {
	info := Info{Healthy: true}
	if err := store.database.Ping(); err != nil {
		info.Healthy = false
		info.Error = err.Error()
		return info
	}
	if err := store.database.QueryRow(`PRAGMA journal_mode`).Scan(&info.JournalMode); err != nil {
		info.Healthy = false
		info.Error = err.Error()
		return info
	}
	if err := store.database.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&info.SchemaVersion); err != nil {
		info.Healthy = false
		info.Error = err.Error()
		return info
	}
	for _, path := range []string{store.path, store.path + "-wal", store.path + "-shm"} {
		fileInfo, err := os.Stat(path)
		if os.IsNotExist(err) && path != store.path {
			continue
		}
		if err != nil {
			info.Healthy = false
			info.Error = err.Error()
			return info
		}
		info.SizeBytes += fileInfo.Size()
	}
	return info
}

func (store *Store) Close() error {
	return store.database.Close()
}
