package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
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
		if err := w.DeleteExpired(ctx); err != nil && ctx.Err() == nil {
			log.Printf("delete expired environments: %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *Worker) DeleteExpired(ctx context.Context) error {
	expired, err := db.ListExpiredEnvironments(ctx, w.db)
	if err != nil {
		return err
	}

	var errs []error
	for _, e := range expired {
		if _, err := w.envs.Remove(ctx, e); err != nil {
			errs = append(errs, fmt.Errorf("environment %s: %w", e.ID, err))
			continue
		}
		log.Printf("environment %s expired at %s, deleting", e.ID, e.ExpiresAt.Format(time.RFC3339))
	}

	return errors.Join(errs...)
}
