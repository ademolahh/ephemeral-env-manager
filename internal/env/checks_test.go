package env

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type fixture struct {
	tx   pgx.Tx
	m    *Manager
	fake *fakeProvider
}

func newFixture(t *testing.T, pool provider.Resources) fixture {
	t.Helper()
	tx := testTx(t)
	ctx := context.Background()
	if err := db.SetHourlyPrices(ctx, tx, provider.Docker, dockerPrices); err != nil {
		t.Fatalf("SetHourlyPrices() error = %v", err)
	}
	if err := db.SetCapacityPool(ctx, tx, provider.Docker, pool); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}
	fake := &fakeProvider{}
	return fixture{tx: tx, m: NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake}), fake: fake}
}

func (f fixture) caller(t *testing.T, mutate func(*db.NewCaller)) uuid.UUID {
	t.Helper()
	c := db.NewCaller{
		Role:            db.RoleUser,
		PermittedEnv:    db.EnvProdBug,
		Quota:           provider.Resources{CPUCore: 1000, RAMGB: 1000, DiskGB: 1000},
		CostBudget:      decimal.RequireFromString("1000"),
		BudgetExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if mutate != nil {
		mutate(&c)
	}
	id, err := db.CreateCaller(context.Background(), f.tx, c)
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	return id
}

func (f fixture) create(callerID uuid.UUID, typ db.EnvType, r provider.Resources) (uuid.UUID, error) {
	return f.m.Create(context.Background(), CreateRequest{
		CallerID: callerID,
		Type:     typ,
		Workload: "app:latest",
		Template: provider.Template{
			Name:            "app",
			Version:         "v1",
			DefaultProvider: provider.Docker,
			TTL:             time.Hour,
			Workload:        provider.Workload{Resources: r},
		},
	})
}

func (f fixture) mustCreate(t *testing.T, callerID uuid.UUID, typ db.EnvType, r provider.Resources) uuid.UUID {
	t.Helper()
	id, err := f.create(callerID, typ, r)
	if err != nil {
		t.Fatalf("Create(%s, %+v) error = %v", typ, r, err)
	}
	return id
}

func (f fixture) status(t *testing.T, id uuid.UUID) db.EnvStatus {
	t.Helper()
	e, err := db.GetEnvironment(context.Background(), f.tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	return e.Status
}

func (f fixture) available(t *testing.T) provider.Resources {
	t.Helper()
	p, err := db.LockCapacityPool(context.Background(), f.tx, provider.Docker)
	if err != nil {
		t.Fatalf("LockCapacityPool() error = %v", err)
	}
	return p.Available
}

func (f fixture) age(t *testing.T, id uuid.UUID, d time.Duration) {
	t.Helper()
	_, err := f.tx.Exec(context.Background(), `
		UPDATE environments SET
			created_at = created_at - make_interval(secs => $2),
			expires_at = expires_at - make_interval(secs => $2)
		WHERE id = $1`, id, d.Seconds())
	if err != nil {
		t.Fatalf("age environment: %v", err)
	}
}

func cpu(n int) provider.Resources {
	return provider.Resources{CPUCore: n}
}

func TestCreateReservesCapacity(t *testing.T) {
	f := newFixture(t, provider.Resources{CPUCore: 10, RAMGB: 10, DiskGB: 10})
	callerID := f.caller(t, nil)

	f.mustCreate(t, callerID, db.EnvRegularTesting, provider.Resources{CPUCore: 2, RAMGB: 3, DiskGB: 4})

	if got, want := f.available(t), (provider.Resources{CPUCore: 8, RAMGB: 7, DiskGB: 6}); got != want {
		t.Errorf("available = %+v, want %+v", got, want)
	}
}

func TestCreateFailureReleasesReservation(t *testing.T) {
	f := newFixture(t, provider.Resources{CPUCore: 10, RAMGB: 10, DiskGB: 10})
	callerID := f.caller(t, func(c *db.NewCaller) {
		c.Quota = cpu(2)
		c.CostBudget = decimal.RequireFromString("0.008096")
	})
	f.fake.err = errors.New("boom")

	id, err := f.create(callerID, db.EnvRegularTesting, cpu(2))
	if err == nil {
		t.Fatal("Create() error = nil, want provider error")
	}
	if got := f.status(t, id); got != db.EnvFailed {
		t.Fatalf("status = %s, want %s", got, db.EnvFailed)
	}
	if got, want := f.available(t), (provider.Resources{CPUCore: 10, RAMGB: 10, DiskGB: 10}); got != want {
		t.Errorf("available = %+v, want %+v", got, want)
	}

	f.fake.err = nil
	if _, err := f.create(callerID, db.EnvRegularTesting, cpu(2)); err != nil {
		t.Errorf("Create() after failure error = %v, want quota and budget released", err)
	}
}

func TestCreatePermission(t *testing.T) {
	tests := []struct {
		permitted db.EnvType
		typ       db.EnvType
		allowed   bool
	}{
		{db.EnvRegularTesting, db.EnvRegularTesting, true},
		{db.EnvRegularTesting, db.EnvPRReview, false},
		{db.EnvRegularTesting, db.EnvProdBug, false},
		{db.EnvPRReview, db.EnvRegularTesting, true},
		{db.EnvPRReview, db.EnvPRReview, true},
		{db.EnvPRReview, db.EnvProdBug, false},
		{db.EnvProdBug, db.EnvRegularTesting, true},
		{db.EnvProdBug, db.EnvPRReview, true},
		{db.EnvProdBug, db.EnvProdBug, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.permitted)+"/"+string(tt.typ), func(t *testing.T) {
			f := newFixture(t, cpu(10))
			callerID := f.caller(t, func(c *db.NewCaller) { c.PermittedEnv = tt.permitted })

			_, err := f.create(callerID, tt.typ, cpu(1))
			if tt.allowed && err != nil {
				t.Errorf("Create() error = %v, want allowed", err)
			}
			if !tt.allowed && !errors.Is(err, ErrNotPermitted) {
				t.Errorf("Create() error = %v, want ErrNotPermitted", err)
			}
		})
	}
}

func TestCreateInvalidType(t *testing.T) {
	f := newFixture(t, cpu(10))
	callerID := f.caller(t, nil)

	if _, err := f.create(callerID, "STAGING", cpu(1)); !errors.Is(err, ErrInvalidEnvType) {
		t.Errorf("Create() error = %v, want ErrInvalidEnvType", err)
	}
}

func TestCreateQuota(t *testing.T) {
	f := newFixture(t, provider.Resources{CPUCore: 100, RAMGB: 100, DiskGB: 100})
	callerID := f.caller(t, func(c *db.NewCaller) {
		c.Quota = provider.Resources{CPUCore: 4, RAMGB: 8, DiskGB: 10}
	})

	first := f.mustCreate(t, callerID, db.EnvRegularTesting, provider.Resources{CPUCore: 3, RAMGB: 4, DiskGB: 5})

	if _, err := f.create(callerID, db.EnvRegularTesting, provider.Resources{CPUCore: 2, RAMGB: 1, DiskGB: 1}); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("Create() over cpu quota error = %v, want ErrQuotaExceeded", err)
	}
	if _, err := f.create(callerID, db.EnvProdBug, provider.Resources{CPUCore: 1, RAMGB: 5, DiskGB: 1}); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("Create() PROD_BUG over ram quota error = %v, want ErrQuotaExceeded", err)
	}

	f.mustCreate(t, callerID, db.EnvRegularTesting, provider.Resources{CPUCore: 1, RAMGB: 4, DiskGB: 5})

	if err := db.MarkEnvironmentDeleting(context.Background(), f.tx, first); err != nil {
		t.Fatalf("MarkEnvironmentDeleting() error = %v", err)
	}
	if _, err := f.create(callerID, db.EnvRegularTesting, provider.Resources{CPUCore: 3, RAMGB: 4, DiskGB: 5}); err != nil {
		t.Errorf("Create() after deleting error = %v, want quota freed", err)
	}
}

