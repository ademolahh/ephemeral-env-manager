package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider/docker"
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

	d, err := docker.New()
	if err != nil {
		log.Fatalf("docker: %v", err)
	}

	if err := d.Ping(ctx); err != nil {
		log.Fatalf("docker: %v", err)
	}

	envs := env.NewManager(pool, map[provider.Type]provider.Provider{provider.Docker: d})

	log.Printf("worker deleting expired environments every %s", interval)
	w := worker.New(pool, envs, interval)

	var wg sync.WaitGroup

	wg.Go(func() {
		w.Run(ctx)
	})

	wg.Go(func() {
		w.RemovedEnv(ctx)
	})

	log.Print("waiting for in-flight deletions")
	wg.Wait()
	envs.Wait()
}
