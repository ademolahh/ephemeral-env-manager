package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/admin"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/logging"
	"github.com/ademolahh/ephemeral-env-manager/internal/setup"
	"github.com/ademolahh/ephemeral-env-manager/internal/worker"
)

func main() {
	admin.HandleHealthcheckFlag()
	if err := logging.Setup("worker"); err != nil {
		logging.Fatal("logging", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		logging.Fatal("DATABASE_URL is required", nil)
	}

	interval := 30 * time.Second
	if v := os.Getenv("WORKER_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			logging.Fatal("WORKER_INTERVAL must be a positive duration like 30s", fmt.Errorf("got %q", v))
		}
		interval = d
	}

	pool, err := db.New(ctx, databaseURL)
	if err != nil {
		logging.Fatal("database", err)
	}
	defer pool.Close()

	envs, d, err := setup.Manager(ctx, pool)
	if err != nil {
		logging.Fatal("environments", err)
	}

	admin.Start(ctx, map[string]admin.Check{"postgres": pool.Ping, "docker": d.Ping})

	slog.Info("worker running", "interval", interval)
	worker.New(pool, envs, interval).Run(ctx)

	slog.Info("waiting for in-flight deletions")
	envs.Stop()
}
