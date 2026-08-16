package storage_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/continuity"
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
	if got, want := info.SchemaVersion, 3; got != want {
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

func TestCollectionGapRecoveryIsAtomicAndStoredOnlyInTheReconnectMinute(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	if _, err := store.AcceptSample(continuity.State{SampledAt: start, UploadTotal: 100, DownloadTotal: 200}, nil); err != nil {
		t.Fatalf("accept baseline: %v", err)
	}
	if err := store.OpenCollectionGap("disconnected"); err != nil {
		t.Fatalf("open Collection gap: %v", err)
	}
	reconnectedAt := start.Add(5 * time.Minute)
	result, err := store.AcceptSample(continuity.State{SampledAt: reconnectedAt, UploadTotal: 130, DownloadTotal: 260}, nil)
	if err != nil {
		t.Fatalf("accept reconnection: %v", err)
	}
	if result.Gap == nil || result.Gap.Disposition != continuity.DispositionRecovered || result.Gap.RecoveredUpload != 30 || result.Gap.RecoveredDownload != 60 {
		t.Fatalf("recovery result = %+v", result)
	}

	before, err := store.Summary(start, reconnectedAt)
	if err != nil {
		t.Fatalf("query gap interval: %v", err)
	}
	if before.Total.GapRecovered != 0 {
		t.Fatalf("recovered traffic was interpolated into the gap: %+v", before.Total)
	}
	after, err := store.Summary(reconnectedAt, reconnectedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("query reconnect minute: %v", err)
	}
	if after.Upload.GapRecovered != 30 || after.Download.GapRecovered != 60 || after.Total.Total != 90 {
		t.Fatalf("reconnect-minute summary = %+v", after)
	}

	gaps, err := store.CollectionGaps(start, reconnectedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("query Collection gaps: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Open || !gaps[0].StartedAt.Equal(start) || gaps[0].EndedAt == nil || !gaps[0].EndedAt.Equal(reconnectedAt) || gaps[0].Reason != "disconnected" {
		t.Fatalf("stored Collection gaps = %+v", gaps)
	}
}

func TestCounterResetCreatesANewBaselineWithoutRecoveredTraffic(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)
	if _, err := store.AcceptSample(continuity.State{SampledAt: start, UploadTotal: 900, DownloadTotal: 1_200}, nil); err != nil {
		t.Fatalf("accept baseline: %v", err)
	}
	resetAt := start.Add(time.Second)
	result, err := store.AcceptSample(continuity.State{SampledAt: resetAt, UploadTotal: 3, DownloadTotal: 7}, nil)
	if err != nil {
		t.Fatalf("accept reset: %v", err)
	}
	if result.Gap == nil || result.Gap.Disposition != continuity.DispositionReset || result.Gap.RecoveredUpload != 0 || result.Gap.RecoveredDownload != 0 {
		t.Fatalf("reset acceptance = %+v", result)
	}

	gaps, err := store.CollectionGaps(start.Add(-time.Second), resetAt.Add(time.Second))
	if err != nil {
		t.Fatalf("query reset gap: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Reason != "counter_reset" || gaps[0].EndedAt == nil || !gaps[0].EndedAt.Equal(resetAt) {
		t.Fatalf("reset gaps = %+v", gaps)
	}
	summary, err := store.Summary(start, resetAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("query reset summary: %v", err)
	}
	if summary.Total.Total != 0 {
		t.Fatalf("counter reset produced traffic: %+v", summary.Total)
	}
}

func TestOpenCollectionGapIsIdempotentAcrossStoreRestart(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	start := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	if _, err := store.AcceptSample(continuity.State{SampledAt: start, UploadTotal: 100, DownloadTotal: 200}, nil); err != nil {
		t.Fatalf("accept baseline: %v", err)
	}
	if err := store.OpenCollectionGap("disconnected"); err != nil {
		t.Fatalf("open initial gap: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	store, err = storage.Open(databasePath)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.OpenCollectionGap("monitor_restart"); err != nil {
		t.Fatalf("resume gap after restart: %v", err)
	}
	gaps, err := store.CollectionGaps(start.Add(-time.Second), start.Add(time.Hour))
	if err != nil {
		t.Fatalf("query open gap: %v", err)
	}
	if len(gaps) != 1 || !gaps[0].Open || gaps[0].Reason != "monitor_restart" || !gaps[0].StartedAt.Equal(start) {
		t.Fatalf("resumed gaps = %+v", gaps)
	}

	reconnectedAt := start.Add(10 * time.Minute)
	result, err := store.AcceptSample(continuity.State{SampledAt: reconnectedAt, UploadTotal: 140, DownloadTotal: 260}, nil)
	if err != nil {
		t.Fatalf("accept restarted collector sample: %v", err)
	}
	if result.Gap == nil || result.Gap.ID != gaps[0].ID || result.Gap.RecoveredUpload != 40 || result.Gap.RecoveredDownload != 60 {
		t.Fatalf("restarted acceptance = %+v", result)
	}
	if err := store.OpenCollectionGap("disconnected"); err != nil {
		t.Fatalf("open second gap: %v", err)
	}
	if err := store.OpenCollectionGap("authentication_failed"); err != nil {
		t.Fatalf("update second gap: %v", err)
	}
	gaps, err = store.CollectionGaps(start.Add(-time.Second), reconnectedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("query all gaps: %v", err)
	}
	if len(gaps) != 2 || !gaps[0].Open || gaps[0].Reason != "authentication_failed" || gaps[1].ID != result.Gap.ID {
		t.Fatalf("idempotent gaps = %+v", gaps)
	}
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

func TestDimensionsSearchesRetainedCanonicalValuesWithoutInventingIPDomains(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	minute := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	if err := store.AddTraffic([]traffic.Record{
		{Minute: minute, Class: traffic.Observed, App: "Safari", Host: "api.example.com", RegistrableDomain: "example.com", Upload: 10, Download: 20},
		{Minute: minute, Class: traffic.Observed, App: "curl", Host: "cdn.example.com", RegistrableDomain: "example.com", Upload: 20, Download: 10},
		{Minute: minute, Class: traffic.Observed, App: "Mail", Host: "mail.example.net", RegistrableDomain: "example.net", Upload: 5, Download: 5},
		{Minute: minute, Class: traffic.Observed, App: "ping", Host: "192.0.2.10", RegistrableDomain: "", Upload: 1, Download: 1},
	}); err != nil {
		t.Fatalf("seed dimensions: %v", err)
	}

	dimensions, err := store.Dimensions("EXAMPLE", 10)
	if err != nil {
		t.Fatalf("query dimensions: %v", err)
	}
	if got, want := strings.Join(dimensions.Apps, ","), ""; got != want {
		t.Fatalf("matching Apps = %q, want %q", got, want)
	}
	if got, want := strings.Join(dimensions.Hosts, ","), "api.example.com,cdn.example.com,mail.example.net"; got != want {
		t.Fatalf("matching Hosts = %q, want %q", got, want)
	}
	if got, want := strings.Join(dimensions.Domains, ","), "example.com,example.net"; got != want {
		t.Fatalf("matching domains = %q, want %q", got, want)
	}
	limited, err := store.Dimensions("", 2)
	if err != nil {
		t.Fatalf("query limited dimensions: %v", err)
	}
	if got, want := strings.Join(limited.Hosts, ","), "192.0.2.10,api.example.com"; got != want {
		t.Fatalf("limited Hosts = %q, want %q", got, want)
	}
	if got, want := strings.Join(limited.Domains, ","), "example.com,example.net"; got != want {
		t.Fatalf("limited domains = %q, want %q", got, want)
	}
}

func TestFilteredSummaryAndSeriesUseORWithinDimensionsAndANDAcrossThem(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	if err := store.AddTraffic([]traffic.Record{
		{Minute: start, Class: traffic.Observed, App: "Safari", Host: "api.example.com", RegistrableDomain: "example.com", Upload: 10, Download: 20},
		{Minute: start, Class: traffic.Observed, App: "curl", Host: "cdn.example.com", RegistrableDomain: "example.com", Upload: 30, Download: 40},
		{Minute: start, Class: traffic.Observed, App: "Mail", Host: "mail.example.com", RegistrableDomain: "example.com", Upload: 50, Download: 60},
		{Minute: start.Add(time.Minute), Class: traffic.Observed, App: "Safari", Host: "api.example.net", RegistrableDomain: "example.net", Upload: 70, Download: 80},
		{Minute: start, Class: traffic.Residual, Upload: 100, Download: 200},
		{Minute: start.Add(time.Minute), Class: traffic.GapRecovered, Upload: 300, Download: 400},
	}); err != nil {
		t.Fatalf("seed filtered traffic: %v", err)
	}
	filter := storage.TrafficFilter{Apps: []string{"Safari", "curl"}, Domains: []string{"example.com"}}

	summary, err := store.FilteredSummary(start, start.Add(2*time.Minute), filter)
	if err != nil {
		t.Fatalf("query filtered summary: %v", err)
	}
	if summary.Scope != storage.ScopeObserved || summary.Upload != (storage.AttributionTotals{Observed: 40, Total: 40}) || summary.Download != (storage.AttributionTotals{Observed: 60, Total: 60}) {
		t.Fatalf("filtered summary = %+v", summary)
	}
	if summary.Coverage != 1 || len(summary.Apps) != 2 || len(summary.Hosts) != 2 {
		t.Fatalf("filtered summary metadata/leaders = %+v", summary)
	}

	series, err := store.Series(storage.SeriesOptions{
		Start: start, End: start.Add(2 * time.Minute), Granularity: storage.GranularityMinute, Location: time.UTC, Filter: filter,
	})
	if err != nil {
		t.Fatalf("query filtered series: %v", err)
	}
	if series.Scope != storage.ScopeObserved || len(series.Points) != 1 || series.Points[0].Total != (storage.AttributionTotals{Observed: 100, Total: 100}) {
		t.Fatalf("filtered series = %+v", series)
	}

	empty, err := store.FilteredSummary(start, start.Add(2*time.Minute), storage.TrafficFilter{Hosts: []string{"absent.example"}})
	if err != nil {
		t.Fatalf("query empty filtered summary: %v", err)
	}
	if empty.Scope != storage.ScopeObserved || empty.Total.Total != 0 || len(empty.Apps) != 0 || len(empty.Hosts) != 0 {
		t.Fatalf("empty filtered summary = %+v", empty)
	}
}

func TestRankingsSupportEveryDimensionDirectionAndDeterministicTies(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	if err := store.AddTraffic([]traffic.Record{
		{Minute: start, Class: traffic.Observed, App: "Safari", Host: "api.example.com", RegistrableDomain: "example.com", Upload: 10, Download: 30},
		{Minute: start.Add(time.Minute), Class: traffic.Observed, App: "Safari", Host: "cdn.example.com", RegistrableDomain: "example.com", Upload: 10, Download: 10},
		{Minute: start, Class: traffic.Observed, App: "curl", Host: "api.example.net", RegistrableDomain: "example.net", Upload: 20, Download: 20},
		{Minute: start, Class: traffic.Observed, App: "ping", Host: "192.0.2.10", Upload: 100, Download: 100},
	}); err != nil {
		t.Fatalf("seed rankings: %v", err)
	}

	apps, err := store.Rankings(storage.RankingOptions{Start: start, End: start.Add(2 * time.Minute), Dimension: storage.DimensionApp, Direction: storage.DirectionTotal, Limit: 2, Filter: storage.TrafficFilter{Domains: []string{"example.com", "example.net"}}})
	if err != nil {
		t.Fatalf("rank Apps: %v", err)
	}
	if len(apps.Items) != 2 || apps.Items[0].Name != "Safari" || apps.Items[0].Total != 60 || apps.Items[1].Name != "curl" {
		t.Fatalf("App rankings = %+v", apps)
	}
	hosts, err := store.Rankings(storage.RankingOptions{Start: start, End: start.Add(2 * time.Minute), Dimension: storage.DimensionHost, Direction: storage.DirectionUpload, Limit: 10, Filter: storage.TrafficFilter{Domains: []string{"example.com", "example.net"}}})
	if err != nil {
		t.Fatalf("rank Hosts: %v", err)
	}
	if len(hosts.Items) != 3 || hosts.Items[0].Name != "api.example.net" || hosts.Items[1].Name != "api.example.com" || hosts.Items[2].Name != "cdn.example.com" {
		t.Fatalf("Host upload rankings = %+v", hosts)
	}
	domains, err := store.Rankings(storage.RankingOptions{Start: start, End: start.Add(2 * time.Minute), Dimension: storage.DimensionDomain, Direction: storage.DirectionDownload, Limit: 10})
	if err != nil {
		t.Fatalf("rank domains: %v", err)
	}
	if domains.Scope != storage.ScopeObserved || len(domains.Items) != 2 || domains.Items[0].Name != "example.com" || domains.Items[1].Name != "example.net" {
		t.Fatalf("domain rankings = %+v", domains)
	}
}

func TestSeriesKeepsRepeatedDaylightSavingHoursDistinctAndPreservesTotals(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load test time zone: %v", err)
	}
	starts := []time.Time{
		time.Date(2026, 11, 1, 4, 30, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 7, 30, 0, 0, time.UTC),
	}
	for index, minute := range starts {
		if err := store.AddTraffic([]traffic.Record{
			{Minute: minute, Class: traffic.Observed, App: "Safari", Host: "example.com", Upload: int64(index + 1), Download: 10},
			{Minute: minute, Class: traffic.Residual, Upload: 2, Download: 3},
			{Minute: minute, Class: traffic.GapRecovered, Upload: 4, Download: 5},
		}); err != nil {
			t.Fatalf("seed minute %d: %v", index, err)
		}
	}

	result, err := store.Series(storage.SeriesOptions{
		Start:       time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC),
		End:         time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC),
		Granularity: storage.GranularityHour,
		Location:    zone,
	})
	if err != nil {
		t.Fatalf("query hourly series: %v", err)
	}
	if result.Granularity != storage.GranularityHour || len(result.Points) != 4 {
		t.Fatalf("hourly series = %+v", result)
	}
	wantStarts := []string{
		"2026-11-01T00:00:00-04:00",
		"2026-11-01T01:00:00-04:00",
		"2026-11-01T01:00:00-05:00",
		"2026-11-01T02:00:00-05:00",
	}
	wantTotals := []int64{25, 26, 27, 28}
	for index, point := range result.Points {
		if got := point.Start.Format(time.RFC3339); got != wantStarts[index] {
			t.Fatalf("point %d start = %s, want %s", index, got, wantStarts[index])
		}
		if point.Upload.Total != point.Upload.Observed+point.Upload.Residual+point.Upload.GapRecovered || point.Download.Total != point.Download.Observed+point.Download.Residual+point.Download.GapRecovered || point.Total.Total != point.Total.Observed+point.Total.Residual+point.Total.GapRecovered {
			t.Fatalf("point %d total identity failed: %+v", index, point)
		}
		if point.Total.Total != wantTotals[index] {
			t.Fatalf("point %d total = %d, want %d", index, point.Total.Total, wantTotals[index])
		}
	}
}

func TestSeriesAlignsDayBucketsAcrossDaylightSavingTransitions(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load test time zone: %v", err)
	}
	minutes := []time.Time{
		time.Date(2026, 3, 8, 4, 30, 0, 0, time.UTC),
		time.Date(2026, 3, 8, 5, 30, 0, 0, time.UTC),
		time.Date(2026, 3, 9, 3, 30, 0, 0, time.UTC),
		time.Date(2026, 3, 9, 4, 30, 0, 0, time.UTC),
	}
	for _, minute := range minutes {
		if err := store.AddTraffic([]traffic.Record{{Minute: minute, Class: traffic.Residual, Upload: 1, Download: 2}}); err != nil {
			t.Fatalf("seed day traffic: %v", err)
		}
	}
	result, err := store.Series(storage.SeriesOptions{
		Start:       time.Date(2026, 3, 8, 4, 0, 0, 0, time.UTC),
		End:         time.Date(2026, 3, 9, 5, 0, 0, 0, time.UTC),
		Granularity: storage.GranularityDay,
		Location:    zone,
	})
	if err != nil {
		t.Fatalf("query daily series: %v", err)
	}
	wantStarts := []string{
		"2026-03-07T00:00:00-05:00",
		"2026-03-08T00:00:00-05:00",
		"2026-03-09T00:00:00-04:00",
	}
	wantTotals := []int64{3, 6, 3}
	if result.Granularity != storage.GranularityDay || len(result.Points) != len(wantStarts) {
		t.Fatalf("daily series = %+v", result)
	}
	for index, point := range result.Points {
		if got := point.Start.Format(time.RFC3339); got != wantStarts[index] || point.Total.Total != wantTotals[index] {
			t.Fatalf("point %d = %s total %d, want %s total %d", index, got, point.Total.Total, wantStarts[index], wantTotals[index])
		}
	}
}

