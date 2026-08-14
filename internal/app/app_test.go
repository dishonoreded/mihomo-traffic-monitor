package app_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/app"
)

func TestVersionReportsBuildIdentity(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := app.Run(context.Background(), []string{"version"}, emptyEnvironment, t.TempDir(), &output); err != nil {
		t.Fatalf("run version: %v", err)
	}
	if !strings.HasPrefix(output.String(), "mihomo-monitor dev (commit none, built unknown)") {
		t.Fatalf("unexpected version output: %q", output.String())
	}
}

func emptyEnvironment(string) (string, bool) { return "", false }

func TestServeExposesTheEmbeddedPrivateStatusAPI(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outputReader, outputWriter := io.Pipe()
	databasePath := filepath.Join(t.TempDir(), "data", "traffic.db")
	home := t.TempDir()
	environment := map[string]string{
		"MIHOMO_MONITOR_DASHBOARD_ADDRESS": "127.0.0.1:0",
		"MIHOMO_MONITOR_DATABASE_PATH":     databasePath,
	}
	lookup := func(key string) (string, bool) { value, ok := environment[key]; return value, ok }
	finished := make(chan error, 1)
	go func() {
		finished <- app.Run(ctx, []string{"serve"}, lookup, home, outputWriter)
	}()

	listenLine := make(chan string, 1)
	readError := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(outputReader).ReadString('\n')
		if err != nil {
			readError <- err
			return
		}
		listenLine <- line
	}()
	var line string
	select {
	case line = <-listenLine:
	case err := <-readError:
		t.Fatalf("read listen address: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not report its listen address")
	}
	address := strings.TrimSpace(strings.TrimPrefix(line, "mihomo-monitor listening on "))
	response, err := http.Get(address + "/api/v1/status")
	if err != nil {
		t.Fatalf("get running status API: %v", err)
	}
	defer response.Body.Close()
	var payload struct {
		APIVersion string `json:"apiVersion"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode running status API: %v", err)
	}
	if response.StatusCode != http.StatusOK || payload.APIVersion != "v1" {
		t.Fatalf("unexpected running status: code=%d payload=%+v", response.StatusCode, payload)
	}

	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("stop server: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
}
