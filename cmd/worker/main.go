package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider/docker"
	"github.com/ademolahh/ephemeral-env-manager/internal/proxy"
	"github.com/ademolahh/ephemeral-env-manager/internal/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	interval := 30 * time.Second
	if v := os.Getenv("WORKER_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			log.Fatalf("WORKER_INTERVAL %q must be a positive duration like 30s", v)
		}
		interval = d
	}

	pool, err := db.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()

	px, err := proxy.FromEnv()
	if err != nil {
		log.Fatalf("proxy: %v", err)
	}

	var dockerOpts []docker.Option
	var envOpts []env.Option
	if px != nil {
		dockerOpts = append(dockerOpts, docker.WithProxy(px.Container))
		envOpts = append(envOpts, env.WithProxy(px))
	}

	d, err := docker.New(dockerOpts...)
	if err != nil {
		log.Fatalf("docker: %v", err)
	}

	if err := d.Ping(ctx); err != nil {
		log.Fatalf("docker: %v", err)
	}

	envs := env.NewManager(pool, map[provider.Type]provider.Provider{provider.Docker: d}, envOpts...)

	log.Printf("worker sweeping environments every %s", interval)
	worker.New(pool, envs, interval).Run(ctx)

	log.Print("waiting for in-flight deletions")
	envs.Wait()
}