func TestSeriesAlignsHoursAcrossHalfHourDaylightSavingTransitions(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	zone, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Fatalf("load test time zone: %v", err)
	}
	minutes := []time.Time{
		time.Date(2026, 4, 4, 14, 45, 0, 0, time.UTC),
		time.Date(2026, 4, 4, 15, 15, 0, 0, time.UTC),
		time.Date(2026, 10, 3, 15, 45, 0, 0, time.UTC),
	}
	for _, minute := range minutes {
		if err := store.AddTraffic([]traffic.Record{{Minute: minute, Class: traffic.Residual, Upload: 1}}); err != nil {
			t.Fatalf("seed half-hour transition traffic: %v", err)
		}
	}
	result, err := store.Series(storage.SeriesOptions{
		Start:       minutes[0].Add(-time.Minute),
		End:         minutes[2].Add(time.Minute),
		Granularity: storage.GranularityHour,
		Location:    zone,
	})
	if err != nil {
		t.Fatalf("query half-hour transition series: %v", err)
	}
	want := []string{
		"2026-04-05T01:00:00+11:00",
		"2026-04-05T01:30:00+10:30",
		"2026-10-04T02:30:00+11:00",
	}
	if len(result.Points) != len(want) {
		t.Fatalf("half-hour transition points = %+v", result.Points)
	}
	for index, point := range result.Points {
		if got := point.Start.Format(time.RFC3339); got != want[index] {
			t.Fatalf("half-hour point %d = %s, want %s", index, got, want[index])
		}
	}
}

