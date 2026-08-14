package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/api"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/collector"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/config"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/storage"
)

func TestLiveEventsImmediatelySendTheCurrentReadOnlyStatus(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lastSample := time.Date(2026, 8, 14, 6, 15, 0, 123, time.UTC)
	monitor := &staticLiveSource{snapshot: collector.Snapshot{
		State:                  collector.StateConnected,
		Reason:                 collector.ReasonConnected,
		Message:                "Live traffic collection is active.",
		ControllerVersion:      "v1.19.0",
		LastSample:             &lastSample,
		UploadBytesPerSecond:   2048,
		DownloadBytesPerSecond: 4096,
		ActiveConnections:      7,
	}}
	before := monitor.Snapshot()
	handler := api.NewHandlerWithCollector(config.Config{DatabasePath: databasePath, SampleInterval: time.Second}, store, testAssets(t), monitor)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	first := readLiveEvent(t, server.URL)
	if first.Collector.State != "connected" || first.Live.UploadBytesPerSecond != 2048 || first.Live.DownloadBytesPerSecond != 4096 || first.Live.ActiveConnections != 7 {
		t.Fatalf("unexpected initial SSE status: %+v", first)
	}
	if _, err := time.Parse(time.RFC3339Nano, first.Timestamp); err != nil || first.Collector.LastSample != lastSample.Format(time.RFC3339Nano) {
		t.Fatalf("SSE timestamps are not RFC3339: %+v", first.Collector)
	}

	second := readLiveEvent(t, server.URL)
	if second.Collector.State != "connected" || second.Live.ActiveConnections != 7 {
		t.Fatalf("reconnected SSE status: %+v", second)
	}
	if after := monitor.Snapshot(); !reflect.DeepEqual(after, before) {
		t.Fatalf("SSE reads mutated collection state: before=%+v after=%+v", before, after)
	}
}

type staticLiveSource struct {
	snapshot collector.Snapshot
}

func (source *staticLiveSource) Snapshot() collector.Snapshot {
	return source.snapshot
}

func (source *staticLiveSource) Subscribe() (<-chan collector.Snapshot, func()) {
	updates := make(chan collector.Snapshot, 1)
	updates <- source.snapshot
	return updates, func() {}
}

type streamedStatus struct {
	Timestamp string `json:"timestamp"`
	Collector struct {
		State      string `json:"state"`
		LastSample string `json:"lastSample"`
	} `json:"collector"`
	Live struct {
		UploadBytesPerSecond   int64 `json:"uploadBytesPerSecond"`
		DownloadBytesPerSecond int64 `json:"downloadBytesPerSecond"`
		ActiveConnections      int   `json:"activeConnections"`
	} `json:"live"`
}

func readLiveEvent(t *testing.T, serverURL string) streamedStatus {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, serverURL+"/api/v1/live/events", nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("SSE content type = %q", got)
	}

	reader := bufio.NewReader(response.Body)
	eventLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read SSE event: %v", err)
	}
	dataLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read SSE data: %v", err)
	}
	if strings.TrimSpace(eventLine) != "event: status" || !strings.HasPrefix(dataLine, "data: ") {
		t.Fatalf("unexpected initial SSE event: %q %q", eventLine, dataLine)
	}
	var status streamedStatus
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(dataLine, "data: "))), &status); err != nil {
		t.Fatalf("decode SSE status: %v", err)
	}
	return status
}