func TestCreateQuotaPerCaller(t *testing.T) {
	f := newFixture(t, cpu(100))
	alice := f.caller(t, func(c *db.NewCaller) { c.Quota = cpu(2) })
	bob := f.caller(t, func(c *db.NewCaller) { c.Quota = cpu(2) })

	f.mustCreate(t, alice, db.EnvRegularTesting, cpu(2))
	f.mustCreate(t, bob, db.EnvRegularTesting, cpu(2))
}

func TestCreateBudget(t *testing.T) {
	f := newFixture(t, cpu(100))
	callerID := f.caller(t, func(c *db.NewCaller) { c.CostBudget = decimal.RequireFromString("0.008096") })

	f.mustCreate(t, callerID, db.EnvRegularTesting, cpu(1))
	f.mustCreate(t, callerID, db.EnvPRReview, cpu(1))

	if _, err := f.create(callerID, db.EnvRegularTesting, cpu(1)); !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("Create() over budget error = %v, want ErrBudgetExceeded", err)
	}
	if _, err := f.create(callerID, db.EnvProdBug, cpu(1)); err != nil {
		t.Errorf("Create() PROD_BUG over budget error = %v, want exempt", err)
	}
	if _, err := f.create(callerID, db.EnvRegularTesting, provider.Resources{}); err != nil {
		t.Errorf("Create() zero cost error = %v, want allowed", err)
	}
}

