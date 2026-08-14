package api

import (
	"bufio"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/collector"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/config"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/storage"
)

func TestLiveEventsSendKeepalives(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	assets, err := fs.Sub(fstest.MapFS{"dist/index.html": {Data: []byte("<!doctype html>")}}, "dist")
	if err != nil {
		t.Fatalf("create assets: %v", err)
	}
	monitor := collector.New(collector.Config{})
	server := httptest.NewServer(newHandler(config.Config{DatabasePath: databasePath}, store, assets, monitor, 10*time.Millisecond))
	t.Cleanup(server.Close)

	response, err := http.Get(server.URL + "/api/v1/live/events")
	if err != nil {
		t.Fatalf("open live events: %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for lines := 0; lines < 3; lines++ {
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatalf("read initial event: %v", err)
		}
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read keepalive: %v", err)
	}
	if strings.TrimSpace(line) != ": keepalive" {
		t.Fatalf("keepalive = %q", line)
	}
}
