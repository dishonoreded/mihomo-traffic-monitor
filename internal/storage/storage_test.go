package storage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/storage"
)

func TestOpenCreatesPrivateMigratedWALDatabase(t *testing.T) {
	t.Parallel()

	dataDirectory := filepath.Join(t.TempDir(), "monitor-data")
	databasePath := filepath.Join(dataDirectory, "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	info := store.Info()
	if !info.Healthy {
		t.Fatalf("database is unhealthy: %s", info.Error)
	}
	if got, want := info.JournalMode, "wal"; got != want {
		t.Fatalf("journal mode = %q, want %q", got, want)
	}
	if got, want := info.SchemaVersion, 1; got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
	var storedBytes int64
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		if fileInfo, err := os.Stat(path); err == nil {
			storedBytes += fileInfo.Size()
		}
	}
	if info.SizeBytes != storedBytes {
		t.Fatalf("reported database size = %d, want database plus WAL/SHM size %d", info.SizeBytes, storedBytes)
	}

	assertPermissions(t, dataDirectory, 0o700)
	assertPermissions(t, databasePath, 0o600)
}

func TestOpenRefusesAnExistingSharedDataDirectory(t *testing.T) {
	t.Parallel()

	dataDirectory := filepath.Join(t.TempDir(), "shared-data")
	if err := os.Mkdir(dataDirectory, 0o755); err != nil {
		t.Fatalf("create shared directory: %v", err)
	}
	if err := os.Chmod(dataDirectory, 0o755); err != nil {
		t.Fatalf("set shared directory permissions: %v", err)
	}
	_, err := storage.Open(filepath.Join(dataDirectory, "traffic.db"))
	if err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("expected private-directory error, got %v", err)
	}
	if got := mustPermissions(t, dataDirectory); got != 0o755 {
		t.Fatalf("existing directory was mutated to %04o", got)
	}
}

func assertPermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if got := mustPermissions(t, path); got != want {
		t.Fatalf("permissions for %s = %04o, want %04o", path, got, want)
	}
}

func mustPermissions(t *testing.T, path string) os.FileMode {
	t.Helper()
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fileInfo.Mode().Perm()
}
