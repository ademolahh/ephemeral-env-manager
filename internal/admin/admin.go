// Package admin serves each service's operational endpoints: /healthz for
// liveness, /readyz for dependency checks and /metrics for Prometheus.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	DefaultAddr  = ":9090"
	checkTimeout = 2 * time.Second
)

// Check reports whether one dependency is usable.
type Check func(context.Context) error

// Addr is ADMIN_ADDR, or DefaultAddr when it is unset.
func Addr() string {
	if v := os.Getenv("ADMIN_ADDR"); v != "" {
		return v
	}
	return DefaultAddr
}

func Handler(checks map[string]Check) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		results, ok := run(r.Context(), checks)
		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(results)
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	return mux
}

func run(ctx context.Context, checks map[string]Check) (map[string]string, bool) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make(map[string]string, len(checks))
	ok := true
	for name, check := range checks {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, checkTimeout)
			defer cancel()
			err := check(ctx)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				results[name] = err.Error()
				ok = false
				return
			}
			results[name] = "ok"
		})
	}
	wg.Wait()
	return results, ok
}

// Serve runs the admin server on addr until ctx is done.
func Serve(ctx context.Context, addr string, checks map[string]Check) error {
	srv := &http.Server{Addr: addr, Handler: Handler(checks), ReadHeaderTimeout: 5 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	slog.Info("admin listening", "addr", addr)

	select {
	case err := <-errs:
		return fmt.Errorf("admin server: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("admin server shutdown: %w", err)
	}
	return nil
}

// Probe asks the local admin server on addr whether the service is ready. The
// images are distroless with no curl, so container healthchecks run the
// service binary with -healthcheck, which calls this.
func Probe(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("admin addr %q: %w", addr, err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/readyz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var results map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&results)
		return fmt.Errorf("not ready: %v", results)
	}
	return nil
}

// HandleHealthcheckFlag parses the command line and, when -healthcheck is
// given, probes the running service's /readyz and exits 0 if it is ready.
func HandleHealthcheckFlag() {
	check := flag.Bool("healthcheck", false, "probe this service's /readyz on ADMIN_ADDR and exit 0 when ready")
	flag.Parse()
	if !*check {
		return
	}

	if err := Probe(Addr()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Start runs the admin server in the background and exits the process if it
// cannot listen.
func Start(ctx context.Context, checks map[string]Check) {
	go func() {
		if err := Serve(ctx, Addr(), checks); err != nil {
			slog.Error("admin server", "err", err)
			os.Exit(1)
		}
	}()
}
