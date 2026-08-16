package collector_test

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/collector"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/continuity"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/storage"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/testcontroller"
)

func TestCollectorPersistsReconciledTrafficAcrossControllerScenarios(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 8, 14, 10, 0, 58, 0, time.UTC)
	times := []time.Time{
		start,
		start.Add(time.Second),
		start.Add(2 * time.Second),
		start.Add(3 * time.Second),
		start.Add(4 * time.Second),
		start.Add(5 * time.Second),
	}
	var clockIndex atomic.Int64
	allSent := make(chan struct{})
	snapshots := []testcontroller.TrafficSnapshot{
		{UploadTotal: 100, DownloadTotal: 200, Connections: []testcontroller.TrafficConnection{{ID: "one", Upload: 10, Download: 20, Process: "Safari", Host: "Example.COM."}}},
		{UploadTotal: 130, DownloadTotal: 260, Connections: []testcontroller.TrafficConnection{{ID: "one", Upload: 40, Download: 80, Process: "Safari", Host: "Example.COM."}}},
		{UploadTotal: 150, DownloadTotal: 300, Connections: []testcontroller.TrafficConnection{}},
		{UploadTotal: 150, DownloadTotal: 300, Connections: []testcontroller.TrafficConnection{{ID: "two", Upload: 10, Download: 10, Process: "curl", SniffHost: "API.Example.ORG."}}},
		{UploadTotal: 155, DownloadTotal: 305, Connections: []testcontroller.TrafficConnection{{ID: "two", Upload: 15, Download: 15, Process: "curl", SniffHost: "API.Example.ORG."}}},
		{UploadTotal: 2, DownloadTotal: 3, Connections: []testcontroller.TrafficConnection{{ID: "two", Upload: 1, Download: 1, Process: "curl", SniffHost: "API.Example.ORG."}}},
	}
	controller := testcontroller.Start(t, testcontroller.Options{
		OnConnections: func(request *http.Request, connection *websocket.Conn) {
			for _, snapshot := range snapshots {
				if err := testcontroller.WriteTrafficSnapshot(request.Context(), connection, snapshot); err != nil {
					t.Errorf("write traffic snapshot: %v", err)
					return
				}
			}
			close(allSent)
			<-request.Context().Done()
		},
	})
	monitor := collector.New(collector.Config{
		ControllerURL: controller.URL, SampleInterval: 20 * time.Millisecond, TrafficSink: store,
		Now: func() time.Time {
			index := clockIndex.Add(1) - 1
			if index >= int64(len(times)) {
				return times[len(times)-1]
			}
			return times[index]
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go monitor.Run(ctx)
	select {
	case <-allSent:
	case <-time.After(3 * time.Second):
		t.Fatal("fake Controller did not send all snapshots")
	}

	var summary storage.TrafficSummary
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		summary, err = store.Summary(start.Truncate(time.Minute), start.Add(2*time.Minute).Truncate(time.Minute))
		if err != nil {
			t.Fatalf("query collected summary: %v", err)
		}
		if summary.Total.Total == 160 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if summary.Upload != (storage.AttributionTotals{Observed: 45, Residual: 10, Total: 55}) {
		t.Fatalf("upload summary = %+v", summary.Upload)
	}
	if summary.Download != (storage.AttributionTotals{Observed: 75, Residual: 30, Total: 105}) {
		t.Fatalf("download summary = %+v", summary.Download)
	}
	if len(summary.Apps) != 2 || summary.Apps[0].Name != "Safari" || summary.Apps[1].Name != "curl" {
		t.Fatalf("App leaders = %+v", summary.Apps)
	}
	if len(summary.Hosts) != 2 || summary.Hosts[0].Name != "example.com" || summary.Hosts[1].Name != "api.example.org" {
		t.Fatalf("Host leaders = %+v", summary.Hosts)
	}
}

func TestCollectorRecoversDisconnectedGlobalGrowthExactlyOnce(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)
	times := []time.Time{start, start.Add(time.Second), start.Add(5 * time.Minute)}
	var clockIndex atomic.Int64
	reconnected := make(chan struct{})
	controller := testcontroller.Start(t, testcontroller.Options{
		OnConnections: func(request *http.Request, connection *websocket.Conn) {
			switch stream := int(clockIndex.Load()); stream {
			case 0:
				if err := testcontroller.WriteTrafficSnapshot(request.Context(), connection, testcontroller.TrafficSnapshot{UploadTotal: 100, DownloadTotal: 200, Connections: []testcontroller.TrafficConnection{}}); err != nil {
					t.Errorf("write first baseline: %v", err)
					return
				}
				if err := testcontroller.WriteTrafficSnapshot(request.Context(), connection, testcontroller.TrafficSnapshot{UploadTotal: 110, DownloadTotal: 220, Connections: []testcontroller.TrafficConnection{}}); err != nil {
					t.Errorf("write pre-gap growth: %v", err)
				}
			case 2:
				if err := testcontroller.WriteTrafficSnapshot(request.Context(), connection, testcontroller.TrafficSnapshot{UploadTotal: 150, DownloadTotal: 300, Connections: []testcontroller.TrafficConnection{}}); err != nil {
					t.Errorf("write reconnect baseline: %v", err)
					return
				}
				close(reconnected)
				<-request.Context().Done()
			}
		},
	})
	monitor := collector.New(collector.Config{
		ControllerURL: controller.URL, SampleInterval: 20 * time.Millisecond, TrafficSink: store,
		Now: func() time.Time {
			index := clockIndex.Add(1) - 1
			if index >= int64(len(times)) {
				return times[len(times)-1]
			}
			return times[index]
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go monitor.Run(ctx)
	select {
	case <-reconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not reconnect")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		gaps, queryErr := store.CollectionGaps(start, start.Add(time.Hour))
		if queryErr != nil {
			t.Fatalf("query Collection gaps: %v", queryErr)
		}
		if len(gaps) == 1 && !gaps[0].Open {
			if gaps[0].RecoveredUpload != 40 || gaps[0].RecoveredDownload != 80 {
				t.Fatalf("recovered gap = %+v", gaps[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("closed Collection gap was not persisted: %+v", gaps)
		}
		time.Sleep(10 * time.Millisecond)
	}
	summary, err := store.Summary(start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("query collected summary: %v", err)
	}
	if summary.Upload.Residual != 10 || summary.Download.Residual != 20 || summary.Upload.GapRecovered != 40 || summary.Download.GapRecovered != 80 {
		t.Fatalf("continuity summary = upload %+v download %+v", summary.Upload, summary.Download)
	}
}

func TestCollectorRecoversOnceAfterControllerRestartsAtTheSameAddress(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 8, 16, 13, 30, 0, 0, time.UTC)
	baselineSent := make(chan struct{})
	stopFirstController := make(chan struct{})
	firstController := testcontroller.Start(t, testcontroller.Options{
		OnConnections: func(request *http.Request, connection *websocket.Conn) {
			if err := testcontroller.WriteTrafficSnapshot(request.Context(), connection, testcontroller.TrafficSnapshot{UploadTotal: 100, DownloadTotal: 200, Connections: []testcontroller.TrafficConnection{}}); err != nil {
				t.Errorf("write first Controller baseline: %v", err)
				return
			}
			close(baselineSent)
			select {
			case <-stopFirstController:
				_ = connection.Close(websocket.StatusGoingAway, "Controller restarting")
			case <-request.Context().Done():
			}
		},
	})
	var clock atomic.Int32
	monitor := collector.New(collector.Config{
		ControllerURL: firstController.URL, SampleInterval: 20 * time.Millisecond, TrafficSink: store,
		Now: func() time.Time {
			if clock.Add(1) == 1 {
				return start
			}
			return start.Add(5 * time.Minute)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go monitor.Run(ctx)
	select {
	case <-baselineSent:
	case <-time.After(3 * time.Second):
		t.Fatal("first Controller did not establish a baseline")
	}
	waitForSnapshot(t, monitor, func(snapshot collector.Snapshot) bool { return snapshot.State == collector.StateConnected })
	address := strings.TrimPrefix(firstController.URL, "http://")
	close(stopFirstController)
	waitForSnapshot(t, monitor, func(snapshot collector.Snapshot) bool { return snapshot.State == collector.StateUnavailable })
	firstController.Close()

	restartedSent := make(chan struct{})
	secondController := testcontroller.StartAt(t, address, testcontroller.Options{
		OnConnections: func(request *http.Request, connection *websocket.Conn) {
			if err := testcontroller.WriteTrafficSnapshot(request.Context(), connection, testcontroller.TrafficSnapshot{UploadTotal: 150, DownloadTotal: 300, Connections: []testcontroller.TrafficConnection{}}); err != nil {
				t.Errorf("write restarted Controller baseline: %v", err)
				return
			}
			close(restartedSent)
			<-request.Context().Done()
		},
	})
	_ = secondController
	select {
	case <-restartedSent:
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not reconnect to the restarted Controller")
	}
	waitForSnapshot(t, monitor, func(snapshot collector.Snapshot) bool { return snapshot.State == collector.StateConnected })

	gaps, err := store.CollectionGaps(start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("query Controller restart gaps: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Disposition != continuity.DispositionRecovered || gaps[0].RecoveredUpload != 50 || gaps[0].RecoveredDownload != 100 {
		t.Fatalf("Controller restart gaps = %+v", gaps)
	}
	summary, err := store.Summary(start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("query Controller restart summary: %v", err)
	}
	if summary.Upload.GapRecovered != 50 || summary.Download.GapRecovered != 100 || summary.Total.Total != 150 {
		t.Fatalf("Controller restart summary = %+v", summary)
	}
}

func TestCollectorResumesPersistedGapAfterAuthenticationRecovery(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	start := time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)
	baselineSent := make(chan struct{})
	firstController := testcontroller.Start(t, testcontroller.Options{
		OnConnections: func(request *http.Request, connection *websocket.Conn) {
			if err := testcontroller.WriteTrafficSnapshot(request.Context(), connection, testcontroller.TrafficSnapshot{UploadTotal: 100, DownloadTotal: 200, Connections: []testcontroller.TrafficConnection{}}); err != nil {
				t.Errorf("write persisted baseline: %v", err)
				return
			}
			close(baselineSent)
			<-request.Context().Done()
		},
	})
	first := collector.New(collector.Config{ControllerURL: firstController.URL, SampleInterval: 20 * time.Millisecond, TrafficSink: store, Now: func() time.Time { return start }})
	firstContext, stopFirst := context.WithCancel(context.Background())
	firstDone := make(chan struct{})
	go func() { first.Run(firstContext); close(firstDone) }()
	select {
	case <-baselineSent:
	case <-time.After(3 * time.Second):
		t.Fatal("first collector did not persist a baseline")
	}
	waitForSnapshot(t, first, func(snapshot collector.Snapshot) bool { return snapshot.State == collector.StateConnected })
	stopFirst()
	<-firstDone
	if err := store.Close(); err != nil {
		t.Fatalf("close database before restart: %v", err)
	}

	store, err = storage.Open(databasePath)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var authorizationChecks atomic.Int32
	reconnected := make(chan struct{})
	secondController := testcontroller.Start(t, testcontroller.Options{
		Authorize: func(*http.Request) bool { return authorizationChecks.Add(1) > 1 },
		OnConnections: func(request *http.Request, connection *websocket.Conn) {
			if err := testcontroller.WriteTrafficSnapshot(request.Context(), connection, testcontroller.TrafficSnapshot{UploadTotal: 130, DownloadTotal: 260, Connections: []testcontroller.TrafficConnection{}}); err != nil {
				t.Errorf("write restarted sample: %v", err)
				return
			}
			close(reconnected)
			<-request.Context().Done()
		},
	})
	reconnectAt := start.Add(10 * time.Minute)
	second := collector.New(collector.Config{ControllerURL: secondController.URL, SampleInterval: 20 * time.Millisecond, TrafficSink: store, Now: func() time.Time { return reconnectAt }})
	secondContext, stopSecond := context.WithCancel(context.Background())
	t.Cleanup(stopSecond)
	go second.Run(secondContext)
	waitForSnapshot(t, second, func(snapshot collector.Snapshot) bool {
		return snapshot.State == collector.StateUnavailable && snapshot.Reason == collector.ReasonAuthenticationFailed
	})
	select {
	case <-reconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not recover after authentication succeeded")
	}
	waitForSnapshot(t, second, func(snapshot collector.Snapshot) bool { return snapshot.State == collector.StateConnected })
	gaps, err := store.CollectionGaps(start, reconnectAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("query restarted gaps: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Open || gaps[0].Reason != continuity.ReasonAuthenticationFailed || gaps[0].Disposition != continuity.DispositionRecovered || gaps[0].RecoveredUpload != 30 || gaps[0].RecoveredDownload != 60 {
		t.Fatalf("restarted authentication gap = %+v", gaps)
	}
	summary, err := store.Summary(start, reconnectAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("query authentication recovery summary: %v", err)
	}
	if summary.Upload.GapRecovered != 30 || summary.Download.GapRecovered != 60 || summary.Total.Total != 90 {
		t.Fatalf("authentication recovery summary = %+v", summary)
	}
}

func TestCollectorKeepsMalformedGapOpenUntilValidResetBaseline(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(filepath.Join(t.TempDir(), "data", "traffic.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 8, 16, 15, 0, 0, 0, time.UTC)
	validResetSent := make(chan struct{})
	var streams atomic.Int32
	controller := testcontroller.Start(t, testcontroller.Options{
		OnConnections: func(request *http.Request, connection *websocket.Conn) {
			switch streams.Add(1) {
			case 1:
				if err := testcontroller.WriteTrafficSnapshot(request.Context(), connection, testcontroller.TrafficSnapshot{UploadTotal: 100, DownloadTotal: 200, Connections: []testcontroller.TrafficConnection{}}); err != nil {
					t.Errorf("write pre-malformed baseline: %v", err)
					return
				}
				_ = connection.Write(request.Context(), websocket.MessageText, []byte(`{"uploadTotal":"malformed"}`))
			default:
				if err := testcontroller.WriteTrafficSnapshot(request.Context(), connection, testcontroller.TrafficSnapshot{UploadTotal: 3, DownloadTotal: 7, Connections: []testcontroller.TrafficConnection{}}); err != nil {
					t.Errorf("write reset baseline: %v", err)
					return
				}
				close(validResetSent)
				<-request.Context().Done()
			}
		},
	})
	var clockIndex atomic.Int64
	monitor := collector.New(collector.Config{
		ControllerURL: controller.URL, SampleInterval: 20 * time.Millisecond, TrafficSink: store,
		Now: func() time.Time {
			if clockIndex.Add(1) == 1 {
				return start
			}
			return start.Add(time.Minute)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go monitor.Run(ctx)
	waitForSnapshot(t, monitor, func(snapshot collector.Snapshot) bool {
		return snapshot.State == collector.StateUnavailable && snapshot.Reason == collector.ReasonInvalidSchema
	})
	select {
	case <-validResetSent:
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not reconnect with the reset baseline")
	}
	waitForSnapshot(t, monitor, func(snapshot collector.Snapshot) bool { return snapshot.State == collector.StateConnected })
	gaps, err := store.CollectionGaps(start, start.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("query malformed recovery gap: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Disposition != continuity.DispositionReset || gaps[0].Reason != continuity.ReasonInvalidSchema {
		t.Fatalf("malformed recovery gaps = %+v", gaps)
	}
	summary, err := store.Summary(start, start.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("query reset summary: %v", err)
	}
	if summary.Total.Total != 0 {
		t.Fatalf("malformed reset produced traffic: %+v", summary.Total)
	}
}

func TestCollectorBaselinesBeforeReportingLiveGrowth(t *testing.T) {
	t.Parallel()

	const secret = "controller-secret"
	allowGrowth := make(chan struct{})
	allowDisconnect := make(chan struct{})
	baselineSent := make(chan struct{})

	controller := testcontroller.Start(t, testcontroller.Options{
		RequiredSecret: secret,
		Version:        "v1.19.0",
		OnConnections: func(request *http.Request, connection *websocket.Conn) {
			if got, want := request.URL.Query().Get("interval"), "200"; got != want {
				t.Errorf("WebSocket interval = %q, want %q", got, want)
			}
			if err := testcontroller.WriteSnapshot(request.Context(), connection, 10_000, 20_000, 400, 900); err != nil {
				t.Errorf("write baseline: %v", err)
			}
			close(baselineSent)
			select {
			case <-allowGrowth:
			case <-request.Context().Done():
				return
			}
			time.Sleep(50 * time.Millisecond)
			if err := testcontroller.WriteSnapshot(request.Context(), connection, 10_500, 21_000, 500, 1_100); err != nil {
				t.Errorf("write growth: %v", err)
			}
			select {
			case <-allowDisconnect:
			case <-request.Context().Done():
			}
		},
	})

	monitor := collector.New(collector.Config{
		ControllerURL:    controller.URL,
		ControllerSecret: secret,
		SampleInterval:   200 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go monitor.Run(ctx)

	select {
	case <-baselineSent:
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not establish a baseline")
	}
	baseline := waitForSnapshot(t, monitor, func(snapshot collector.Snapshot) bool {
		return snapshot.State == collector.StateConnected
	})
	if baseline.UploadBytesPerSecond != 0 || baseline.DownloadBytesPerSecond != 0 {
		t.Fatalf("baseline imported pre-monitor traffic: %+v", baseline)
	}
	if baseline.ActiveConnections != 1 || baseline.LastSample == nil {
		t.Fatalf("baseline live state = %+v", baseline)
	}

	close(allowGrowth)
	growth := waitForSnapshot(t, monitor, func(snapshot collector.Snapshot) bool {
		return snapshot.UploadBytesPerSecond > 0 && snapshot.DownloadBytesPerSecond > 0
	})
	if growth.ActiveConnections != 1 {
		t.Fatalf("growth active connections = %d, want 1", growth.ActiveConnections)
	}
	if controller.VersionRequests.Load() != 1 || controller.ConnectionStreams.Load() != 1 {
		t.Fatalf("requests: version=%d streams=%d", controller.VersionRequests.Load(), controller.ConnectionStreams.Load())
	}

	close(allowDisconnect)
	disconnected := waitForSnapshot(t, monitor, func(snapshot collector.Snapshot) bool {
		return snapshot.State == collector.StateUnavailable && snapshot.Reason == collector.ReasonDisconnected
	})
	if disconnected.LastSample == nil || !disconnected.LastSample.Equal(*growth.LastSample) || disconnected.ControllerVersion != growth.ControllerVersion {
		t.Fatalf("disconnection lost last usable diagnostics: before=%+v after=%+v", growth, disconnected)
	}
}

func TestCollectorReportsActionableRedactedControllerFailures(t *testing.T) {
	const secret = "do-not-leak-this-secret"
	tests := []struct {
		name       string
		controller func(*testing.T) string
		reason     collector.Reason
	}{
		{
			name: "WebSocket authentication failure",
			controller: func(t *testing.T) string {
				return testcontroller.Start(t, testcontroller.Options{
					RequiredSecret:    secret,
					Version:           "v1.19.0",
					ConnectionsStatus: http.StatusUnauthorized,
				}).URL
			},
			reason: collector.ReasonAuthenticationFailed,
		},
		{
			name: "unsupported version",
			controller: func(t *testing.T) string {
				return testcontroller.Start(t, testcontroller.Options{RequiredSecret: secret, Version: "v2.0.0"}).URL
			},
			reason: collector.ReasonIncompatibleVersion,
		},
		{
			name: "malformed connections schema",
			controller: func(t *testing.T) string {
				return testcontroller.Start(t, testcontroller.Options{
					RequiredSecret: secret,
					Version:        "v1.19.0",
					OnConnections: func(request *http.Request, connection *websocket.Conn) {
						_ = connection.Write(request.Context(), websocket.MessageText, []byte(`{"uploadTotal":"bad","downloadTotal":2,"connections":[]}`))
						<-request.Context().Done()
					},
				}).URL
			},
			reason: collector.ReasonInvalidSchema,
		},
		{
			name: "initial disconnection",
			controller: func(_ *testing.T) string {
				return testcontroller.DisconnectedURL()
			},
			reason: collector.ReasonUnreachable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			monitor := collector.New(collector.Config{
				ControllerURL:    test.controller(t),
				ControllerSecret: secret,
				SampleInterval:   20 * time.Millisecond,
			})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go monitor.Run(ctx)

			failure := waitForSnapshot(t, monitor, func(snapshot collector.Snapshot) bool {
				return snapshot.State == collector.StateUnavailable && snapshot.Reason == test.reason
			})
			if failure.Message == "" {
				t.Fatal("failure did not include an actionable message")
			}
			if strings.Contains(failure.Message, secret) || strings.Contains(failure.ControllerVersion, secret) {
				t.Fatal("collector snapshot exposed the Controller secret")
			}
		})
	}
}

func waitForSnapshot(t *testing.T, monitor *collector.Collector, matches func(collector.Snapshot) bool) collector.Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := monitor.Snapshot()
		if matches(snapshot) {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("collector did not reach expected state; last snapshot: %s", strconv.Quote(monitor.Snapshot().Message))
	return collector.Snapshot{}
}