func TestCreateBudgetIgnoresProdBugSpend(t *testing.T) {
	f := newFixture(t, cpu(100))
	callerID := f.caller(t, func(c *db.NewCaller) { c.CostBudget = decimal.RequireFromString("0.004048") })

	f.mustCreate(t, callerID, db.EnvProdBug, cpu(10))
	f.mustCreate(t, callerID, db.EnvRegularTesting, cpu(1))
}

func TestCreateBudgetExpired(t *testing.T) {
	f := newFixture(t, cpu(100))
	callerID := f.caller(t, func(c *db.NewCaller) { c.BudgetExpiresAt = time.Now().Add(-time.Hour) })

	if _, err := f.create(callerID, db.EnvRegularTesting, cpu(1)); !errors.Is(err, ErrBudgetExpired) {
		t.Errorf("Create() error = %v, want ErrBudgetExpired", err)
	}
	if _, err := f.create(callerID, db.EnvProdBug, cpu(1)); err != nil {
		t.Errorf("Create() PROD_BUG error = %v, want exempt", err)
	}
}

func TestCreateBudgetPeriod(t *testing.T) {
	f := newFixture(t, cpu(100))
	callerID := f.caller(t, func(c *db.NewCaller) { c.CostBudget = decimal.RequireFromString("0.004048") })

	old := f.mustCreate(t, callerID, db.EnvRegularTesting, cpu(1))
	if _, err := f.create(callerID, db.EnvRegularTesting, cpu(1)); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Create() error = %v, want ErrBudgetExceeded", err)
	}

	f.age(t, old, 48*time.Hour)
	if _, err := f.create(callerID, db.EnvRegularTesting, cpu(1)); err != nil {
		t.Errorf("Create() error = %v, want spend before budget period ignored", err)
	}
}

func TestCreateCapacityNormalLimit(t *testing.T) {
	f := newFixture(t, cpu(10))
	callerID := f.caller(t, nil)

	first := f.mustCreate(t, callerID, db.EnvRegularTesting, cpu(9))

	if _, err := f.create(callerID, db.EnvRegularTesting, cpu(1)); !errors.Is(err, ErrCapacityExceeded) {
		t.Errorf("Create() REGULAR_TESTING over 90%% error = %v, want ErrCapacityExceeded", err)
	}
	if got := f.status(t, first); got != db.EnvRunning {
		t.Errorf("existing env status = %s, want %s (no preemption within type)", got, db.EnvRunning)
	}
}

func TestCreateCapacityProdBugFullPool(t *testing.T) {
	f := newFixture(t, cpu(10))
	callerID := f.caller(t, nil)

	first := f.mustCreate(t, callerID, db.EnvPRReview, cpu(9))
	f.mustCreate(t, callerID, db.EnvProdBug, cpu(1))

	if got := f.status(t, first); got != db.EnvRunning {
		t.Errorf("PR_REVIEW status = %s, want %s (PROD_BUG fits in last 10%%)", got, db.EnvRunning)
	}
	if got := f.available(t); got != (provider.Resources{}) {
		t.Errorf("available = %+v, want zero", got)
	}

	f.mustCreate(t, callerID, db.EnvProdBug, cpu(1))
	if got := f.status(t, first); got != db.EnvDeleting {
		t.Errorf("PR_REVIEW status = %s, want %s (preempted once pool is full)", got, db.EnvDeleting)
	}
	if _, err := f.create(callerID, db.EnvProdBug, cpu(9)); !errors.Is(err, ErrCapacityExceeded) {
		t.Errorf("Create() PROD_BUG with nothing to preempt error = %v, want ErrCapacityExceeded", err)
	}
}

func TestCreatePreemptsNewestLowerPriority(t *testing.T) {
	f := newFixture(t, cpu(10))
	callerID := f.caller(t, nil)

	older := f.mustCreate(t, callerID, db.EnvRegularTesting, cpu(5))
	f.age(t, older, time.Minute)
	newer := f.mustCreate(t, callerID, db.EnvRegularTesting, cpu(4))

	id := f.mustCreate(t, callerID, db.EnvPRReview, cpu(4))

	if got := f.status(t, newer); got != db.EnvDeleting {
		t.Errorf("newer REGULAR_TESTING status = %s, want %s", got, db.EnvDeleting)
	}
	if got := f.status(t, older); got != db.EnvRunning {
		t.Errorf("older REGULAR_TESTING status = %s, want %s", got, db.EnvRunning)
	}
	if got := f.status(t, id); got != db.EnvRunning {
		t.Errorf("PR_REVIEW status = %s, want %s", got, db.EnvRunning)
	}
	if got := f.available(t); got != cpu(1) {
		t.Errorf("available = %+v, want %+v", got, cpu(1))
	}
}

