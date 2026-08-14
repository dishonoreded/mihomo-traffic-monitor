package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const currentSchemaVersion = 1

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
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
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
