package config_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/config"
)

func TestLoadServeUsesPrivateDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.LoadServe(nil, emptyEnvironment, "/Users/tester")
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}

	if got, want := cfg.ControllerURL, "http://127.0.0.1:9090"; got != want {
		t.Fatalf("controller URL = %q, want %q", got, want)
	}
	if got, want := cfg.DashboardAddress, "127.0.0.1:9091"; got != want {
		t.Fatalf("dashboard address = %q, want %q", got, want)
	}
	if got, want := cfg.SampleInterval, time.Second; got != want {
		t.Fatalf("sample interval = %s, want %s", got, want)
	}
	if got, want := cfg.DatabasePath, filepath.Join("/Users/tester", "Library", "Application Support", "mihomo-traffic-monitor", "traffic.db"); got != want {
		t.Fatalf("database path = %q, want %q", got, want)
	}
	if cfg.ControllerSecret != "" {
		t.Fatal("default Controller secret must be empty")
	}
}

func emptyEnvironment(string) (string, bool) {
	return "", false
}

func TestLoadServeAppliesFlagsOverEnvironmentWithoutASecretFlag(t *testing.T) {
	t.Parallel()

	environment := map[string]string{
		"MIHOMO_MONITOR_CONTROLLER_URL":    "http://127.0.0.1:19090",
		"MIHOMO_MONITOR_CONTROLLER_SECRET": "private-value",
		"MIHOMO_MONITOR_DASHBOARD_ADDRESS": "127.0.0.1:19091",
		"MIHOMO_MONITOR_SAMPLE_INTERVAL":   "3s",
		"MIHOMO_MONITOR_DATABASE_PATH":     "/tmp/environment.db",
	}
	lookup := func(key string) (string, bool) { value, ok := environment[key]; return value, ok }
	cfg, err := config.LoadServe([]string{
		"--controller-url", "http://127.0.0.1:29090",
		"--dashboard-address", "[::1]:29091",
		"--sample-interval", "750ms",
		"--database-path", "/tmp/flag.db",
	}, lookup, "/Users/tester")
	if err != nil {
		t.Fatalf("load overrides: %v", err)
	}
	if cfg.ControllerURL != "http://127.0.0.1:29090" || cfg.DashboardAddress != "[::1]:29091" || cfg.SampleInterval != 750*time.Millisecond || cfg.DatabasePath != "/tmp/flag.db" {
		t.Fatalf("flags did not override environment: %+v", cfg)
	}
	if cfg.ControllerSecret != "private-value" {
		t.Fatal("Controller secret was not read from its dedicated environment variable")
	}

	_, err = config.LoadServe([]string{"--controller-secret", "must-not-work"}, lookup, "/Users/tester")
	if err == nil || strings.Contains(err.Error(), "must-not-work") {
		t.Fatalf("secret flag should be rejected without echoing its value: %v", err)
	}
}

func TestLoadServeRejectsNonLoopbackDashboard(t *testing.T) {
	t.Parallel()

	_, err := config.LoadServe([]string{"--dashboard-address", "0.0.0.0:9091"}, emptyEnvironment, "/Users/tester")
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback error, got %v", err)
	}
}

func TestLoadServeRejectsCredentialsEmbeddedInControllerURL(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"http://user:do-not-echo@127.0.0.1:9090",
		"http://127.0.0.1:9090/?secret=do-not-echo",
	} {
		_, err := config.LoadServe([]string{"--controller-url", rawURL}, emptyEnvironment, "/Users/tester")
		if err == nil || !strings.Contains(err.Error(), "MIHOMO_MONITOR_CONTROLLER_SECRET") {
			t.Fatalf("expected credential guidance for %q, got %v", rawURL, err)
		}
		if strings.Contains(err.Error(), "do-not-echo") {
			t.Fatalf("Controller URL error exposed credential material: %v", err)
		}
	}
}
