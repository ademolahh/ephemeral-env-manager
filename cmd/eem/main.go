package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/api"
	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider/docker"
	"github.com/ademolahh/ephemeral-env-manager/internal/proxy"
	"github.com/ademolahh/ephemeral-env-manager/internal/session"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		log.Fatal("REDIS_URL is required")
	}

	github, err := githubFromEnv()
	if err != nil {
		log.Fatalf("github: %v", err)
	}

	pool, err := db.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()

	rdb, err := session.Connect(ctx, redisURL)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer rdb.Close()

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

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           api.Routes(api.NewHandler(pool, envs, session.New(rdb, "eem:"), github)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()
	log.Printf("eem listening on %s", srv.Addr)

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}

	log.Print("waiting for in-flight provisioning")
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
