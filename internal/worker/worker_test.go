package worker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/dbtest"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type fakeProvider struct {
	mu          sync.Mutex
	deleted     []string
	statuses    map[string]provider.Status
	statusErr   error
	deleteFails int
	listed      []string
	listing     chan struct{}
}

func (f *fakeProvider) Create(ctx context.Context, id, workload string, temp provider.Template) (string, error) {
	return "container-" + id, nil
}

func (f *fakeProvider) Delete(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteFails > 0 {
		f.deleteFails--
		return errors.New("daemon busy")
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeProvider) Status(ctx context.Context, id string) (provider.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return "", f.statusErr
	}
	if s, ok := f.statuses[id]; ok {
		return s, nil
	}
	return provider.StatusRunning, nil
}

func (f *fakeProvider) setStatus(id uuid.UUID, s provider.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statuses == nil {
		f.statuses = map[string]provider.Status{}
	}
	f.statuses[id.String()] = s
}

func (f *fakeProvider) Address(ctx context.Context, id string) (string, error) { return "", nil }

func (f *fakeProvider) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	return "", nil
}

func (f *fakeProvider) List(ctx context.Context) ([]string, error) {
	if f.listing != nil {
		select {
		case f.listing <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.listed), nil
}

func (f *fakeProvider) deletedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deleted)
}

func (w *Worker) sweep(ctx context.Context) error {
	var errs []error
	for _, t := range w.tasks() {
		errs = append(errs, t.run(ctx))
	}
	return errors.Join(errs...)
}

type fixture struct {
	q        db.DBTX
	m        *env.Manager
	fake     *fakeProvider
	callerID uuid.UUID
}