func TestSeriesUsesTheFirstValidInstantOfADayWhenMidnightIsSkipped(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	zone, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("load test time zone: %v", err)
	}
	minute := time.Date(2018, 11, 4, 3, 30, 0, 0, time.UTC)
	if err := store.AddTraffic([]traffic.Record{{Minute: minute, Class: traffic.Residual, Download: 1}}); err != nil {
		t.Fatalf("seed midnight transition traffic: %v", err)
	}
	result, err := store.Series(storage.SeriesOptions{Start: minute.Add(-time.Minute), End: minute.Add(time.Minute), Granularity: storage.GranularityDay, Location: zone})
	if err != nil {
		t.Fatalf("query midnight transition series: %v", err)
	}
	if len(result.Points) != 1 || result.Points[0].Start.Format(time.RFC3339) != "2018-11-04T01:00:00-02:00" {
		t.Fatalf("midnight transition point = %+v", result.Points)
	}
}

func TestMigrationPreservesPopulatedVersionTwoMinuteHistory(t *testing.T) {
	t.Parallel()

	dataDirectory := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDirectory, 0o700); err != nil {
		t.Fatalf("create version two data directory: %v", err)
	}
	databasePath := filepath.Join(dataDirectory, "traffic.db")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open version two fixture: %v", err)
	}
	minute := time.Date(2019, 6, 7, 8, 9, 0, 0, time.UTC)
	if _, err := database.Exec(`
		CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
		INSERT INTO schema_migrations(version, applied_at) VALUES (1, '2019-01-01T00:00:00Z'), (2, '2019-01-01T00:00:01Z');
		CREATE TABLE apps (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);
		CREATE TABLE endpoints (id INTEGER PRIMARY KEY, host TEXT NOT NULL, registrable_domain TEXT NOT NULL DEFAULT '', UNIQUE(host, registrable_domain));
		CREATE TABLE minute_traffic (
			minute INTEGER NOT NULL,
			attribution_class TEXT NOT NULL,
			app_id INTEGER REFERENCES apps(id),
			endpoint_id INTEGER REFERENCES endpoints(id),
			upload_bytes INTEGER NOT NULL,
			download_bytes INTEGER NOT NULL
		);
		INSERT INTO minute_traffic(minute, attribution_class, upload_bytes, download_bytes)
		VALUES (?, 'residual', 7, 11);
	`, minute.Unix()); err != nil {
		_ = database.Close()
		t.Fatalf("create populated version two fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close version two fixture: %v", err)
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		t.Fatalf("secure version two fixture: %v", err)
	}

	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("migrate populated version two database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.Info().SchemaVersion != 3 {
		t.Fatalf("migrated schema version = %d, want 3", store.Info().SchemaVersion)
	}
	series, err := store.Series(storage.SeriesOptions{Start: minute, End: minute.Add(time.Minute), Granularity: storage.GranularityMinute, Location: time.UTC})
	if err != nil {
		t.Fatalf("query migrated minute history: %v", err)
	}
	if len(series.Points) != 1 || series.Points[0].Upload.Total != 7 || series.Points[0].Download.Total != 11 || series.Points[0].Total.Total != 18 {
		t.Fatalf("migrated minute history = %+v", series.Points)
	}
}

