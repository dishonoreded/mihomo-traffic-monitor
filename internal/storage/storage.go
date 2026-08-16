package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/continuity"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/traffic"
	_ "modernc.org/sqlite"
)

const currentSchemaVersion = 3

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

type TrafficScope string

const (
	ScopeAll      TrafficScope = "all"
	ScopeObserved TrafficScope = "observed"
)

type TrafficFilter struct {
	Apps    []string
	Hosts   []string
	Domains []string
}

func (filter TrafficFilter) Active() bool {
	return len(filter.Apps) > 0 || len(filter.Hosts) > 0 || len(filter.Domains) > 0
}

type DimensionValues struct {
	Apps    []string `json:"apps"`
	Hosts   []string `json:"hosts"`
	Domains []string `json:"domains"`
}

type RankingDimension string

const (
	DimensionApp    RankingDimension = "app"
	DimensionHost   RankingDimension = "host"
	DimensionDomain RankingDimension = "domain"
)

type RankingDirection string

const (
	DirectionUpload   RankingDirection = "upload"
	DirectionDownload RankingDirection = "download"
	DirectionTotal    RankingDirection = "total"
)

type RankingOptions struct {
	Start     time.Time
	End       time.Time
	Dimension RankingDimension
	Direction RankingDirection
	Limit     int
	Filter    TrafficFilter
}

type TrafficRankings struct {
	Scope     TrafficScope
	Dimension RankingDimension
	Direction RankingDirection
	Items     []Leader
}

type TrafficSummary struct {
	Scope    TrafficScope
	Upload   AttributionTotals
	Download AttributionTotals
	Total    AttributionTotals
	Coverage float64
	Apps     []Leader
	Hosts    []Leader
}

type Granularity string

const (
	GranularityMinute Granularity = "minute"
	GranularityHour   Granularity = "hour"
	GranularityDay    Granularity = "day"
	GranularityAuto   Granularity = "auto"
	AutoPointLimit                = 400
)

var ErrAutoPointLimitExceeded = errors.New("automatic series exceeds the point limit at day granularity")

type SeriesOptions struct {
	Start       time.Time
	End         time.Time
	Granularity Granularity
	Location    *time.Location
	Filter      TrafficFilter
}

type SeriesPoint struct {
	Start    time.Time         `json:"start"`
	Upload   AttributionTotals `json:"upload"`
	Download AttributionTotals `json:"download"`
	Total    AttributionTotals `json:"total"`
}

type TrafficSeries struct {
	Granularity Granularity
	Scope       TrafficScope
	Points      []SeriesPoint
}

