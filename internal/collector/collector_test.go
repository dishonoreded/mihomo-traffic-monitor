package collector_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/collector"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/testcontroller"
)

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
