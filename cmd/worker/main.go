package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/logging"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider/docker"
	"github.com/ademolahh/ephemeral-env-manager/internal/proxy"
	"github.com/ademolahh/ephemeral-env-manager/internal/worker"
)

func main() {
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

	px, err := proxy.FromEnv()
	if err != nil {
		logging.Fatal("proxy", err)
	}

	var dockerOpts []docker.Option
	var envOpts []env.Option
	if px != nil {
		dockerOpts = append(dockerOpts, docker.WithProxy(px.Container))
		envOpts = append(envOpts, env.WithProxy(px))
	}

	d, err := docker.New(dockerOpts...)
	if err != nil {
		logging.Fatal("docker", err)
	}

	if err := d.Ping(ctx); err != nil {
		logging.Fatal("docker", err)
	}

	envs := env.NewManager(pool, map[provider.Type]provider.Provider{provider.Docker: d}, envOpts...)

	slog.Info("worker running", "interval", interval)
	worker.New(pool, envs, interval).Run(ctx)

	slog.Info("waiting for in-flight deletions")
	envs.Wait()
}