func TestSeriesAutoBoundsPointsWithoutChangingPermanentMinuteHistory(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2020, 1, 2, 3, 0, 0, 0, time.UTC)
	records := make([]traffic.Record, 0, storage.AutoPointLimit+1)
	for index := 0; index <= storage.AutoPointLimit; index++ {
		records = append(records, traffic.Record{Minute: start.Add(time.Duration(index) * time.Minute), Class: traffic.Residual, Upload: 1, Download: 2})
	}
	if err := store.AddTraffic(records); err != nil {
		t.Fatalf("seed permanent minute history: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close permanent history database: %v", err)
	}
	store, err = storage.Open(databasePath)
	if err != nil {
		t.Fatalf("reopen permanent history database: %v", err)
	}
	end := start.Add(time.Duration(storage.AutoPointLimit+1) * time.Minute)
	automatic, err := store.Series(storage.SeriesOptions{Start: start, End: end, Granularity: storage.GranularityAuto, Location: time.UTC})
	if err != nil {
		t.Fatalf("query automatic series: %v", err)
	}
	if automatic.Granularity != storage.GranularityHour || len(automatic.Points) > storage.AutoPointLimit {
		t.Fatalf("automatic series = granularity %q, points %d", automatic.Granularity, len(automatic.Points))
	}
	minuteSeries, err := store.Series(storage.SeriesOptions{Start: start, End: end, Granularity: storage.GranularityMinute, Location: time.UTC})
	if err != nil {
		t.Fatalf("query minute series: %v", err)
	}
	if len(minuteSeries.Points) != storage.AutoPointLimit+1 {
		t.Fatalf("minute points = %d, want %d", len(minuteSeries.Points), storage.AutoPointLimit+1)
	}
	summary, err := store.Summary(start, end)
	if err != nil {
		t.Fatalf("query permanent history summary: %v", err)
	}
	if summary.Upload.Total != 401 || summary.Download.Total != 802 || summary.Total.Total != 1_203 {
		t.Fatalf("permanent history changed after rollups: %+v", summary)
	}
}