func newFixture(t *testing.T, q db.DBTX) fixture {
	t.Helper()
	ctx := context.Background()
	if err := db.SetCapacityPool(ctx, q, provider.Docker, provider.Resources{CPUCore: 100, RAMGB: 100, DiskGB: 1000}); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}
	callerID, err := db.CreateCaller(ctx, q, db.NewCaller{
		Role:            db.RoleUser,
		PermittedEnv:    db.EnvProdBug,
		Quota:           provider.Resources{CPUCore: 100, RAMGB: 100, DiskGB: 1000},
		CostBudgetTotal: decimal.RequireFromString("100"),
		BudgetExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	fake := &fakeProvider{}
	return fixture{
		q:        q,
		m:        env.NewManager(q, map[provider.Type]provider.Provider{provider.Docker: fake}),
		fake:     fake,
		callerID: callerID,
	}
}

func (f fixture) env(t *testing.T, p provider.Type, status db.EnvStatus, expired bool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id, err := db.CreateEnvironment(ctx, f.q, db.NewEnvironment{
		CallerID:        f.callerID,
		Type:            db.EnvRegularTesting,
		Provider:        p,
		Resources:       provider.Resources{CPUCore: 1, RAMGB: 1, DiskGB: 1},
		HourlyRate:      decimal.RequireFromString("0.01"),
		TTL:             time.Hour,
		TemplateName:    "app",
		TemplateVersion: "v1",
	})
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	expires := "expires_at"
	if expired {
		expires = "now() - interval '1 second'"
	}
	if _, err := f.q.Exec(ctx, `UPDATE environments SET status = $2::env_status, expires_at = `+expires+` WHERE id = $1`, id, status); err != nil {
		t.Fatalf("set status and expiry: %v", err)
	}
	return id
}

func (f fixture) status(t *testing.T, id uuid.UUID) db.EnvStatus {
	t.Helper()
	e, err := db.GetEnvironment(context.Background(), f.q, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	return e.Status
}

func TestSweepDeletesExpired(t *testing.T) {
	f := newFixture(t, dbtest.Pool(t))
	expiredRunning := f.env(t, provider.Docker, db.EnvRunning, true)
	expiredProvisioning := f.env(t, provider.Docker, db.EnvProvisioning, true)
	live := f.env(t, provider.Docker, db.EnvRunning, false)
	expiredFailed := f.env(t, provider.Docker, db.EnvFailed, true)
	expiredDeleted := f.env(t, provider.Docker, db.EnvDeleted, true)

	if err := New(f.q, f.m, time.Minute).sweep(context.Background()); err != nil {
		t.Fatalf("sweep() error = %v", err)
	}
	f.m.Wait()

	for id, want := range map[uuid.UUID]db.EnvStatus{
		expiredRunning:      db.EnvDeleted,
		expiredProvisioning: db.EnvDeleted,
		live:                db.EnvRunning,
		expiredFailed:       db.EnvFailed,
		expiredDeleted:      db.EnvDeleted,
	} {
		if got := f.status(t, id); got != want {
			t.Errorf("env %s status = %s, want %s", id, got, want)
		}
	}

	got := f.fake.deletedIDs()
	slices.Sort(got)
	want := []string{expiredRunning.String(), expiredProvisioning.String()}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("provider Delete calls = %v, want %v", got, want)
	}
}

func TestSweepNothingToRemove(t *testing.T) {
	f := newFixture(t, dbtest.Tx(t))
	live := f.env(t, provider.Docker, db.EnvRunning, false)

	if err := New(f.q, f.m, time.Minute).sweep(context.Background()); err != nil {
		t.Fatalf("sweep() error = %v", err)
	}
	f.m.Wait()

	if got := f.status(t, live); got != db.EnvRunning {
		t.Errorf("status = %s, want %s", got, db.EnvRunning)
	}
	if got := f.fake.deletedIDs(); len(got) != 0 {
		t.Errorf("provider Delete calls = %v, want none", got)
	}
}

func TestSweepContinuesPastErrors(t *testing.T) {
	f := newFixture(t, dbtest.Tx(t))
	unconfigured := f.env(t, provider.Cloud, db.EnvRunning, true)
	docker := f.env(t, provider.Docker, db.EnvRunning, true)

	err := New(f.q, f.m, time.Minute).sweep(context.Background())
	f.m.Wait()

	if err == nil || !strings.Contains(err.Error(), unconfigured.String()) {
		t.Errorf("sweep() error = %v, want error naming %s", err, unconfigured)
	}
	if got := f.status(t, unconfigured); got != db.EnvRunning {
		t.Errorf("unconfigured provider env status = %s, want %s", got, db.EnvRunning)
	}
	if got := f.status(t, docker); got != db.EnvDeleted {
		t.Errorf("docker env status = %s, want %s", got, db.EnvDeleted)
	}
}

func TestSweepDeletesMissing(t *testing.T) {
	f := newFixture(t, dbtest.Pool(t))
	missing := f.env(t, provider.Docker, db.EnvRunning, false)
	unknown := f.env(t, provider.Docker, db.EnvRunning, false)
	stopped := f.env(t, provider.Docker, db.EnvRunning, false)
	healthy := f.env(t, provider.Docker, db.EnvRunning, false)
	f.fake.setStatus(missing, provider.StatusMissing)
	f.fake.setStatus(unknown, provider.StatusUnknown)
	f.fake.setStatus(stopped, provider.StatusStopped)

	if err := New(f.q, f.m, time.Minute).sweep(context.Background()); err != nil {
		t.Fatalf("sweep() error = %v", err)
	}
	f.m.Wait()

	for id, want := range map[uuid.UUID]db.EnvStatus{
		missing: db.EnvDeleted,
		unknown: db.EnvDeleted,
		stopped: db.EnvRunning,
		healthy: db.EnvRunning,
	} {
		if got := f.status(t, id); got != want {
			t.Errorf("env %s status = %s, want %s", id, got, want)
		}
	}
}

func TestSweepIgnoresMissingWhileProvisioning(t *testing.T) {
	f := newFixture(t, dbtest.Tx(t))
	provisioning := f.env(t, provider.Docker, db.EnvProvisioning, false)
	f.fake.setStatus(provisioning, provider.StatusMissing)

	if err := New(f.q, f.m, time.Minute).sweep(context.Background()); err != nil {
		t.Fatalf("sweep() error = %v", err)
	}
	f.m.Wait()

	if got := f.status(t, provisioning); got != db.EnvProvisioning {
		t.Errorf("status = %s, want %s", got, db.EnvProvisioning)
	}
	if got := f.fake.deletedIDs(); len(got) != 0 {
		t.Errorf("provider Delete calls = %v, want none", got)
	}
}

func TestSweepReportsStatusErrors(t *testing.T) {
	f := newFixture(t, dbtest.Pool(t))
	live := f.env(t, provider.Docker, db.EnvRunning, false)
	expired := f.env(t, provider.Docker, db.EnvRunning, true)
	f.fake.statusErr = errors.New("daemon unreachable")

	err := New(f.q, f.m, time.Minute).sweep(context.Background())
	f.m.Wait()

	if err == nil || !strings.Contains(err.Error(), live.String()) {
		t.Errorf("sweep() error = %v, want error naming %s", err, live)
	}
	if got := f.status(t, live); got != db.EnvRunning {
		t.Errorf("live env status = %s, want %s", got, db.EnvRunning)
	}
	if got := f.status(t, expired); got != db.EnvDeleted {
		t.Errorf("expired env status = %s, want %s", got, db.EnvDeleted)
	}
}

func TestRun(t *testing.T) {
	pool := dbtest.Pool(t)
	f := newFixture(t, pool)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		New(pool, f.m, 20*time.Millisecond).Run(ctx)
		close(done)
	}()

	first := f.env(t, provider.Docker, db.EnvRunning, true)
	later := f.env(t, provider.Docker, db.EnvRunning, false)

	deadline := time.Now().Add(5 * time.Second)
	for f.status(t, first) != db.EnvDeleted && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := f.status(t, first); got != db.EnvDeleted {
		t.Fatalf("expired env status = %s, want %s", got, db.EnvDeleted)
	}

	if _, err := pool.Exec(context.Background(), `UPDATE environments SET expires_at = now() WHERE id = $1`, later); err != nil {
		t.Fatalf("expire env: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for f.status(t, later) != db.EnvDeleted && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := f.status(t, later); got != db.EnvDeleted {
		t.Errorf("env expired after start status = %s, want %s (later sweep)", got, db.EnvDeleted)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after cancel")
	}
	f.m.Wait()
}

func TestRunBlockedTaskDoesNotStopOthers(t *testing.T) {
	pool := dbtest.Pool(t)
	f := newFixture(t, pool)
	f.fake.listing = make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		New(pool, f.m, 20*time.Millisecond).Run(ctx)
		close(done)
	}()

	select {
	case <-f.fake.listing:
	case <-time.After(5 * time.Second):
		t.Fatal("remove orphans never listed provider resources")
	}

	expired := f.env(t, provider.Docker, db.EnvRunning, true)
	deadline := time.Now().Add(5 * time.Second)
	for f.status(t, expired) != db.EnvDeleted && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := f.status(t, expired); got != db.EnvDeleted {
		t.Errorf("expired env status = %s, want %s while remove orphans is blocked", got, db.EnvDeleted)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after cancel")
	}
	f.m.Wait()
}

