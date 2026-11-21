package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ademolahh/ephemeral-env-manager/internal/admin"
	"github.com/ademolahh/ephemeral-env-manager/internal/api"
	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/logging"
	"github.com/ademolahh/ephemeral-env-manager/internal/metrics"
	"github.com/ademolahh/ephemeral-env-manager/internal/session"
	"github.com/ademolahh/ephemeral-env-manager/internal/setup"
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

	envs, d, err := setup.Manager(ctx, pool)
	if err != nil {
		logging.Fatal("environments", err)
	}

	if n, err := envs.Resume(ctx); err != nil {
		logging.Fatal("resume", err)
	} else if n > 0 {
		slog.Info("resumed readiness checks", "environments", n)
	}

	prometheus.MustRegister(metrics.NewStateCollector(pool))
	admin.Start(ctx, map[string]admin.Check{
		"postgres": pool.Ping,
		"redis":    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		"docker":   d.Ping,
	})

	h := api.NewHandler(pool, envs, session.New(rdb, "eem:"), github)
	if v := os.Getenv("INLINE_TEMPLATES"); v != "" {
		allow, err := strconv.ParseBool(v)
		if err != nil {
			logging.Fatal("INLINE_TEMPLATES must be true or false", err)
		}
		h.DisableInlineTemplates = !allow
	}

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           api.Routes(h),
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
	envs.Stop()
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
