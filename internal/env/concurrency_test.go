package env

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := "eem_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s; SET search_path TO %s", schema, schema)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), url)
		if err != nil {
			t.Errorf("connect for cleanup: %v", err)
			return
		}
		defer c.Close(context.Background())
		if _, err := c.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

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
	return ok, errs
}

type safeProvider struct{ fakeProvider }

func (s *safeProvider) Create(ctx context.Context, id, workload string, temp provider.Template) (string, error) {
	return "container-" + id, nil
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

func seedCaller(t *testing.T, pool *pgxpool.Pool, quota provider.Resources, budget string) uuid.UUID {
	t.Helper()
	id, err := db.CreateCaller(context.Background(), pool, db.NewCaller{
		Role:            db.RoleUser,
		PermittedEnv:    db.EnvRegularTesting,
		Quota:           quota,
		CostBudget:      decimal.RequireFromString(budget),
		BudgetExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	return id
}

func TestConcurrentCreatesRespectQuota(t *testing.T) {
	pool := testPool(t)
	m := seedPool(t, pool, cpu(1000))
	callerID := seedCaller(t, pool, cpu(3), "1000")

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
	pool := testPool(t)
	m := seedPool(t, pool, cpu(1000))
	callerID := seedCaller(t, pool, cpu(1000), "0.012144")

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
	pool := testPool(t)
	m := seedPool(t, pool, cpu(10))

	callers := make([]uuid.UUID, 12)
	for i := range callers {
		callers[i] = seedCaller(t, pool, cpu(1000), "1000")
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