func (f fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.q.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (f fixture) otherCaller(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := db.CreateCaller(context.Background(), f.q, db.NewCaller{
		Role:            db.RoleUser,
		PermittedEnv:    db.EnvProdBug,
		Quota:           provider.Resources{CPUCore: 100, RAMGB: 100, DiskGB: 1000},
		CostBudgetTotal: decimal.RequireFromString("100"),
		BudgetExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	return id
}

func TestSweepStopsCallersOverBudget(t *testing.T) {
	for name, overBudget := range map[string]string{
		"budget lowered below use": `UPDATE callers SET cost_used = 50, cost_budget_total = 10 WHERE id = $1`,
		"budget expired":           `UPDATE callers SET budget_expires_at = now() - interval '1 hour' WHERE id = $1`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, dbtest.Pool(t))
			running := f.env(t, provider.Docker, db.EnvRunning, false)
			provisioning := f.env(t, provider.Docker, db.EnvProvisioning, false)
			prodBug := f.env(t, provider.Docker, db.EnvRunning, false)
			f.exec(t, `UPDATE environments SET type = 'PROD_BUG' WHERE id = $1`, prodBug)
			other := f.env(t, provider.Docker, db.EnvRunning, false)
			f.exec(t, `UPDATE environments SET caller_id = $2 WHERE id = $1`, other, f.otherCaller(t))
			f.exec(t, overBudget, f.callerID)

			if err := New(f.q, f.m, time.Minute).sweep(context.Background()); err != nil {
				t.Fatalf("sweep() error = %v", err)
			}

			for id, want := range map[uuid.UUID]db.EnvStatus{
				running:      db.EnvDeleted,
				provisioning: db.EnvDeleted,
				prodBug:      db.EnvRunning,
				other:        db.EnvRunning,
			} {
				if got := f.status(t, id); got != want {
					t.Errorf("env %s status = %s, want %s", id, got, want)
				}
			}
		})
	}
}

func TestSweepAllowsBudgetFullyReserved(t *testing.T) {
	f := newFixture(t, dbtest.Tx(t))
	live := f.env(t, provider.Docker, db.EnvRunning, false)
	f.exec(t, `UPDATE callers SET cost_used = 10, cost_budget_total = 10 WHERE id = $1`, f.callerID)

	if err := New(f.q, f.m, time.Minute).sweep(context.Background()); err != nil {
		t.Fatalf("sweep() error = %v", err)
	}
	if got := f.status(t, live); got != db.EnvRunning {
		t.Errorf("status = %s, want %s when the budget is exactly used", got, db.EnvRunning)
	}
}