func TestSeriesAutoReportsWhenDailyHistoryExceedsThePointLimit(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2018, 1, 1, 12, 0, 0, 0, time.UTC)
	records := make([]traffic.Record, 0, storage.AutoPointLimit+1)
	for index := 0; index <= storage.AutoPointLimit; index++ {
		records = append(records, traffic.Record{Minute: start.AddDate(0, 0, index), Class: traffic.Residual, Upload: 1})
	}
	if err := store.AddTraffic(records); err != nil {
		t.Fatalf("seed daily history: %v", err)
	}

	_, err = store.Series(storage.SeriesOptions{
		Start:       start,
		End:         start.AddDate(0, 0, storage.AutoPointLimit+1),
		Granularity: storage.GranularityAuto,
		Location:    time.UTC,
	})
	if !errors.Is(err, storage.ErrAutoPointLimitExceeded) {
		t.Fatalf("automatic oversized series error = %v", err)
	}
}

func TestSeriesReadsContinueWhileWALCollectionWrites(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	if err := store.AddTraffic([]traffic.Record{{Minute: start, Class: traffic.Residual, Upload: 1, Download: 2}}); err != nil {
		t.Fatalf("seed traffic: %v", err)
	}

	var writers sync.WaitGroup
	writers.Add(1)
	writeErrors := make(chan error, 1)
	go func() {
		defer writers.Done()
		for index := 1; index <= 40; index++ {
			if err := store.AddTraffic([]traffic.Record{{Minute: start.Add(time.Duration(index) * time.Minute), Class: traffic.Residual, Upload: 1, Download: 2}}); err != nil {
				writeErrors <- err
				return
			}
		}
	}()

	for index := 0; index < 40; index++ {
		series, err := store.Series(storage.SeriesOptions{Start: start, End: start.Add(time.Hour), Granularity: storage.GranularityMinute, Location: time.UTC})
		if err != nil {
			t.Fatalf("read series during collection: %v", err)
		}
		for _, point := range series.Points {
			if point.Total.Total != 3 {
				t.Fatalf("read partial traffic transaction: %+v", point)
			}
		}
	}
	writers.Wait()
	select {
	case err := <-writeErrors:
		t.Fatalf("collect traffic concurrently: %v", err)
	default:
	}
	series, err := store.Series(storage.SeriesOptions{Start: start, End: start.Add(time.Hour), Granularity: storage.GranularityMinute, Location: time.UTC})
	if err != nil {
		t.Fatalf("read final series: %v", err)
	}
	if len(series.Points) != 41 {
		t.Fatalf("final series points = %d, want 41", len(series.Points))
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