func TestCreatePreemptsLowestPriorityFirst(t *testing.T) {
	f := newFixture(t, cpu(10))
	callerID := f.caller(t, nil)

	regular := f.mustCreate(t, callerID, db.EnvRegularTesting, cpu(5))
	f.age(t, regular, time.Minute)
	review := f.mustCreate(t, callerID, db.EnvPRReview, cpu(4))

	f.mustCreate(t, callerID, db.EnvProdBug, cpu(5))

	if got := f.status(t, regular); got != db.EnvDeleting {
		t.Errorf("REGULAR_TESTING status = %s, want %s", got, db.EnvDeleting)
	}
	if got := f.status(t, review); got != db.EnvRunning {
		t.Errorf("PR_REVIEW status = %s, want %s", got, db.EnvRunning)
	}
}

func TestCreatePreemptsOnlyWhatHelps(t *testing.T) {
	f := newFixture(t, provider.Resources{CPUCore: 10, RAMGB: 10})
	callerID := f.caller(t, nil)

	ramHeavy := f.mustCreate(t, callerID, db.EnvRegularTesting, provider.Resources{CPUCore: 1, RAMGB: 9})
	f.age(t, ramHeavy, time.Minute)
	cpuOnly := f.mustCreate(t, callerID, db.EnvRegularTesting, provider.Resources{CPUCore: 4})

	f.mustCreate(t, callerID, db.EnvPRReview, provider.Resources{CPUCore: 1, RAMGB: 5})

	if got := f.status(t, cpuOnly); got != db.EnvRunning {
		t.Errorf("cpu-only env status = %s, want %s", got, db.EnvRunning)
	}
	if got := f.status(t, ramHeavy); got != db.EnvDeleting {
		t.Errorf("ram-heavy env status = %s, want %s", got, db.EnvDeleting)
	}
}

func TestCreatePreemptionInsufficient(t *testing.T) {
	f := newFixture(t, cpu(10))
	callerID := f.caller(t, nil)

	review := f.mustCreate(t, callerID, db.EnvPRReview, cpu(5))
	regular := f.mustCreate(t, callerID, db.EnvRegularTesting, cpu(4))

	if _, err := f.create(callerID, db.EnvPRReview, cpu(6)); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("Create() error = %v, want ErrCapacityExceeded", err)
	}
	for _, id := range []uuid.UUID{review, regular} {
		if got := f.status(t, id); got != db.EnvRunning {
			t.Errorf("env %s status = %s, want %s (nothing preempted)", id, got, db.EnvRunning)
		}
	}
	if got := f.available(t); got != cpu(1) {
		t.Errorf("available = %+v, want %+v", got, cpu(1))
	}
	if n := countEnvironments(t, f.tx); n != 2 {
		t.Errorf("environments = %d, want 2", n)
	}
}

func TestCreateNoCapacityPool(t *testing.T) {
	tx, callerID := setup(t)
	if _, err := tx.Exec(context.Background(), `DELETE FROM capacity_pools`); err != nil {
		t.Fatalf("delete pools: %v", err)
	}
	fake := &fakeProvider{}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})

	_, err := m.Create(context.Background(), CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "app",
		Template: dashboard,
	})
	if !errors.Is(err, db.ErrPoolNotFound) {
		t.Errorf("Create() error = %v, want ErrPoolNotFound", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("provider Create calls = %d, want 0", len(fake.calls))
	}
}

func TestEstimatedCost(t *testing.T) {
	tests := []struct {
		rate string
		ttl  time.Duration
		want string
	}{
		{"0.054569", 4 * time.Hour, "0.218276"},
		{"0.004048", 30 * time.Minute, "0.002024"},
		{"1", 0, "0"},
	}
	for _, tt := range tests {
		got := estimatedCost(decimal.RequireFromString(tt.rate), tt.ttl)
		if !got.Equal(decimal.RequireFromString(tt.want)) {
			t.Errorf("estimatedCost(%s, %s) = %s, want %s", tt.rate, tt.ttl, got, tt.want)
		}
	}
}