type seriesCandidate struct {
	points   map[time.Time]*SeriesPoint
	exceeded bool
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
	if version < 3 {
		if _, err := transaction.Exec(`
			CREATE TABLE collector_state (
				singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
				sampled_at_ns INTEGER NOT NULL,
				upload_total INTEGER NOT NULL CHECK(upload_total >= 0),
				download_total INTEGER NOT NULL CHECK(download_total >= 0)
			);
			CREATE TABLE collection_gaps (
				id INTEGER PRIMARY KEY,
				started_at_ns INTEGER NOT NULL,
				ended_at_ns INTEGER,
				reason TEXT NOT NULL,
				disposition TEXT NOT NULL CHECK(disposition IN ('open', 'recovered', 'no_growth', 'reset')),
				recovered_upload INTEGER NOT NULL DEFAULT 0 CHECK(recovered_upload >= 0),
				recovered_download INTEGER NOT NULL DEFAULT 0 CHECK(recovered_download >= 0),
				CHECK(
					(ended_at_ns IS NULL AND disposition = 'open')
					OR (ended_at_ns IS NOT NULL AND disposition != 'open')
				)
			);
			CREATE UNIQUE INDEX collection_gaps_one_open
				ON collection_gaps((1)) WHERE ended_at_ns IS NULL;
			CREATE INDEX collection_gaps_range ON collection_gaps(started_at_ns, ended_at_ns);
		`); err != nil {
			return fmt.Errorf("create collection continuity schema: %w", err)
		}
		if _, err := transaction.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(3, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record schema version 3: %w", err)
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
	if err := addTrafficRecords(transaction, records); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit traffic transaction: %w", err)
	}
	return nil
}

func addTrafficRecords(transaction *sql.Tx, records []traffic.Record) error {
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
	return nil
}

func (store *Store) OpenCollectionGap(reason continuity.Reason) error {
	if reason == "" {
		return fmt.Errorf("Collection gap reason is required")
	}
	transaction, err := store.database.Begin()
	if err != nil {
		return fmt.Errorf("begin Collection gap transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	var sampledAt int64
	if err := transaction.QueryRow(`SELECT sampled_at_ns FROM collector_state WHERE singleton = 1`).Scan(&sampledAt); err != nil {
		if err == sql.ErrNoRows {
			return transaction.Commit()
		}
		return fmt.Errorf("read collector state for Collection gap: %w", err)
	}
	var openID int64
	err = transaction.QueryRow(`SELECT id FROM collection_gaps WHERE ended_at_ns IS NULL`).Scan(&openID)
	switch {
	case err == nil:
		if _, err := transaction.Exec(`UPDATE collection_gaps SET reason = ? WHERE id = ?`, reason, openID); err != nil {
			return fmt.Errorf("update open Collection gap: %w", err)
		}
	case err == sql.ErrNoRows:
		if _, err := transaction.Exec(`
			INSERT INTO collection_gaps(started_at_ns, reason, disposition)
			VALUES(?, ?, ?)
		`, sampledAt, reason, continuity.DispositionOpen); err != nil {
			return fmt.Errorf("open Collection gap: %w", err)
		}
	default:
		return fmt.Errorf("read open Collection gap: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit Collection gap: %w", err)
	}
	return nil
}

func (store *Store) AcceptSample(state continuity.State, records []traffic.Record) (continuity.Acceptance, error) {
	if state.SampledAt.IsZero() || state.UploadTotal < 0 || state.DownloadTotal < 0 {
		return continuity.Acceptance{}, fmt.Errorf("valid collector sample state is required")
	}
	transaction, err := store.database.Begin()
	if err != nil {
		return continuity.Acceptance{}, fmt.Errorf("begin collector sample transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	var previous continuity.State
	var previousAt int64
	stateErr := transaction.QueryRow(`
		SELECT sampled_at_ns, upload_total, download_total
		FROM collector_state WHERE singleton = 1
	`).Scan(&previousAt, &previous.UploadTotal, &previous.DownloadTotal)
	if stateErr != nil && stateErr != sql.ErrNoRows {
		return continuity.Acceptance{}, fmt.Errorf("read collector state: %w", stateErr)
	}
	if stateErr == nil {
		previous.SampledAt = time.Unix(0, previousAt).UTC()
	}

	result := continuity.Acceptance{}
	gap, err := openGap(transaction)
	if err != nil {
		return continuity.Acceptance{}, err
	}
	recovery := continuity.Recovery{Disposition: continuity.DispositionReset}
	if stateErr == nil {
		recovery = continuity.EvaluateRecovery(previous, state)
	}
	if gap == nil && recovery.Disposition == continuity.DispositionReset && stateErr == nil {
		endedAt := state.SampledAt.UTC()
		inserted, err := transaction.Exec(`
			INSERT INTO collection_gaps(
				started_at_ns, ended_at_ns, reason, disposition, recovered_upload, recovered_download
			) VALUES(?, ?, ?, ?, 0, 0)
		`, previous.SampledAt.UnixNano(), endedAt.UnixNano(), continuity.ReasonCounterReset, continuity.DispositionReset)
		if err != nil {
			return continuity.Acceptance{}, fmt.Errorf("record Controller counter reset: %w", err)
		}
		gapID, err := inserted.LastInsertId()
		if err != nil {
			return continuity.Acceptance{}, fmt.Errorf("resolve Controller counter reset: %w", err)
		}
		result.Gap = &continuity.Gap{
			ID: gapID, StartedAt: previous.SampledAt, EndedAt: &endedAt,
			Reason: continuity.ReasonCounterReset, Disposition: continuity.DispositionReset,
		}
	}
	if gap != nil {
		if recovery.Disposition == continuity.DispositionRecovered {
			if err := addTrafficRecords(transaction, []traffic.Record{{
				Minute: state.SampledAt, Class: traffic.GapRecovered, Upload: recovery.Upload, Download: recovery.Download,
			}}); err != nil {
				return continuity.Acceptance{}, err
			}
		}
		endedAt := state.SampledAt.UTC()
		if _, err := transaction.Exec(`
			UPDATE collection_gaps
			SET ended_at_ns = ?, disposition = ?, recovered_upload = ?, recovered_download = ?
			WHERE id = ?
		`, endedAt.UnixNano(), recovery.Disposition, recovery.Upload, recovery.Download, gap.ID); err != nil {
			return continuity.Acceptance{}, fmt.Errorf("close Collection gap: %w", err)
		}
		gap.EndedAt = &endedAt
		gap.Open = false
		gap.Disposition = recovery.Disposition
		gap.RecoveredUpload = recovery.Upload
		gap.RecoveredDownload = recovery.Download
		result.Gap = gap
	}
	if err := addTrafficRecords(transaction, records); err != nil {
		return continuity.Acceptance{}, err
	}
	if _, err := transaction.Exec(`
		INSERT INTO collector_state(singleton, sampled_at_ns, upload_total, download_total)
		VALUES(1, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			sampled_at_ns = excluded.sampled_at_ns,
			upload_total = excluded.upload_total,
			download_total = excluded.download_total
	`, state.SampledAt.UTC().UnixNano(), state.UploadTotal, state.DownloadTotal); err != nil {
		return continuity.Acceptance{}, fmt.Errorf("persist collector state: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return continuity.Acceptance{}, fmt.Errorf("commit collector sample: %w", err)
	}
	return result, nil
}

func openGap(transaction *sql.Tx) (*continuity.Gap, error) {
	var gap continuity.Gap
	var startedAt int64
	err := transaction.QueryRow(`
		SELECT id, started_at_ns, reason
		FROM collection_gaps WHERE ended_at_ns IS NULL
	`).Scan(&gap.ID, &startedAt, &gap.Reason)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read open Collection gap: %w", err)
	}
	gap.StartedAt = time.Unix(0, startedAt).UTC()
	gap.Open = true
	gap.Disposition = continuity.DispositionOpen
	return &gap, nil
}

func (store *Store) CollectionGaps(start, end time.Time) ([]continuity.Gap, error) {
	if !end.After(start) {
		return nil, fmt.Errorf("Collection gap end must be after start")
	}
	rows, err := store.database.Query(`
		SELECT id, started_at_ns, ended_at_ns, reason, disposition, recovered_upload, recovered_download
		FROM collection_gaps
		WHERE started_at_ns < ? AND (ended_at_ns IS NULL OR ended_at_ns > ?)
		ORDER BY started_at_ns DESC, id DESC
	`, end.UTC().UnixNano(), start.UTC().UnixNano())
	if err != nil {
		return nil, fmt.Errorf("query Collection gaps: %w", err)
	}
	defer rows.Close()
	gaps := []continuity.Gap{}
	for rows.Next() {
		var gap continuity.Gap
		var startedAt int64
		var endedAt sql.NullInt64
		if err := rows.Scan(&gap.ID, &startedAt, &endedAt, &gap.Reason, &gap.Disposition, &gap.RecoveredUpload, &gap.RecoveredDownload); err != nil {
			return nil, fmt.Errorf("scan Collection gap: %w", err)
		}
		gap.StartedAt = time.Unix(0, startedAt).UTC()
		gap.Open = !endedAt.Valid
		if endedAt.Valid {
			value := time.Unix(0, endedAt.Int64).UTC()
			gap.EndedAt = &value
		}
		gaps = append(gaps, gap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Collection gaps: %w", err)
	}
	return gaps, nil
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
	return store.FilteredSummary(start, end, TrafficFilter{})
}

func (store *Store) FilteredSummary(start, end time.Time, filter TrafficFilter) (TrafficSummary, error) {
	if !end.After(start) {
		return TrafficSummary{}, fmt.Errorf("summary end must be after start")
	}
	result := TrafficSummary{Scope: ScopeAll, Apps: []Leader{}, Hosts: []Leader{}}
	statement := `
		SELECT attribution_class, COALESCE(SUM(upload_bytes), 0), COALESCE(SUM(download_bytes), 0)
		FROM minute_traffic AS traffic
		WHERE traffic.minute >= ? AND traffic.minute < ?
	`
	arguments := []any{unixCeiling(start), unixCeiling(end)}
	if filter.Active() {
		result.Scope = ScopeObserved
		statement = `
			SELECT traffic.attribution_class, COALESCE(SUM(traffic.upload_bytes), 0), COALESCE(SUM(traffic.download_bytes), 0)
			FROM minute_traffic AS traffic
			JOIN apps AS app ON app.id = traffic.app_id
			JOIN endpoints AS endpoint ON endpoint.id = traffic.endpoint_id
			WHERE traffic.minute >= ? AND traffic.minute < ? AND traffic.attribution_class = 'observed'
		`
		filterSQL, filterArguments := trafficFilterSQL(filter)
		statement += filterSQL
		arguments = append(arguments, filterArguments...)
	}
	statement += `
		GROUP BY attribution_class
	`
	rows, err := store.database.Query(statement, arguments...)
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
		addClassTotals(&result.Upload, class, upload)
		addClassTotals(&result.Download, class, download)
		addClassTotals(&result.Total, class, upload+download)
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
	result.Apps, err = store.dimensionLeaders(start, end, DimensionApp, DirectionTotal, 5, filter)
	if err != nil {
		return TrafficSummary{}, err
	}
	result.Hosts, err = store.dimensionLeaders(start, end, DimensionHost, DirectionTotal, 5, filter)
	if err != nil {
		return TrafficSummary{}, err
	}
	return result, nil
}

func (store *Store) Series(options SeriesOptions) (TrafficSeries, error) {
	if !options.End.After(options.Start) {
		return TrafficSeries{}, fmt.Errorf("series end must be after start")
	}
	if options.Location == nil {
		return TrafficSeries{}, fmt.Errorf("series location is required")
	}
	if options.Granularity != GranularityMinute && options.Granularity != GranularityHour && options.Granularity != GranularityDay && options.Granularity != GranularityAuto {
		return TrafficSeries{}, fmt.Errorf("unsupported series granularity %q", options.Granularity)
	}
	granularities := []Granularity{options.Granularity}
	pointLimit := 0
	if options.Granularity == GranularityAuto {
		granularities = []Granularity{GranularityMinute, GranularityHour, GranularityDay}
		pointLimit = AutoPointLimit
	}
	candidates := make(map[Granularity]*seriesCandidate, len(granularities))
	for _, granularity := range granularities {
		candidates[granularity] = &seriesCandidate{points: make(map[time.Time]*SeriesPoint)}
	}
	statement := `
		SELECT minute, attribution_class, SUM(upload_bytes), SUM(download_bytes)
		FROM minute_traffic AS traffic
		WHERE traffic.minute >= ? AND traffic.minute < ?
	`
	arguments := []any{unixCeiling(options.Start), unixCeiling(options.End)}
	if options.Filter.Active() {
		statement = `
			SELECT traffic.minute, traffic.attribution_class, SUM(traffic.upload_bytes), SUM(traffic.download_bytes)
			FROM minute_traffic AS traffic
			JOIN apps AS app ON app.id = traffic.app_id
			JOIN endpoints AS endpoint ON endpoint.id = traffic.endpoint_id
			WHERE traffic.minute >= ? AND traffic.minute < ? AND traffic.attribution_class = 'observed'
		`
		filterSQL, filterArguments := trafficFilterSQL(options.Filter)
		statement += filterSQL
		arguments = append(arguments, filterArguments...)
	}
	statement += `
		GROUP BY minute, attribution_class
		ORDER BY minute
	`
	rows, err := store.database.Query(statement, arguments...)
	if err != nil {
		return TrafficSeries{}, fmt.Errorf("query traffic series: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var minute int64
		var class traffic.Class
		var upload int64
		var download int64
		if err := rows.Scan(&minute, &class, &upload, &download); err != nil {
			return TrafficSeries{}, fmt.Errorf("scan traffic series: %w", err)
		}
		instant := time.Unix(minute, 0).UTC()
		for _, granularity := range granularities {
			candidate := candidates[granularity]
			if candidate.exceeded {
				continue
			}
			start := seriesBucket(instant, granularity, options.Location)
			point := candidate.points[start]
			if point == nil {
				if pointLimit > 0 && len(candidate.points) == pointLimit {
					candidate.points = nil
					candidate.exceeded = true
					continue
				}
				point = &SeriesPoint{Start: start}
				candidate.points[start] = point
			}
			addClassTotals(&point.Upload, class, upload)
			addClassTotals(&point.Download, class, download)
			addClassTotals(&point.Total, class, upload+download)
		}
	}
	if err := rows.Err(); err != nil {
		return TrafficSeries{}, fmt.Errorf("iterate traffic series: %w", err)
	}
	selected := options.Granularity
	if selected == GranularityAuto {
		selected = ""
		for _, granularity := range granularities {
			if !candidates[granularity].exceeded {
				selected = granularity
				break
			}
		}
		if selected == "" {
			return TrafficSeries{}, ErrAutoPointLimitExceeded
		}
	}
	pointsByStart := candidates[selected].points
	scope := ScopeAll
	if options.Filter.Active() {
		scope = ScopeObserved
	}
	result := TrafficSeries{Granularity: selected, Scope: scope, Points: make([]SeriesPoint, 0, len(pointsByStart))}
	for _, point := range pointsByStart {
		finalizeTotals(&point.Upload)
		finalizeTotals(&point.Download)
		finalizeTotals(&point.Total)
		result.Points = append(result.Points, *point)
	}
	sort.Slice(result.Points, func(left, right int) bool { return result.Points[left].Start.Before(result.Points[right].Start) })
	return result, nil
}

func seriesBucket(instant time.Time, granularity Granularity, location *time.Location) time.Time {
	local := instant.In(location)
	switch granularity {
	case GranularityMinute:
		return local
	case GranularityDay:
		return calendarDayStart(instant, local, location)
	default:
		return calendarHourStart(instant, local, location)
	}
}

func calendarHourStart(instant, local time.Time, location *time.Location) time.Time {
	candidate := instant.Add(-time.Duration(local.Minute())*time.Minute - time.Duration(local.Second())*time.Second - time.Duration(local.Nanosecond()))
	if sameCalendarHourSegment(candidate.In(location), local) {
		return candidate.In(location)
	}

	low, high := candidate.Unix(), instant.Unix()
	for low < high {
		middle := low + (high-low)/2
		if sameCalendarHourSegment(time.Unix(middle, 0).In(location), local) {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return time.Unix(low, 0).In(location)
}

func sameCalendarHourSegment(left, right time.Time) bool {
	_, leftOffset := left.Zone()
	_, rightOffset := right.Zone()
	return left.Year() == right.Year() && left.Month() == right.Month() && left.Day() == right.Day() && left.Hour() == right.Hour() && leftOffset == rightOffset
}

func calendarDayStart(instant, local time.Time, location *time.Location) time.Time {
	candidate := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	if sameCalendarDay(candidate.In(location), local) && !sameCalendarDay(candidate.Add(-time.Second).In(location), local) {
		return candidate.In(location)
	}

	target := calendarDayNumber(local)
	low, high := instant.Add(-48*time.Hour).Unix(), instant.Unix()
	for low < high {
		middle := low + (high-low)/2
		if calendarDayNumber(time.Unix(middle, 0).In(location)) >= target {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return time.Unix(low, 0).In(location)
}

func sameCalendarDay(left, right time.Time) bool {
	return left.Year() == right.Year() && left.Month() == right.Month() && left.Day() == right.Day()
}

func calendarDayNumber(value time.Time) int {
	return value.Year()*10_000 + int(value.Month())*100 + value.Day()
}

func addClassTotals(totals *AttributionTotals, class traffic.Class, value int64) {
	switch class {
	case traffic.Observed:
		totals.Observed += value
	case traffic.Residual:
		totals.Residual += value
	case traffic.GapRecovered:
		totals.GapRecovered += value
	}
}

func finalizeTotals(totals *AttributionTotals) {
	totals.Total = totals.Observed + totals.Residual + totals.GapRecovered
}

func trafficFilterSQL(filter TrafficFilter) (string, []any) {
	statement := ""
	arguments := []any{}
	for _, values := range []struct {
		column string
		items  []string
	}{
		{column: "app.name", items: filter.Apps},
		{column: "endpoint.host", items: filter.Hosts},
		{column: "endpoint.registrable_domain", items: filter.Domains},
	} {
		if len(values.items) == 0 {
			continue
		}
		statement += " AND " + values.column + " IN (" + placeholders(len(values.items)) + ")"
		for _, value := range values.items {
			arguments = append(arguments, value)
		}
	}
	return statement, arguments
}

func placeholders(count int) string {
	result := "?"
	for index := 1; index < count; index++ {
		result += ", ?"
	}
	return result
}

func (store *Store) Dimensions(search string, limit int) (DimensionValues, error) {
	if limit < 1 {
		return DimensionValues{}, fmt.Errorf("dimension limit must be positive")
	}
	result := DimensionValues{Apps: []string{}, Hosts: []string{}, Domains: []string{}}
	queries := []struct {
		name      string
		statement string
		target    *[]string
	}{
		{name: "Apps", statement: `SELECT name FROM apps WHERE instr(lower(name), lower(?)) > 0 ORDER BY name COLLATE NOCASE, name LIMIT ?`, target: &result.Apps},
		{name: "Hosts", statement: `SELECT DISTINCT host FROM endpoints WHERE instr(lower(host), lower(?)) > 0 ORDER BY host COLLATE NOCASE, host LIMIT ?`, target: &result.Hosts},
		{name: "domains", statement: `SELECT DISTINCT registrable_domain FROM endpoints WHERE registrable_domain != '' AND instr(lower(registrable_domain), lower(?)) > 0 ORDER BY registrable_domain COLLATE NOCASE, registrable_domain LIMIT ?`, target: &result.Domains},
	}
	for _, query := range queries {
		rows, err := store.database.Query(query.statement, search, limit)
		if err != nil {
			return DimensionValues{}, fmt.Errorf("query %s dimensions: %w", query.name, err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				_ = rows.Close()
				return DimensionValues{}, fmt.Errorf("scan %s dimension: %w", query.name, err)
			}
			*query.target = append(*query.target, value)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return DimensionValues{}, fmt.Errorf("iterate %s dimensions: %w", query.name, err)
		}
		if err := rows.Close(); err != nil {
			return DimensionValues{}, fmt.Errorf("close %s dimensions: %w", query.name, err)
		}
	}
	return result, nil
}

func (store *Store) Rankings(options RankingOptions) (TrafficRankings, error) {
	if !options.End.After(options.Start) {
		return TrafficRankings{}, fmt.Errorf("ranking end must be after start")
	}
	if options.Limit < 1 {
		return TrafficRankings{}, fmt.Errorf("ranking limit must be positive")
	}
	if options.Dimension != DimensionApp && options.Dimension != DimensionHost && options.Dimension != DimensionDomain {
		return TrafficRankings{}, fmt.Errorf("unsupported ranking dimension %q", options.Dimension)
	}
	if options.Direction != DirectionUpload && options.Direction != DirectionDownload && options.Direction != DirectionTotal {
		return TrafficRankings{}, fmt.Errorf("unsupported ranking direction %q", options.Direction)
	}
	items, err := store.dimensionLeaders(options.Start, options.End, options.Dimension, options.Direction, options.Limit, options.Filter)
	if err != nil {
		return TrafficRankings{}, err
	}
	return TrafficRankings{Scope: ScopeObserved, Dimension: options.Dimension, Direction: options.Direction, Items: items}, nil
}

func (store *Store) dimensionLeaders(start, end time.Time, dimension RankingDimension, direction RankingDirection, limit int, filter TrafficFilter) ([]Leader, error) {
	nameExpression := "app.name"
	name := "App"
	if dimension == DimensionHost {
		nameExpression = "endpoint.host"
		name = "Host"
	} else if dimension == DimensionDomain {
		nameExpression = "endpoint.registrable_domain"
		name = "domain"
	}
	orderExpression := "SUM(traffic.upload_bytes + traffic.download_bytes)"
	if direction == DirectionUpload {
		orderExpression = "SUM(traffic.upload_bytes)"
	} else if direction == DirectionDownload {
		orderExpression = "SUM(traffic.download_bytes)"
	}
	statement := `
		SELECT ` + nameExpression + `, SUM(traffic.upload_bytes), SUM(traffic.download_bytes)
		FROM minute_traffic AS traffic
		JOIN apps AS app ON app.id = traffic.app_id
		JOIN endpoints AS endpoint ON endpoint.id = traffic.endpoint_id
		WHERE traffic.minute >= ? AND traffic.minute < ? AND traffic.attribution_class = 'observed'
	`
	arguments := []any{unixCeiling(start), unixCeiling(end)}
	filterSQL, filterArguments := trafficFilterSQL(filter)
	statement += filterSQL
	arguments = append(arguments, filterArguments...)
	if dimension == DimensionDomain {
		statement += " AND endpoint.registrable_domain != ''"
	}
	statement += " GROUP BY " + nameExpression + " ORDER BY " + orderExpression + " DESC, " + nameExpression + " ASC LIMIT ?"
	arguments = append(arguments, limit)
	rows, err := store.database.Query(statement, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query %s leaders: %w", name, err)
	}
	defer rows.Close()
	leaders := []Leader{}
	for rows.Next() {
		var leader Leader
		if err := rows.Scan(&leader.Name, &leader.Upload, &leader.Download); err != nil {
			return nil, fmt.Errorf("scan %s leader: %w", name, err)
		}
		leader.Total = leader.Upload + leader.Download
		leaders = append(leaders, leader)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s leaders: %w", name, err)
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
