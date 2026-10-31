package db

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func TestLockCaller(t *testing.T) {
	tx := testTx(t)
	c := newCaller()
	id := createTestCaller(t, tx, c)

	got, err := LockCaller(context.Background(), tx, id)
	if err != nil {
		t.Fatalf("LockCaller() error = %v", err)
	}
	if got.PermittedEnv != c.PermittedEnv {
		t.Errorf("PermittedEnv = %s, want %s", got.PermittedEnv, c.PermittedEnv)
	}
	if got.Quota != c.Quota {
		t.Errorf("Quota = %+v, want %+v", got.Quota, c.Quota)
	}
	if !got.CostBudget.Equal(c.CostBudget) {
		t.Errorf("CostBudget = %s, want %s", got.CostBudget, c.CostBudget)
	}
	if got.BudgetExpired {
		t.Error("BudgetExpired = true, want false")
	}
	if got.BudgetSetAt.IsZero() {
		t.Error("BudgetSetAt is zero")
	}
}

func TestLockCallerBudgetExpired(t *testing.T) {
	tx := testTx(t)
	c := newCaller()
	c.BudgetExpiresAt = time.Now().Add(-time.Minute)
	id := createTestCaller(t, tx, c)

	got, err := LockCaller(context.Background(), tx, id)
	if err != nil {
		t.Fatalf("LockCaller() error = %v", err)
	}
	if !got.BudgetExpired {
		t.Error("BudgetExpired = false, want true")
	}
}

func TestLockCallerNotFound(t *testing.T) {
	tx := testTx(t)
	if _, err := LockCaller(context.Background(), tx, uuid.New()); !errors.Is(err, ErrCallerNotFound) {
		t.Errorf("LockCaller() error = %v, want ErrCallerNotFound", err)
	}
}

func TestCallerUsage(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	other := newCaller()
	other.GithubID = new("other")
	otherID := createTestCaller(t, tx, other)

	usage, err := CallerUsage(ctx, tx, callerID)
	if err != nil {
		t.Fatalf("CallerUsage() error = %v", err)
	}
	if usage != (provider.Resources{}) {
		t.Errorf("CallerUsage() with no envs = %+v, want zero", usage)
	}

	e := newEnvironment(callerID)
	e.Resources = provider.Resources{CPUCore: 1, RAMGB: 2, DiskGB: 3}
	createTestEnvironment(t, tx, e)
	running := createTestEnvironment(t, tx, e)
	forceStatus(t, tx, running, EnvRunning)
	for _, s := range []EnvStatus{EnvDeleting, EnvDeleted, EnvFailed} {
		forceStatus(t, tx, createTestEnvironment(t, tx, e), s)
	}
	createTestEnvironment(t, tx, newEnvironment(otherID))

	usage, err = CallerUsage(ctx, tx, callerID)
	if err != nil {
		t.Fatalf("CallerUsage() error = %v", err)
	}
	if want := (provider.Resources{CPUCore: 2, RAMGB: 4, DiskGB: 6}); usage != want {
		t.Errorf("CallerUsage() = %+v, want %+v", usage, want)
	}
}

func TestCallerSpend(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	since := txNow(t, tx)

	spend, err := CallerSpend(ctx, tx, callerID, since)
	if err != nil {
		t.Fatalf("CallerSpend() error = %v", err)
	}
	if !spend.IsZero() {
		t.Errorf("CallerSpend() with no envs = %s, want 0", spend)
	}

	e := newEnvironment(callerID)
	e.HourlyRate = decimal.RequireFromString("1.5")
	e.TTL = 2 * time.Hour
	createTestEnvironment(t, tx, e)

	deleted := createTestEnvironment(t, tx, e)
	if _, err := tx.Exec(ctx, `UPDATE environments SET status = 'DELETED', deleted_at = created_at + interval '30 minutes' WHERE id = $1`, deleted); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}

	forceStatus(t, tx, createTestEnvironment(t, tx, e), EnvFailed)

	prodBug := e
	prodBug.Type = EnvProdBug
	createTestEnvironment(t, tx, prodBug)

	before := createTestEnvironment(t, tx, e)
	if _, err := tx.Exec(ctx, `UPDATE environments SET created_at = created_at - interval '1 day' WHERE id = $1`, before); err != nil {
		t.Fatalf("backdate env: %v", err)
	}

	spend, err = CallerSpend(ctx, tx, callerID, since)
	if err != nil {
		t.Fatalf("CallerSpend() error = %v", err)
	}
	if want := decimal.RequireFromString("3.75"); !spend.Equal(want) {
		t.Errorf("CallerSpend() = %s, want %s (3 for active, 0.75 for deleted)", spend, want)
	}
}

func TestListPreemptible(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())

	seed := func(typ EnvType, p provider.Type, status EnvStatus, age time.Duration) uuid.UUID {
		e := newEnvironment(callerID)
		e.Type = typ
		e.Provider = p
		id := createTestEnvironment(t, tx, e)
		forceStatus(t, tx, id, status)
		if _, err := tx.Exec(ctx, `UPDATE environments SET created_at = created_at - make_interval(secs => $2) WHERE id = $1`, id, age.Seconds()); err != nil {
			t.Fatalf("age env: %v", err)
		}
		return id
	}

	oldRegular := seed(EnvRegularTesting, provider.Docker, EnvRunning, 2*time.Hour)
	newReview := seed(EnvPRReview, provider.Docker, EnvProvisioning, time.Hour)
	seed(EnvProdBug, provider.Docker, EnvRunning, 0)
	seed(EnvRegularTesting, provider.Cloud, EnvRunning, 0)
	seed(EnvRegularTesting, provider.Docker, EnvDeleting, 0)
	seed(EnvRegularTesting, provider.Docker, EnvFailed, 0)

	got, err := ListPreemptible(ctx, tx, provider.Docker, []EnvType{EnvRegularTesting, EnvPRReview})
	if err != nil {
		t.Fatalf("ListPreemptible() error = %v", err)
	}
	if want := []uuid.UUID{newReview, oldRegular}; !slices.Equal(envIDs(got), want) {
		t.Errorf("ListPreemptible() = %v, want %v", envIDs(got), want)
	}

	got, err = ListPreemptible(ctx, tx, provider.Docker, nil)
	if err != nil {
		t.Fatalf("ListPreemptible(nil) error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListPreemptible(nil) = %v, want empty", envIDs(got))
	}
}

