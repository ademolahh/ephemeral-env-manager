package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type Worker struct {
	db       db.DBTX
	envs     *env.Manager
	interval time.Duration
}

func New(q db.DBTX, envs *env.Manager, interval time.Duration) *Worker {
	return &Worker{db: q, envs: envs, interval: interval}
}

func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()

	for {
		if err := w.Sweep(ctx); err != nil && ctx.Err() == nil {
			log.Printf("sweep environments: %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *Worker) Sweep(ctx context.Context) error {
	active, err := db.ListActiveEnvironments(ctx, w.db)
	if err != nil {
		return err
	}

	var errs []error
	for _, e := range active {
		reason, err := w.removalReason(ctx, e)
		if err != nil {
			errs = append(errs, fmt.Errorf("environment %s: %w", e.ID, err))
			continue
		}

		if reason == "" {
			continue
		}

		if _, err := w.envs.Remove(ctx, e); err != nil {
			errs = append(errs, fmt.Errorf("environment %s: %w", e.ID, err))
			continue
		}
		log.Printf("environment %s %s, deleting", e.ID, reason)
	}

	return errors.Join(errs...)
}

func (w *Worker) removalReason(ctx context.Context, e db.Environment) (string, error) {
	if !e.ExpiresAt.After(time.Now()) {
		return "expired at " + e.ExpiresAt.Format(time.RFC3339), nil
	}

	if e.Status != db.EnvRunning {
		return "", nil
	}

	status, err := w.envs.Status(ctx, e)
	if err != nil {
		return "", err
	}

	if status == provider.StatusMissing || status == provider.StatusUnknown {
		return "missing from " + string(e.Provider), nil
	}

	return "", nil
}
