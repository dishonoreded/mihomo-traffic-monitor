package config

import (
	"flag"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultControllerURL    = "http://127.0.0.1:9090"
	defaultDashboardAddress = "127.0.0.1:9091"
	defaultSampleInterval   = time.Second
)

type Config struct {
	ControllerURL    string
	ControllerSecret string
	DashboardAddress string
	SampleInterval   time.Duration
	DatabasePath     string
}

type LookupEnvironment func(string) (string, bool)

func LoadServe(args []string, lookup LookupEnvironment, home string) (Config, error) {
	cfg := Config{
		ControllerURL:    environmentOr(lookup, "MIHOMO_MONITOR_CONTROLLER_URL", defaultControllerURL),
		ControllerSecret: environmentOr(lookup, "MIHOMO_MONITOR_CONTROLLER_SECRET", ""),
		DashboardAddress: environmentOr(lookup, "MIHOMO_MONITOR_DASHBOARD_ADDRESS", defaultDashboardAddress),
		DatabasePath:     environmentOr(lookup, "MIHOMO_MONITOR_DATABASE_PATH", filepath.Join(home, "Library", "Application Support", "mihomo-traffic-monitor", "traffic.db")),
	}

	intervalText := environmentOr(lookup, "MIHOMO_MONITOR_SAMPLE_INTERVAL", defaultSampleInterval.String())
	interval, err := time.ParseDuration(intervalText)
	if err != nil || interval <= 0 {
		return Config{}, fmt.Errorf("invalid sample interval %q", intervalText)
	}
	cfg.SampleInterval = interval

	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.StringVar(&cfg.ControllerURL, "controller-url", cfg.ControllerURL, "Mihomo External Controller URL")
	flags.StringVar(&cfg.DashboardAddress, "dashboard-address", cfg.DashboardAddress, "loopback dashboard address")
	flags.DurationVar(&cfg.SampleInterval, "sample-interval", cfg.SampleInterval, "Controller sampling interval")
	flags.StringVar(&cfg.DatabasePath, "database-path", cfg.DatabasePath, "SQLite database path")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected serve arguments: %v", flags.Args())
	}
	if cfg.SampleInterval <= 0 {
		return Config{}, fmt.Errorf("sample interval must be positive")
	}
	if err := validateControllerURL(cfg.ControllerURL); err != nil {
		return Config{}, err
	}
	if err := validateLoopback(cfg.DashboardAddress); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateControllerURL(rawURL string) error {
	controllerURL, err := url.ParseRequestURI(rawURL)
	if err != nil || controllerURL.Host == "" || (controllerURL.Scheme != "http" && controllerURL.Scheme != "https") {
		return fmt.Errorf("Controller URL must be an absolute http or https URL")
	}
	if controllerURL.User != nil || controllerURL.RawQuery != "" || controllerURL.Fragment != "" || strings.Trim(controllerURL.Path, "/") != "" {
		return fmt.Errorf("Controller URL must not contain credentials, query parameters, fragments, or a path; use MIHOMO_MONITOR_CONTROLLER_SECRET for authentication")
	}
	return nil
}

func environmentOr(lookup LookupEnvironment, key, fallback string) string {
	if lookup == nil {
		return fallback
	}
	if value, ok := lookup(key); ok && value != "" {
		return value
	}
	return fallback
}

func validateLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid dashboard address %q: %w", address, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("dashboard address must use a loopback host")
	}
	return nil
}
