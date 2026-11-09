package db

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

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
	if got.CostBudgetTotal != c.CostBudgetTotal || got.CostUsed != 0 {
		t.Errorf("total, used = %d, %d, want %d, 0", got.CostBudgetTotal, got.CostUsed, c.CostBudgetTotal)
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
			mark: markRunning,
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

func TestEnvironmentByIdempotencyKey(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	e := newEnvironment(callerID)
	e.IdempotencyKey = new("req-1")
	id := createTestEnvironment(t, tx, e)
	createTestEnvironment(t, tx, newEnvironment(callerID))

	got, err := EnvironmentByIdempotencyKey(ctx, tx, callerID, "req-1")
	if err != nil {
		t.Fatalf("EnvironmentByIdempotencyKey() error = %v", err)
	}
	if got.ID != id {
		t.Errorf("EnvironmentByIdempotencyKey() id = %s, want %s", got.ID, id)
	}

	if _, err := EnvironmentByIdempotencyKey(ctx, tx, callerID, "req-2"); !errors.Is(err, ErrEnvNotFound) {
		t.Errorf("EnvironmentByIdempotencyKey(unknown) error = %v, want ErrEnvNotFound", err)
	}
	if _, err := EnvironmentByIdempotencyKey(ctx, tx, uuid.New(), "req-1"); !errors.Is(err, ErrEnvNotFound) {
		t.Errorf("EnvironmentByIdempotencyKey(other caller) error = %v, want ErrEnvNotFound", err)
	}

	if _, err := CreateEnvironment(ctx, tx, e); pgCode(err) != "23505" {
		t.Errorf("CreateEnvironment() duplicate key error = %v, want unique violation", err)
	}
}

func TestStatusChangeRefundsUnusedBudget(t *testing.T) {
	failed := func(ctx context.Context, q DBTX, id uuid.UUID) error {
		return MarkEnvironmentFailed(ctx, q, id, "boom")
	}

	tests := []struct {
		name   string
		from   EnvStatus
		mark   func(ctx context.Context, q DBTX, id uuid.UUID) error
		to     EnvStatus
		age    time.Duration
		refund int64
	}{
		{name: "failed after 30m of a 2h ttl", from: EnvRunning, mark: failed, to: EnvFailed, age: 30 * time.Minute, refund: 225},
		{name: "failed immediately", from: EnvProvisioning, mark: failed, to: EnvFailed, refund: 300},
		{name: "deleted after 30m of a 2h ttl", from: EnvDeleting, mark: MarkEnvironmentDeleted, to: EnvDeleted, age: 30 * time.Minute, refund: 225},
		{name: "deleted after the ttl ran out", from: EnvDeleting, mark: MarkEnvironmentDeleted, to: EnvDeleted, age: 3 * time.Hour, refund: 0},
		{name: "deleting does not refund yet", from: EnvRunning, mark: MarkEnvironmentDeleting, to: EnvDeleting, age: 30 * time.Minute, refund: 0},
		{name: "running does not refund", from: EnvProvisioning, mark: markRunning, to: EnvRunning, refund: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			ctx := context.Background()
			callerID := createTestCaller(t, tx, newCaller())
			e := newEnvironment(callerID)
			e.HourlyRate = 1500000
			e.TTL = 2 * time.Hour
			e.EstimatedCost = 300
			id := createTestEnvironment(t, tx, e)

			used := func() int64 {
				t.Helper()
				c, err := LockCaller(ctx, tx, callerID)
				if err != nil {
					t.Fatalf("LockCaller() error = %v", err)
				}
				return c.CostUsed
			}
			if got, want := used(), int64(300); got != want {
				t.Fatalf("used after create = %d, want %d reserved", got, want)
			}

			if _, err := tx.Exec(ctx, `
				UPDATE environments SET
					created_at = created_at - make_interval(secs => $2),
					expires_at = expires_at - make_interval(secs => $2)
				WHERE id = $1`, id, tt.age.Seconds()); err != nil {
				t.Fatalf("age env: %v", err)
			}
			forceStatus(t, tx, id, tt.from)

			if err := tt.mark(ctx, tx, id); err != nil {
				t.Fatalf("mark error = %v", err)
			}

			if got, want := used(), 300-tt.refund; got != want {
				t.Errorf("used = %d, want %d (refund %d)", got, want, tt.refund)
			}
			var charged *int64
			if err := tx.QueryRow(ctx, `SELECT charged_cost FROM environments WHERE id = $1`, id).Scan(&charged); err != nil {
				t.Fatalf("read charged_cost: %v", err)
			}
			settles := tt.to == EnvFailed || tt.to == EnvDeleted
			switch {
			case !settles && charged != nil:
				t.Errorf("charged_cost = %d, want NULL until the env ends", *charged)
			case settles && (charged == nil || *charged != 300-tt.refund):
				t.Errorf("charged_cost = %v, want %d", charged, 300-tt.refund)
			}
		})
	}
}

