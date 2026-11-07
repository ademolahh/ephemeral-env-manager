package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

const stuckAfter = 5 * time.Minute

type Worker struct {
	db         db.DBTX
	envs       *env.Manager
	interval   time.Duration
	stuckAfter time.Duration
}

func New(q db.DBTX, envs *env.Manager, interval time.Duration) *Worker {
	return &Worker{db: q, envs: envs, interval: interval, stuckAfter: stuckAfter}
}

type task struct {
	name string
	run  func(context.Context) error
}

func (w *Worker) tasks() []task {
	return []task{
		{"stop expired", func(ctx context.Context) error {
			return w.stop(ctx, "expired", db.ListExpiredEnvironments)
		}},
		{"stop over budget", func(ctx context.Context) error {
			return w.stop(ctx, "caller budget exhausted or expired", db.ListBudgetExhaustedEnvironments)
		}},
		{"stop missing", w.stopMissing},
		{"resolve stuck", w.resolveStuck},
		{"teardown deleting", w.teardownDeleting},
		{"remove orphans", w.removeOrphans},
	}
}

func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, t := range w.tasks() {
		wg.Go(func() { w.loop(ctx, t) })
	}
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context, t task) {
	tick := time.NewTicker(w.interval)
	defer tick.Stop()

	for {
		if err := t.run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("%s: %v", t.name, err)
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (w *Worker) stop(ctx context.Context, reason string, list func(context.Context, db.DBTX) ([]db.Environment, error)) error {
	envs, err := list(ctx, w.db)
	if err != nil {
		return err
	}

	var errs []error
	for _, e := range envs {
		if err := w.markDeleting(ctx, e, reason); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *Worker) stopMissing(ctx context.Context) error {
	active, err := db.ListActiveEnvironments(ctx, w.db)
	if err != nil {
		return err
	}

	var errs []error
	for _, e := range active {
		if e.Status != db.EnvRunning {
			continue
		}

		status, err := w.envs.Status(ctx, e)
		if err != nil {
			errs = append(errs, fmt.Errorf("environment %s: %w", e.ID, err))
			continue
		}

		if status == provider.StatusMissing || status == provider.StatusUnknown {
			if err := w.markDeleting(ctx, e, "missing from "+string(e.Provider)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (w *Worker) markDeleting(ctx context.Context, e db.Environment, reason string) error {
	if _, err := w.envs.MarkDeleting(ctx, e); err != nil {
		return fmt.Errorf("environment %s: %w", e.ID, err)
	}
	log.Printf("environment %s %s, deleting", e.ID, reason)
	return nil
}

func (w *Worker) resolveStuck(ctx context.Context) error {
	stuck, err := db.ListStuckProvisioning(ctx, w.db, w.stuckAfter)
	if err != nil {
		return err
	}

	var errs []error
	for _, e := range stuck {
		status, err := w.envs.ResolveStuck(ctx, e)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if status != "" {
			log.Printf("environment %s stuck in PROVISIONING, marked %s", e.ID, status)
		}
	}
	return errors.Join(errs...)
}

func (w *Worker) teardownDeleting(ctx context.Context) error {
	deleting, err := db.ListDeletingEnvironments(ctx, w.db)
	if err != nil {
		return err
	}

	var errs []error
	for _, e := range deleting {
		if err := w.envs.Teardown(ctx, e); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Printf("environment %s deleted", e.ID)
	}
	return errors.Join(errs...)
}

func (w *Worker) removeOrphans(ctx context.Context) error {
	removed, err := w.envs.RemoveOrphans(ctx)
	for _, id := range removed {
		log.Printf("environment %s had leftover resources with no active record, removed", id)
	}
	return err
}