func TestLockCapacityPool(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	total := provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 30}
	if err := SetCapacityPool(ctx, tx, provider.Docker, total); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}

	got, err := LockCapacityPool(ctx, tx, provider.Docker)
	if err != nil {
		t.Fatalf("LockCapacityPool() error = %v", err)
	}
	if got.Default != total || got.Available != total {
		t.Errorf("LockCapacityPool() = %+v, want default and available %+v", got, total)
	}

	if _, err := LockCapacityPool(ctx, tx, provider.Cloud); !errors.Is(err, ErrPoolNotFound) {
		t.Errorf("LockCapacityPool(cloud) error = %v, want ErrPoolNotFound", err)
	}
}

func TestReserveCapacity(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	if err := SetCapacityPool(ctx, tx, provider.Docker, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 30}); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}

	if err := ReserveCapacity(ctx, tx, provider.Docker, provider.Resources{CPUCore: 1, RAMGB: 2, DiskGB: 3}); err != nil {
		t.Fatalf("ReserveCapacity() error = %v", err)
	}
	if got, want := getPool(t, tx, provider.Docker).Available, (provider.Resources{CPUCore: 9, RAMGB: 18, DiskGB: 27}); got != want {
		t.Errorf("available = %+v, want %+v", got, want)
	}

	if err := ReserveCapacity(ctx, tx, provider.Docker, provider.Resources{CPUCore: 10}); pgCode(err) != "23514" {
		t.Errorf("ReserveCapacity() beyond pool error = %v, want check violation", err)
	}
}

func TestReserveCapacityNoPool(t *testing.T) {
	tx := testTx(t)
	if err := ReserveCapacity(context.Background(), tx, provider.Docker, provider.Resources{CPUCore: 1}); !errors.Is(err, ErrPoolNotFound) {
		t.Errorf("ReserveCapacity() error = %v, want ErrPoolNotFound", err)
	}
}

func TestStatusChangeReleasesCapacity(t *testing.T) {
	total := provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 30}
	held := provider.Resources{CPUCore: 3, RAMGB: 9, DiskGB: 22}

	tests := []struct {
		name string
		from EnvStatus
		mark func(ctx context.Context, tx DBTX, id uuid.UUID) error
		want provider.Resources
	}{
		{
			name: "running keeps reservation",
			from: EnvProvisioning,
			mark: MarkEnvironmentRunning,
			want: sub(total, held),
		},
		{
			name: "failed releases",
			from: EnvProvisioning,
			mark: func(ctx context.Context, tx DBTX, id uuid.UUID) error {
				return MarkEnvironmentFailed(ctx, tx, id, "boom")
			},
			want: total,
		},
		{
			name: "deleting releases",
			from: EnvRunning,
			mark: MarkEnvironmentDeleting,
			want: total,
		},
		{
			name: "deleted does not release twice",
			from: EnvDeleting,
			mark: MarkEnvironmentDeleted,
			want: sub(total, held),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			ctx := context.Background()
			if err := SetCapacityPool(ctx, tx, provider.Docker, total); err != nil {
				t.Fatalf("SetCapacityPool() error = %v", err)
			}
			id := createTestEnvironment(t, tx, newEnvironment(createTestCaller(t, tx, newCaller())))
			if err := ReserveCapacity(ctx, tx, provider.Docker, held); err != nil {
				t.Fatalf("ReserveCapacity() error = %v", err)
			}
			forceStatus(t, tx, id, tt.from)

			if err := tt.mark(ctx, tx, id); err != nil {
				t.Fatalf("mark error = %v", err)
			}
			if got := getPool(t, tx, provider.Docker).Available; got != tt.want {
				t.Errorf("available = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStatusChangeRejectedKeepsCapacity(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	total := provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 30}
	if err := SetCapacityPool(ctx, tx, provider.Docker, total); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}
	id := createTestEnvironment(t, tx, newEnvironment(createTestCaller(t, tx, newCaller())))
	forceStatus(t, tx, id, EnvDeleted)

	if err := MarkEnvironmentFailed(ctx, tx, id, "late"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("MarkEnvironmentFailed() error = %v, want ErrInvalidTransition", err)
	}
	if got := getPool(t, tx, provider.Docker).Available; got != total {
		t.Errorf("available = %+v, want %+v", got, total)
	}
}

func TestStatusChangeReleaseCappedAtDefault(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	id := createTestEnvironment(t, tx, newEnvironment(createTestCaller(t, tx, newCaller())))
	total := provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 30}
	if err := SetCapacityPool(ctx, tx, provider.Docker, total); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}

	if err := MarkEnvironmentFailed(ctx, tx, id, "boom"); err != nil {
		t.Fatalf("MarkEnvironmentFailed() error = %v", err)
	}
	if got := getPool(t, tx, provider.Docker).Available; got != total {
		t.Errorf("available = %+v, want %+v", got, total)
	}
}

func sub(a, b provider.Resources) provider.Resources {
	return provider.Resources{CPUCore: a.CPUCore - b.CPUCore, RAMGB: a.RAMGB - b.RAMGB, DiskGB: a.DiskGB - b.DiskGB}
}
