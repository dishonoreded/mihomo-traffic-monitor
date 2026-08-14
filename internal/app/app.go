package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/api"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/config"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/storage"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/version"
	"github.com/dishonoreded/mihomo-traffic-monitor/internal/webui"
)

type EnvironmentLookup func(string) (string, bool)

func Run(ctx context.Context, args []string, lookup EnvironmentLookup, home string, output io.Writer) error {
	if len(args) == 0 {
		return usage(output)
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:], lookup, home, output)
	case "version":
		_, err := fmt.Fprintln(output, version.String())
		return err
	case "help", "-h", "--help":
		return usage(output)
	default:
		return fmt.Errorf("unknown command %q; use mihomo-monitor help", args[0])
	}
}

func serve(ctx context.Context, args []string, lookup EnvironmentLookup, home string, output io.Writer) error {
	configuration, err := config.LoadServe(args, config.LookupEnvironment(lookup), home)
	if err != nil {
		return err
	}
	store, err := storage.Open(configuration.DatabasePath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	listener, err := net.Listen("tcp", configuration.DashboardAddress)
	if err != nil {
		return fmt.Errorf("listen on local dashboard: %w", err)
	}
	server := &http.Server{
		Handler:           api.NewHandler(configuration, store, webui.Assets()),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	_, _ = fmt.Fprintf(output, "mihomo-monitor listening on http://%s\n", listener.Addr())

	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("stop local dashboard: %w", err)
		}
		return nil
	case err := <-serveErrors:
		if err == http.ErrServerClosed {
			return nil
		}
		return fmt.Errorf("serve local dashboard: %w", err)
	}
}

func usage(output io.Writer) error {
	_, err := fmt.Fprintln(output, `mihomo-monitor — private local Mihomo traffic observability

Usage:
  mihomo-monitor serve [flags]
  mihomo-monitor version

Serve flags:
  --controller-url URL             default http://127.0.0.1:9090
  --dashboard-address HOST:PORT    default 127.0.0.1:9091 (loopback only)
  --sample-interval DURATION       default 1s
  --database-path PATH             default ~/Library/Application Support/mihomo-traffic-monitor/traffic.db

Environment:
  MIHOMO_MONITOR_CONTROLLER_URL
  MIHOMO_MONITOR_DASHBOARD_ADDRESS
  MIHOMO_MONITOR_SAMPLE_INTERVAL
  MIHOMO_MONITOR_DATABASE_PATH
  MIHOMO_MONITOR_CONTROLLER_SECRET (environment only; never displayed)

Flags override environment variables; environment variables override defaults.`)
	return err
}

func CurrentEnvironment(key string) (string, bool) {
	return os.LookupEnv(key)
}
