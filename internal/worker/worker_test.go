package worker

import (
	"context"
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
	mu      sync.Mutex
	deleted []string
}

func (f *fakeProvider) Create(ctx context.Context, id, workload string, temp provider.Template) (string, error) {
	return "container-" + id, nil
}

func (f *fakeProvider) Delete(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeProvider) Status(ctx context.Context, id string) (provider.Status, error) {
	return "", nil
}

func (f *fakeProvider) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	return "", nil
}

func (f *fakeProvider) List(ctx context.Context) ([]string, error) { return nil, nil }

func (f *fakeProvider) deletedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deleted)
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

func TestDeleteExpired(t *testing.T) {
	f := newFixture(t, dbtest.Pool(t))
	expiredRunning := f.env(t, provider.Docker, db.EnvRunning, true)
	expiredProvisioning := f.env(t, provider.Docker, db.EnvProvisioning, true)
	live := f.env(t, provider.Docker, db.EnvRunning, false)
	expiredFailed := f.env(t, provider.Docker, db.EnvFailed, true)
	expiredDeleted := f.env(t, provider.Docker, db.EnvDeleted, true)

	if err := New(f.q, f.m, time.Minute).DeleteExpired(context.Background()); err != nil {
		t.Fatalf("DeleteExpired() error = %v", err)
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

func TestDeleteExpiredNothingExpired(t *testing.T) {
	f := newFixture(t, dbtest.Tx(t))
	live := f.env(t, provider.Docker, db.EnvRunning, false)

	if err := New(f.q, f.m, time.Minute).DeleteExpired(context.Background()); err != nil {
		t.Fatalf("DeleteExpired() error = %v", err)
	}
	f.m.Wait()

	if got := f.status(t, live); got != db.EnvRunning {
		t.Errorf("status = %s, want %s", got, db.EnvRunning)
	}
	if got := f.fake.deletedIDs(); len(got) != 0 {
		t.Errorf("provider Delete calls = %v, want none", got)
	}
}

func TestDeleteExpiredContinuesPastErrors(t *testing.T) {
	f := newFixture(t, dbtest.Tx(t))
	unconfigured := f.env(t, provider.Cloud, db.EnvRunning, true)
	docker := f.env(t, provider.Docker, db.EnvRunning, true)

	err := New(f.q, f.m, time.Minute).DeleteExpired(context.Background())
	f.m.Wait()

	if err == nil || !strings.Contains(err.Error(), unconfigured.String()) {
		t.Errorf("DeleteExpired() error = %v, want error naming %s", err, unconfigured)
	}
	if got := f.status(t, unconfigured); got != db.EnvRunning {
		t.Errorf("unconfigured provider env status = %s, want %s", got, db.EnvRunning)
	}
	if got := f.status(t, docker); got != db.EnvDeleted {
		t.Errorf("docker env status = %s, want %s", got, db.EnvDeleted)
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
