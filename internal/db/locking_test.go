package db

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ademolahh/ephemeral-env-manager/internal/dbtest"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func waitForLock(t *testing.T, pool *pgxpool.Pool, pid uint32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		err := pool.QueryRow(context.Background(), `
			SELECT coalesce(wait_event_type = 'Lock', false) FROM pg_stat_activity WHERE pid = $1`, int32(pid),
		).Scan(&waiting)
		if err != nil {
			t.Fatalf("pg_stat_activity: %v", err)
		}
		if waiting {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("backend %d never waited on a lock", pid)
}

func TestReleaseDoesNotDeadlockWithPreemption(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	total := provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 30}
	if err := SetCapacityPool(ctx, pool, provider.Docker, total); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}
	callerID, err := CreateCaller(ctx, pool, newCaller())
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	e := newEnvironment(callerID)
	id, err := CreateEnvironment(ctx, pool, e)
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	if err := ReserveCapacity(ctx, pool, provider.Docker, e.Resources); err != nil {
		t.Fatalf("ReserveCapacity() error = %v", err)
	}

	reserver, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer reserver.Rollback(ctx)
	if _, err := LockCapacityPool(ctx, reserver, provider.Docker); err != nil {
		t.Fatalf("LockCapacityPool() error = %v", err)
	}

	marker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer marker.Release()

	done := make(chan error, 1)
	go func() { done <- MarkEnvironmentFailed(ctx, marker, id, "boom") }()
	waitForLock(t, pool, marker.Conn().PgConn().PID())

	if _, err := ListPreemptible(ctx, reserver, provider.Docker, []EnvType{e.Type}); err != nil {
		t.Fatalf("ListPreemptible() while a release is waiting: %v", err)
	}
	if err := reserver.Commit(ctx); err != nil {
		t.Fatalf("commit reserver: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MarkEnvironmentFailed() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("MarkEnvironmentFailed() did not finish")
	}

	got, err := GetEnvironment(ctx, pool, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if got.Status != EnvFailed {
		t.Errorf("status = %s, want %s", got.Status, EnvFailed)
	}
	p, err := LockCapacityPool(ctx, pool, provider.Docker)
	if err != nil {
		t.Fatalf("LockCapacityPool() error = %v", err)
	}
	if p.Available != total {
		t.Errorf("available = %+v, want %+v (capacity released)", p.Available, total)
	}
}

func TestRefundDoesNotDeadlockWithCreate(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	if err := SetCapacityPool(ctx, pool, provider.Docker, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 30}); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}
	callerID, err := CreateCaller(ctx, pool, newCaller())
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	e := newEnvironment(callerID)
	e.EstimatedCost = 200
	id, err := CreateEnvironment(ctx, pool, e)
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	if err := ReserveCapacity(ctx, pool, provider.Docker, e.Resources); err != nil {
		t.Fatalf("ReserveCapacity() error = %v", err)
	}

	creator, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer creator.Rollback(ctx)
	if _, err := LockCaller(ctx, creator, callerID); err != nil {
		t.Fatalf("LockCaller() error = %v", err)
	}

	marker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer marker.Release()

	done := make(chan error, 1)
	go func() { done <- MarkEnvironmentFailed(ctx, marker, id, "boom") }()
	waitForLock(t, pool, marker.Conn().PgConn().PID())

	if _, err := LockCapacityPool(ctx, creator, provider.Docker); err != nil {
		t.Fatalf("LockCapacityPool() while a refund is waiting: %v", err)
	}
	if err := creator.Commit(ctx); err != nil {
		t.Fatalf("commit creator: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MarkEnvironmentFailed() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("MarkEnvironmentFailed() did not finish")
	}

	c, err := LockCaller(ctx, pool, callerID)
	if err != nil {
		t.Fatalf("LockCaller() error = %v", err)
	}
	if c.CostUsed >= 200 {
		t.Errorf("used = %d, want most of the 200 reserved refunded", c.CostUsed)
	}
}
