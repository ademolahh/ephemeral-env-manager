package env

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/dbtest"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func concurrentCreates(t *testing.T, m *Manager, callers []uuid.UUID, r provider.Resources) (int, []error) {
	t.Helper()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ok   int
		errs []error
	)
	for _, callerID := range callers {
		wg.Go(func() {
			_, err := m.Create(context.Background(), CreateRequest{
				CallerID: callerID,
				Type:     db.EnvRegularTesting,
				Workload: "app",
				Template: provider.Template{
					Name:            "app",
					Version:         "v1",
					DefaultProvider: provider.Docker,
					TTL:             time.Hour,
					Workload:        provider.Workload{Resources: r},
				},
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else {
				errs = append(errs, err)
			}
		})
	}
	wg.Wait()
	m.Wait()
	return ok, errs
}

type safeProvider struct{ fakeProvider }

func (s *safeProvider) Create(ctx context.Context, id, workload string, temp provider.Template) (string, error) {
	return "container-" + id, nil
}

func (s *safeProvider) Status(ctx context.Context, id string) (provider.Status, error) {
	return provider.StatusRunning, nil
}

func seedPool(t *testing.T, pool *pgxpool.Pool, capacity provider.Resources) *Manager {
	t.Helper()
	ctx := context.Background()
	if err := db.SetHourlyPrices(ctx, pool, provider.Docker, dockerPrices); err != nil {
		t.Fatalf("SetHourlyPrices() error = %v", err)
	}
	if err := db.SetCapacityPool(ctx, pool, provider.Docker, capacity); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}
	return NewManager(pool, map[provider.Type]provider.Provider{provider.Docker: &safeProvider{}})
}

func seedCaller(t *testing.T, pool *pgxpool.Pool, quota provider.Resources, budget int64) uuid.UUID {
	t.Helper()
	id, err := db.CreateCaller(context.Background(), pool, db.NewCaller{
		Role:            db.RoleUser,
		PermittedEnv:    db.EnvRegularTesting,
		Quota:           quota,
		CostBudgetTotal: budget,
		BudgetExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	return id
}

func TestConcurrentCreatesRespectQuota(t *testing.T) {
	pool := dbtest.Pool(t)
	m := seedPool(t, pool, cpu(1000))
	callerID := seedCaller(t, pool, cpu(3), 100000)

	callers := make([]uuid.UUID, 12)
	for i := range callers {
		callers[i] = callerID
	}
	ok, errs := concurrentCreates(t, m, callers, cpu(1))

	if ok != 3 {
		t.Errorf("successful creates = %d, want 3", ok)
	}
	for _, err := range errs {
		if !errors.Is(err, ErrQuotaExceeded) {
			t.Errorf("Create() error = %v, want ErrQuotaExceeded", err)
		}
	}
}

func TestConcurrentCreatesRespectBudget(t *testing.T) {
	pool := dbtest.Pool(t)
	m := seedPool(t, pool, cpu(1000))
	callerID := seedCaller(t, pool, cpu(1000), 3)

	callers := make([]uuid.UUID, 12)
	for i := range callers {
		callers[i] = callerID
	}
	ok, errs := concurrentCreates(t, m, callers, cpu(1))

	if ok != 3 {
		t.Errorf("successful creates = %d, want 3", ok)
	}
	for _, err := range errs {
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Errorf("Create() error = %v, want ErrBudgetExceeded", err)
		}
	}
}

func TestConcurrentCreatesRespectCapacity(t *testing.T) {
	pool := dbtest.Pool(t)
	m := seedPool(t, pool, cpu(10))

	callers := make([]uuid.UUID, 12)
	for i := range callers {
		callers[i] = seedCaller(t, pool, cpu(1000), 100000)
	}
	ok, errs := concurrentCreates(t, m, callers, cpu(1))

	if ok != 9 {
		t.Errorf("successful creates = %d, want 9 (90%% of pool)", ok)
	}
	for _, err := range errs {
		if !errors.Is(err, ErrCapacityExceeded) {
			t.Errorf("Create() error = %v, want ErrCapacityExceeded", err)
		}
	}

	p, err := db.LockCapacityPool(context.Background(), pool, provider.Docker)
	if err != nil {
		t.Fatalf("LockCapacityPool() error = %v", err)
	}
	if p.Available != cpu(1) {
		t.Errorf("available = %+v, want %+v", p.Available, cpu(1))
	}
}