func TestRejectedStatusChangeDoesNotRefund(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	e := newEnvironment(callerID)
	e.EstimatedCost = 300
	id := createTestEnvironment(t, tx, e)
	forceStatus(t, tx, id, EnvFailed)

	if err := MarkEnvironmentFailed(ctx, tx, id, "again"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("MarkEnvironmentFailed() error = %v, want ErrInvalidTransition", err)
	}
	c, err := LockCaller(ctx, tx, callerID)
	if err != nil {
		t.Fatalf("LockCaller() error = %v", err)
	}
	if want := int64(300); c.CostUsed != want {
		t.Errorf("used = %d, want %d", c.CostUsed, want)
	}
}

func TestCallerBudgetTotalAndRemaining(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	e := newEnvironment(callerID)
	e.EstimatedCost = 300
	createTestEnvironment(t, tx, e)

	b, err := GetCallerBudget(ctx, tx, callerID)
	if err != nil {
		t.Fatalf("GetCallerBudget() error = %v", err)
	}
	if b.CostBudgetTotal != 1234 || b.CostUsed != 300 {
		t.Errorf("total, used = %d, %d, want 1234, 300", b.CostBudgetTotal, b.CostUsed)
	}

	if err := SetCallerBudget(ctx, tx, callerID, provider.Resources{}, 2000, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetCallerBudget() error = %v", err)
	}
	if b, err = GetCallerBudget(ctx, tx, callerID); err != nil {
		t.Fatalf("GetCallerBudget() error = %v", err)
	}
	if b.CostBudgetTotal != 2000 || b.CostUsed != 300 {
		t.Errorf("after set: total, used = %d, %d, want 2000, 300 (update leaves usage alone)", b.CostBudgetTotal, b.CostUsed)
	}
}

func TestSetBudgetBelowCurrentUsage(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	e := newEnvironment(callerID)
	e.EstimatedCost = 300
	id := createTestEnvironment(t, tx, e)

	if err := SetCallerBudget(ctx, tx, callerID, provider.Resources{}, 200, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetCallerBudget() error = %v", err)
	}
	b, err := GetCallerBudget(ctx, tx, callerID)
	if err != nil {
		t.Fatalf("GetCallerBudget() error = %v", err)
	}
	if b.CostUsed != 300 || b.CostBudgetTotal != 200 {
		t.Errorf("total, used = %d, %d, want 200, 300 (usage can exceed a lowered total)", b.CostBudgetTotal, b.CostUsed)
	}

	if err := MarkEnvironmentFailed(ctx, tx, id, "boom"); err != nil {
		t.Fatalf("MarkEnvironmentFailed() error = %v", err)
	}
	if b, err = GetCallerBudget(ctx, tx, callerID); err != nil {
		t.Fatalf("GetCallerBudget() error = %v", err)
	}
	if b.CostUsed != 0 {
		t.Errorf("used = %d, want 0 after an immediate failure", b.CostUsed)
	}
}

func TestProdBugCostTracked(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	e := newEnvironment(callerID)
	e.Type = EnvProdBug
	e.HourlyRate = 1500000
	e.TTL = 2 * time.Hour
	e.EstimatedCost = 300
	id := createTestEnvironment(t, tx, e)

	budget := func() CallerBudget {
		t.Helper()
		b, err := GetCallerBudget(ctx, tx, callerID)
		if err != nil {
			t.Fatalf("GetCallerBudget() error = %v", err)
		}
		return b
	}
	if b := budget(); b.ProdBugCost != 300 || b.CostUsed != 0 {
		t.Errorf("after create: prod bug cost, used = %d, %d, want 300, 0 (budget untouched)", b.ProdBugCost, b.CostUsed)
	}
	var estimated, reserved int64
	if err := tx.QueryRow(ctx, `SELECT estimated_cost, reserved_cost FROM environments WHERE id = $1`, id).Scan(&estimated, &reserved); err != nil {
		t.Fatalf("read costs: %v", err)
	}
	if estimated != 300 || reserved != 0 {
		t.Errorf("estimated, reserved = %d, %d, want 300 recorded and 0 reserved", estimated, reserved)
	}

	if err := SetCallerBudget(ctx, tx, callerID, provider.Resources{}, 2000, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetCallerBudget() error = %v", err)
	}
	if b := budget(); b.ProdBugCost != 300 || b.CostBudgetTotal != 2000 {
		t.Errorf("after update: prod bug cost, total = %d, %d, want 300 unchanged, 2000", b.ProdBugCost, b.CostBudgetTotal)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE environments SET created_at = created_at - interval '30 minutes', expires_at = expires_at - interval '30 minutes'
		WHERE id = $1`, id); err != nil {
		t.Fatalf("age env: %v", err)
	}
	if err := MarkEnvironmentFailed(ctx, tx, id, "boom"); err != nil {
		t.Fatalf("MarkEnvironmentFailed() error = %v", err)
	}
	if b := budget(); b.ProdBugCost != 75 || b.CostUsed != 0 {
		t.Errorf("after failure: prod bug cost, used = %d, %d, want 75 for 30m used, 0", b.ProdBugCost, b.CostUsed)
	}
	var charged int64
	if err := tx.QueryRow(ctx, `SELECT charged_cost FROM environments WHERE id = $1`, id).Scan(&charged); err != nil {
		t.Fatalf("read charged_cost: %v", err)
	}
	if charged != 75 {
		t.Errorf("charged_cost = %d, want 75", charged)
	}
}
