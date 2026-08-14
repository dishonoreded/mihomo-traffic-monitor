package storage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/storage"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/traffic"
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
	if got, want := info.SchemaVersion, 2; got != want {
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

func TestMinuteTrafficUPSERTAndHalfOpenSummarySurviveReopen(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	minute := time.Date(2026, 8, 14, 9, 30, 0, 0, time.UTC)
	records := []traffic.Record{
		{Minute: minute, Class: traffic.Observed, App: "Safari", Host: "api.example.com", RegistrableDomain: "example.com", Upload: 20, Download: 80},
		{Minute: minute, Class: traffic.Residual, Upload: 5, Download: 15},
		{Minute: minute.Add(time.Minute), Class: traffic.GapRecovered, Upload: 7, Download: 13},
	}
	if err := store.AddTraffic(records); err != nil {
		t.Fatalf("add traffic: %v", err)
	}
	if err := store.AddTraffic([]traffic.Record{{
		Minute: minute, Class: traffic.Observed, App: "Safari", Host: "api.example.com", RegistrableDomain: "example.com", Upload: 3, Download: 2,
	}}); err != nil {
		t.Fatalf("upsert traffic: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	store, err = storage.Open(databasePath)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	summary, err := store.Summary(minute, minute.Add(time.Minute))
	if err != nil {
		t.Fatalf("query summary: %v", err)
	}
	if summary.Upload != (storage.AttributionTotals{Observed: 23, Residual: 5, Total: 28}) {
		t.Fatalf("upload summary = %+v", summary.Upload)
	}
	if summary.Download != (storage.AttributionTotals{Observed: 82, Residual: 15, Total: 97}) {
		t.Fatalf("download summary = %+v", summary.Download)
	}
	if summary.Total.Total != summary.Total.Observed+summary.Total.Residual+summary.Total.GapRecovered {
		t.Fatalf("total identity failed: %+v", summary.Total)
	}
	if len(summary.Apps) != 1 || summary.Apps[0].Name != "Safari" || summary.Apps[0].Total != 105 {
		t.Fatalf("app leaders = %+v", summary.Apps)
	}
	if len(summary.Hosts) != 1 || summary.Hosts[0].Name != "api.example.com" || summary.Hosts[0].Total != 105 {
		t.Fatalf("host leaders = %+v", summary.Hosts)
	}
	if summary.Coverage != float64(105)/float64(125) {
		t.Fatalf("coverage = %f, want %f", summary.Coverage, float64(105)/float64(125))
	}

	next, err := store.Summary(minute.Add(time.Minute), minute.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("query next minute: %v", err)
	}
	if next.Total.GapRecovered != 20 || next.Total.Total != 20 || next.Coverage != 0 {
		t.Fatalf("half-open next-minute summary = %+v", next)
	}
	fractional, err := store.Summary(minute.Add(500*time.Millisecond), minute.Add(time.Minute))
	if err != nil {
		t.Fatalf("query fractional boundary: %v", err)
	}
	if fractional.Total.Total != 0 {
		t.Fatalf("fractional half-open start included the preceding minute: %+v", fractional)
	}
}

func TestAddTrafficRejectsInvalidAttributionAtomically(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	minute := time.Date(2026, 8, 14, 9, 30, 0, 0, time.UTC)
	err = store.AddTraffic([]traffic.Record{
		{Minute: minute, Class: traffic.Observed, App: "Safari", Host: "example.com", Upload: 10},
		{Minute: minute, Class: traffic.Residual, App: "must-not-have-a-dimension", Upload: 5},
	})
	if err == nil {
		t.Fatal("invalid residual attribution was accepted")
	}
	summary, queryErr := store.Summary(minute, minute.Add(time.Minute))
	if queryErr != nil {
		t.Fatalf("query summary: %v", queryErr)
	}
	if summary.Total.Total != 0 {
		t.Fatalf("failed transaction persisted traffic: %+v", summary)
	}
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
