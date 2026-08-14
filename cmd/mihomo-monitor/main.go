package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dishonoreded/mihomo-traffic-monitor/internal/app"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mihomo-monitor: resolve home directory")
		os.Exit(1)
	}
	if err := app.Run(ctx, os.Args[1:], app.CurrentEnvironment, home, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "mihomo-monitor: %v\n", err)
		os.Exit(1)
	}
}
