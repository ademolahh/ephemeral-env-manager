package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/admin"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/logging"
	"github.com/ademolahh/ephemeral-env-manager/internal/proxy"
)

func main() {
	admin.HandleHealthcheckFlag()
	if err := logging.Setup("proxy"); err != nil {
		logging.Fatal("logging", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		logging.Fatal("DATABASE_URL is required", nil)
	}

	domain := os.Getenv("PROXY_DOMAIN")
	if domain == "" {
		logging.Fatal("PROXY_DOMAIN is required", nil)
	}

	addr := os.Getenv("PROXY_LISTEN_ADDR")
	if addr == "" {
		addr = ":80"
	}

	pool, err := db.New(ctx, databaseURL)
	if err != nil {
		logging.Fatal("database", err)
	}
	defer pool.Close()

	admin.Start(ctx, map[string]admin.Check{"postgres": pool.Ping})

	srv := &http.Server{
		Addr:              addr,
		Handler:           (&proxy.Proxy{Domain: domain}).Handler(pool),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.Fatal("listen", err)
		}
	}()
	slog.Info("proxy listening", "addr", addr, "domain", domain)

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
}
