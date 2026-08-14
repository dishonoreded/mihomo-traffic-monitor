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
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/app"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/testcontroller"
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

func TestServeStreamsLiveTrafficFromMihomoWithoutImportingTheBaseline(t *testing.T) {
	const secret = "controller-secret"
	allowGrowth := make(chan struct{})
	controller := testcontroller.Start(t, testcontroller.Options{
		RequiredSecret: secret,
		Version:        "v1.19.0",
		OnConnections: func(request *http.Request, connection *websocket.Conn) {
			if err := testcontroller.WriteSnapshot(request.Context(), connection, 8_000, 16_000, 100, 200); err != nil {
				t.Errorf("write baseline: %v", err)
			}
			select {
			case <-allowGrowth:
			case <-request.Context().Done():
				return
			}
			time.Sleep(50 * time.Millisecond)
			if err := testcontroller.WriteSnapshot(request.Context(), connection, 8_500, 17_000, 100, 200); err != nil {
				t.Errorf("write growth: %v", err)
			}
			<-request.Context().Done()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outputReader, outputWriter := io.Pipe()
	environment := map[string]string{
		"MIHOMO_MONITOR_CONTROLLER_URL":    controller.URL,
		"MIHOMO_MONITOR_CONTROLLER_SECRET": secret,
		"MIHOMO_MONITOR_DASHBOARD_ADDRESS": "127.0.0.1:0",
		"MIHOMO_MONITOR_DATABASE_PATH":     filepath.Join(t.TempDir(), "data", "traffic.db"),
		"MIHOMO_MONITOR_SAMPLE_INTERVAL":   "50ms",
	}
	lookup := func(key string) (string, bool) { value, ok := environment[key]; return value, ok }
	finished := make(chan error, 1)
	go func() { finished <- app.Run(ctx, []string{"serve"}, lookup, t.TempDir(), outputWriter) }()

	line, err := bufio.NewReader(outputReader).ReadString('\n')
	if err != nil {
		t.Fatalf("read listen address: %v", err)
	}
	address := strings.TrimSpace(strings.TrimPrefix(line, "mihomo-monitor listening on "))
	baseline := waitForLiveStatus(t, address, func(status liveStatus) bool { return status.Collector.State == "connected" })
	if baseline.Live.UploadBytesPerSecond != 0 || baseline.Live.DownloadBytesPerSecond != 0 {
		t.Fatalf("baseline imported pre-monitor traffic: %+v", baseline.Live)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/api/v1/live/events", nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	_ = readStatusEvent(t, reader)
	close(allowGrowth)
	live := readStatusEvent(t, reader)
	if live.Live.UploadBytesPerSecond <= 0 || live.Live.DownloadBytesPerSecond <= 0 || live.Live.ActiveConnections != 1 {
		t.Fatalf("live SSE status = %+v", live.Live)
	}

	current := waitForLiveStatus(t, address, func(status liveStatus) bool {
		return status.Live.UploadBytesPerSecond > 0 && status.Live.DownloadBytesPerSecond > 0
	})
	if current.Collector.LastSample == nil || current.Collector.ControllerVersion == nil || *current.Collector.ControllerVersion != "v1.19.0" {
		t.Fatalf("status did not expose live Controller diagnostics: %+v", current.Collector)
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

func TestServeKeepsTheShellAvailableForControllerFailures(t *testing.T) {
	const secret = "never-return-this-controller-secret"
	tests := []struct {
		name       string
		controller func(*testing.T) string
		reason     string
	}{
		{
			name: "authentication failure",
			controller: func(t *testing.T) string {
				return testcontroller.Start(t, testcontroller.Options{
					RequiredSecret: secret,
					VersionStatus:  http.StatusUnauthorized,
				}).URL
			},
			reason: "authentication_failed",
		},
		{
			name: "unsupported version",
			controller: func(t *testing.T) string {
				return testcontroller.Start(t, testcontroller.Options{RequiredSecret: secret, Version: "v2.0.0"}).URL
			},
			reason: "incompatible_version",
		},
		{
			name: "malformed schema",
			controller: func(t *testing.T) string {
				return testcontroller.Start(t, testcontroller.Options{
					RequiredSecret: secret,
					Version:        "v1.19.0",
					OnConnections: func(request *http.Request, connection *websocket.Conn) {
						_ = connection.Write(request.Context(), websocket.MessageText, []byte(`{"uploadTotal":"bad"}`))
					},
				}).URL
			},
			reason: "invalid_schema",
		},
		{
			name: "initial disconnection",
			controller: func(_ *testing.T) string {
				return testcontroller.DisconnectedURL()
			},
			reason: "unreachable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := map[string]string{
				"MIHOMO_MONITOR_CONTROLLER_URL":    test.controller(t),
				"MIHOMO_MONITOR_CONTROLLER_SECRET": secret,
				"MIHOMO_MONITOR_DASHBOARD_ADDRESS": "127.0.0.1:0",
				"MIHOMO_MONITOR_DATABASE_PATH":     filepath.Join(t.TempDir(), "data", "traffic.db"),
				"MIHOMO_MONITOR_SAMPLE_INTERVAL":   "20ms",
			}
			address := startTestMonitor(t, environment)
			status := waitForLiveStatus(t, address, func(status liveStatus) bool {
				return status.Collector.State == "unavailable" && status.Collector.Reason == test.reason
			})
			if status.Collector.Message == "" {
				t.Fatal("Controller failure did not include an actionable message")
			}

			response, err := http.Get(address + "/")
			if err != nil {
				t.Fatalf("get historical shell: %v", err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil {
				t.Fatalf("read historical shell: %v", err)
			}
			if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `id="root"`) {
				t.Fatalf("historical shell unavailable: status=%d body=%q", response.StatusCode, body)
			}

			statusResponse, err := http.Get(address + "/api/v1/status")
			if err != nil {
				t.Fatalf("get redacted status: %v", err)
			}
			statusBody, err := io.ReadAll(statusResponse.Body)
			_ = statusResponse.Body.Close()
			if err != nil {
				t.Fatalf("read redacted status: %v", err)
			}
			if strings.Contains(string(statusBody), secret) {
				t.Fatal("status exposed the Controller secret")
			}
		})
	}
}

type liveStatus struct {
	Collector struct {
		State             string  `json:"state"`
		Reason            string  `json:"reason"`
		Message           string  `json:"message"`
		ControllerVersion *string `json:"controllerVersion"`
		LastSample        *string `json:"lastSample"`
	} `json:"collector"`
	Live struct {
		UploadBytesPerSecond   int64 `json:"uploadBytesPerSecond"`
		DownloadBytesPerSecond int64 `json:"downloadBytesPerSecond"`
		ActiveConnections      int   `json:"activeConnections"`
	} `json:"live"`
}

func startTestMonitor(t *testing.T, environment map[string]string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	outputReader, outputWriter := io.Pipe()
	lookup := func(key string) (string, bool) { value, ok := environment[key]; return value, ok }
	home := t.TempDir()
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
	var address string
	select {
	case line := <-listenLine:
		address = strings.TrimSpace(strings.TrimPrefix(line, "mihomo-monitor listening on "))
	case err := <-readError:
		t.Fatalf("read listen address: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not report its listen address")
	}

	var stop sync.Once
	t.Cleanup(func() {
		stop.Do(func() {
			cancel()
			select {
			case err := <-finished:
				if err != nil {
					t.Errorf("stop server: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("server did not stop after cancellation")
			}
			_ = outputReader.Close()
			_ = outputWriter.Close()
		})
	})
	return address
}

func waitForLiveStatus(t *testing.T, address string, matches func(liveStatus) bool) liveStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last liveStatus
	for time.Now().Before(deadline) {
		response, err := http.Get(address + "/api/v1/status")
		if err == nil {
			err = json.NewDecoder(response.Body).Decode(&last)
			_ = response.Body.Close()
			if err == nil && matches(last) {
				return last
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("status did not reach expected live state: %+v", last)
	return liveStatus{}
}

func readStatusEvent(t *testing.T, reader *bufio.Reader) liveStatus {
	t.Helper()
	event, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read SSE event line: %v", err)
	}
	data, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read SSE data line: %v", err)
	}
	if strings.TrimSpace(event) != "event: status" || !strings.HasPrefix(data, "data: ") {
		t.Fatalf("unexpected SSE event: %q %q", event, data)
	}
	var status liveStatus
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(data, "data: "))), &status); err != nil {
		t.Fatalf("decode SSE status: %v", err)
	}
	_, _ = reader.ReadString('\n')
	return status
}
