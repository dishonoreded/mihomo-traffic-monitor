package traffic_test

import (
	"testing"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/traffic"
)

func TestCanonicalIdentityUsesControllerPrecedenceAndPublicSuffixList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		connection traffic.Connection
		want       traffic.Identity
	}{
		{
			name: "sniffed domain wins and is normalized",
			connection: traffic.Connection{
				Process: "", SniffHost: "API.Example.CO.UK.", Host: "ignored.example", DestinationIP: "203.0.113.10",
			},
			want: traffic.Identity{App: "Unknown process", Host: "api.example.co.uk", RegistrableDomain: "example.co.uk"},
		},
		{
			name:       "declared domain is used without sniffed host",
			connection: traffic.Connection{Process: "Safari", Host: "WWW.Example.COM."},
			want:       traffic.Identity{App: "Safari", Host: "www.example.com", RegistrableDomain: "example.com"},
		},
		{
			name:       "destination IP has no registrable domain",
			connection: traffic.Connection{Process: "curl", DestinationIP: "203.0.113.10"},
			want:       traffic.Identity{App: "curl", Host: "203.0.113.10"},
		},
		{
			name:       "public suffix alone has no registrable domain",
			connection: traffic.Connection{Process: "curl", Host: "co.uk"},
			want:       traffic.Identity{App: "curl", Host: "co.uk"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := traffic.CanonicalIdentity(test.connection); got != test.want {
				t.Fatalf("identity = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestCandidateDeltasAreNonNegativeForConnectionLifecycles(t *testing.T) {
	t.Parallel()

	previous := []traffic.Connection{
		{ID: "survives", Upload: 100, Download: 200, Process: "Safari", Host: "example.com"},
		{ID: "vanishes", Upload: 80, Download: 90, Process: "curl", Host: "old.example"},
		{ID: "resets", Upload: 500, Download: 600, Process: "Mail", Host: "mail.example"},
		{ID: "reused", Upload: 700, Download: 800, Process: "Old", Host: "old.example"},
	}
	current := []traffic.Connection{
		{ID: "survives", Upload: 130, Download: 260, Process: "Safari", Host: "example.com"},
		{ID: "new", Upload: 20, Download: 40, Process: "curl", Host: "new.example"},
		{ID: "resets", Upload: 5, Download: 7, Process: "Mail", Host: "mail.example"},
		{ID: "reused", Upload: 11, Download: 13, Process: "New", Host: "new.example"},
	}

	got := traffic.CandidateDeltas(previous, current)
	want := map[string][2]int64{
		"Safari|example.com": {30, 60},
		"curl|new.example":   {20, 40},
		"Mail|mail.example":  {5, 7},
		"New|new.example":    {11, 13},
	}
	if len(got) != len(want) {
		t.Fatalf("candidate count = %d, want %d: %+v", len(got), len(want), got)
	}
	for _, delta := range got {
		if delta.Upload < 0 || delta.Download < 0 {
			t.Fatalf("negative candidate delta: %+v", delta)
		}
		key := delta.Identity.App + "|" + delta.Identity.Host
		if pair, ok := want[key]; !ok || pair != [2]int64{delta.Upload, delta.Download} {
			t.Fatalf("candidate %q = (%d, %d), want %v", key, delta.Upload, delta.Download, pair)
		}
	}
}

func TestReconcilerAbsorbsOnePeriodSkewAndExpiresResidual(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 8, 14, 10, 0, 58, 0, time.UTC)
	reconciler := traffic.NewReconciler()
	baseline := traffic.Sample{At: start, UploadTotal: 1_000, DownloadTotal: 2_000}
	if records := reconciler.Add(baseline); len(records) != 0 {
		t.Fatalf("baseline produced records: %+v", records)
	}

	// Connection counters arrive one sample before their corresponding global growth.
	first := traffic.Sample{
		At: start.Add(time.Second), UploadTotal: 1_000, DownloadTotal: 2_000,
		Connections: []traffic.Connection{{ID: "one", Upload: 30, Download: 70, Process: "Safari", Host: "Example.COM."}},
	}
	if records := reconciler.Add(first); len(records) != 0 {
		t.Fatalf("unmatched candidates produced records: %+v", records)
	}

	second := traffic.Sample{
		At: start.Add(2 * time.Second), UploadTotal: 1_040, DownloadTotal: 2_100,
		Connections: first.Connections,
	}
	records := reconciler.Add(second)
	assertTraffic(t, records, start.Add(2*time.Second).Truncate(time.Minute), traffic.Observed, "Safari", "example.com", 30, 70)

	// The unmatched authoritative remainder stays pending for one more period.
	third := traffic.Sample{At: start.Add(3 * time.Second), UploadTotal: 1_040, DownloadTotal: 2_100, Connections: first.Connections}
	if records := reconciler.Add(third); len(records) != 0 {
		t.Fatalf("remainder expired too early: %+v", records)
	}
	fourth := traffic.Sample{At: start.Add(4 * time.Second), UploadTotal: 1_040, DownloadTotal: 2_100, Connections: first.Connections}
	records = reconciler.Add(fourth)
	assertTraffic(t, records, start.Add(2*time.Second).Truncate(time.Minute), traffic.Residual, "", "", 10, 30)
}

func TestReconcilerCapsObservedAndPreservesGlobalDirectionalTotalsAcrossMinuteBoundary(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 8, 14, 10, 0, 59, 0, time.UTC)
	reconciler := traffic.NewReconciler()
	reconciler.Add(traffic.Sample{
		At: start, UploadTotal: 500, DownloadTotal: 500,
		Connections: []traffic.Connection{{ID: "one", Upload: 100, Download: 100, Process: "Safari", Host: "example.com"}},
	})
	records := reconciler.Add(traffic.Sample{
		At: start.Add(time.Second), UploadTotal: 510, DownloadTotal: 520,
		Connections: []traffic.Connection{{ID: "one", Upload: 140, Download: 150, Process: "Safari", Host: "example.com"}},
	})
	assertTraffic(t, records, start.Add(time.Second).Truncate(time.Minute), traffic.Observed, "Safari", "example.com", 10, 20)

	// Excess connection growth is discarded; a global counter decrease starts a clean baseline.
	if records := reconciler.Add(traffic.Sample{
		At: start.Add(2 * time.Second), UploadTotal: 5, DownloadTotal: 8,
		Connections: []traffic.Connection{{ID: "one", Upload: 2, Download: 3, Process: "Safari", Host: "example.com"}},
	}); len(records) != 0 {
		t.Fatalf("counter reset produced traffic: %+v", records)
	}
}

func assertTraffic(t *testing.T, records []traffic.Record, minute time.Time, class traffic.Class, app, host string, upload, download int64) {
	t.Helper()
	for _, record := range records {
		if record.Minute.Equal(minute) && record.Class == class && record.App == app && record.Host == host && record.Upload == upload && record.Download == download {
			return
		}
	}
	t.Fatalf("missing traffic record minute=%s class=%s app=%q host=%q upload=%d download=%d in %+v", minute, class, app, host, upload, download, records)
}