func TestSweepResolvesStuckProvisioning(t *testing.T) {
	f := newFixture(t, dbtest.Pool(t))
	healthy := f.env(t, provider.Docker, db.EnvProvisioning, false)
	missing := f.env(t, provider.Docker, db.EnvProvisioning, false)
	unhealthy := f.env(t, provider.Docker, db.EnvProvisioning, false)
	starting := f.env(t, provider.Docker, db.EnvProvisioning, false)
	f.fake.setStatus(missing, provider.StatusMissing)
	f.fake.setStatus(unhealthy, provider.StatusUnhealthy)
	f.fake.setStatus(starting, provider.StatusStarting)

	w := New(f.q, f.m, time.Minute)
	w.stuckAfter = 0
	if err := w.sweep(context.Background()); err != nil {
		t.Fatalf("sweep() error = %v", err)
	}

	for id, want := range map[uuid.UUID]db.EnvStatus{
		healthy:   db.EnvRunning,
		missing:   db.EnvFailed,
		unhealthy: db.EnvFailed,
		starting:  db.EnvFailed,
	} {
		if got := f.status(t, id); got != want {
			t.Errorf("env %s status = %s, want %s", id, got, want)
		}
	}

	got := f.fake.deletedIDs()
	slices.Sort(got)
	want := []string{missing.String(), unhealthy.String(), starting.String()}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("provider Delete calls = %v, want leftovers of failed envs %v", got, want)
	}

	e, err := db.GetEnvironment(context.Background(), f.q, missing)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if e.FailureReason == nil || !strings.Contains(*e.FailureReason, "missing") {
		t.Errorf("failure reason = %v, want it to name the instance status", e.FailureReason)
	}
}

func TestSweepLeavesStuckProvisioningWhenProviderDown(t *testing.T) {
	f := newFixture(t, dbtest.Tx(t))
	stuck := f.env(t, provider.Docker, db.EnvProvisioning, false)
	f.fake.statusErr = errors.New("daemon unreachable")

	w := New(f.q, f.m, time.Minute)
	w.stuckAfter = 0
	if err := w.sweep(context.Background()); err == nil || !strings.Contains(err.Error(), stuck.String()) {
		t.Errorf("sweep() error = %v, want error naming %s", err, stuck)
	}

	if got := f.status(t, stuck); got != db.EnvProvisioning {
		t.Errorf("status = %s, want %s", got, db.EnvProvisioning)
	}
	if got := f.fake.deletedIDs(); len(got) != 0 {
		t.Errorf("provider Delete calls = %v, want none", got)
	}
}

func TestSweepWaitsBeforeResolvingProvisioning(t *testing.T) {
	f := newFixture(t, dbtest.Tx(t))
	fresh := f.env(t, provider.Docker, db.EnvProvisioning, false)
	f.fake.setStatus(fresh, provider.StatusMissing)

	if err := New(f.q, f.m, time.Minute).sweep(context.Background()); err != nil {
		t.Fatalf("sweep() error = %v", err)
	}
	if got := f.status(t, fresh); got != db.EnvProvisioning {
		t.Errorf("status = %s, want %s before the stuck threshold", got, db.EnvProvisioning)
	}
}

func TestSweepRetriesDeleting(t *testing.T) {
	f := newFixture(t, dbtest.Tx(t))
	deleting := f.env(t, provider.Docker, db.EnvDeleting, false)
	f.fake.deleteFails = 1
	w := New(f.q, f.m, time.Minute)

	if err := w.sweep(context.Background()); err == nil || !strings.Contains(err.Error(), deleting.String()) {
		t.Errorf("first sweep() error = %v, want error naming %s", err, deleting)
	}
	if got := f.status(t, deleting); got != db.EnvDeleting {
		t.Fatalf("status after failed delete = %s, want %s", got, db.EnvDeleting)
	}

	if err := w.sweep(context.Background()); err != nil {
		t.Fatalf("second sweep() error = %v", err)
	}
	if got := f.status(t, deleting); got != db.EnvDeleted {
		t.Errorf("status after retry = %s, want %s", got, db.EnvDeleted)
	}
}

func TestSweepRemovesOrphans(t *testing.T) {
	f := newFixture(t, dbtest.Tx(t))
	running := f.env(t, provider.Docker, db.EnvRunning, false)
	provisioning := f.env(t, provider.Docker, db.EnvProvisioning, false)
	failed := f.env(t, provider.Docker, db.EnvFailed, false)
	deleted := f.env(t, provider.Docker, db.EnvDeleted, false)
	unknown := uuid.NewString()
	f.fake.listed = []string{running.String(), provisioning.String(), failed.String(), deleted.String(), unknown, "not-an-env-id"}

	if err := New(f.q, f.m, time.Minute).sweep(context.Background()); err != nil {
		t.Fatalf("sweep() error = %v", err)
	}

	got := f.fake.deletedIDs()
	slices.Sort(got)
	want := []string{failed.String(), deleted.String(), unknown}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("provider Delete calls = %v, want orphans %v", got, want)
	}
	for id, want := range map[uuid.UUID]db.EnvStatus{running: db.EnvRunning, provisioning: db.EnvProvisioning} {
		if got := f.status(t, id); got != want {
			t.Errorf("env %s status = %s, want %s", id, got, want)
		}
	}
}
