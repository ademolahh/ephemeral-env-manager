package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ademolahh/ephemeral-env-manager/internal/admin"
	"github.com/ademolahh/ephemeral-env-manager/internal/api"
	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/logging"
	"github.com/ademolahh/ephemeral-env-manager/internal/metrics"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider/docker"
	"github.com/ademolahh/ephemeral-env-manager/internal/proxy"
	"github.com/ademolahh/ephemeral-env-manager/internal/session"
)

func main() {
	admin.HandleHealthcheckFlag()
	if err := logging.Setup("eem"); err != nil {
		logging.Fatal("logging", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		logging.Fatal("DATABASE_URL is required", nil)
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		logging.Fatal("REDIS_URL is required", nil)
	}

	github, err := githubFromEnv()
	if err != nil {
		logging.Fatal("github", err)
	}

	pool, err := db.New(ctx, databaseURL)
	if err != nil {
		logging.Fatal("database", err)
	}
	defer pool.Close()

	rdb, err := session.Connect(ctx, redisURL)
	if err != nil {
		logging.Fatal("redis", err)
	}
	defer rdb.Close()

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

	prometheus.MustRegister(metrics.NewStateCollector(pool))
	admin.Start(ctx, map[string]admin.Check{
		"postgres": pool.Ping,
		"redis":    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		"docker":   d.Ping,
	})

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           api.Routes(api.NewHandler(pool, envs, session.New(rdb, "eem:"), github)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.Fatal("listen", err)
		}
	}()
	slog.Info("eem listening", "addr", srv.Addr)

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}

	slog.Info("waiting for in-flight provisioning")
	envs.Wait()
}

func githubFromEnv() (*auth.GitHub, error) {
	clientID := os.Getenv("GITHUB_CLIENT_ID")
	if clientID == "" {
		return nil, nil
	}

	publicURL := strings.TrimSuffix(os.Getenv("PUBLIC_URL"), "/")
	if publicURL == "" {
		return nil, errors.New("PUBLIC_URL is required when GITHUB_CLIENT_ID is set")
	}

	return auth.NewGitHub(auth.GitHubConfig{
		ClientID:     clientID,
		ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
		RedirectURL:  publicURL + "/auth/github/callback",
		AllowedUsers: strings.Split(os.Getenv("GITHUB_ALLOWED_USERS"), ","),
	})
}
