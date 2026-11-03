package env

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/dbtest"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func createRunning(t *testing.T, m *Manager, q db.DBTX, callerID uuid.UUID) db.Environment {
	t.Helper()
	e, err := m.Create(context.Background(), CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "app",
		Template: dashboard,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	m.Wait()
	got, err := db.GetEnvironment(context.Background(), q, e.ID)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	return got
}

func otherCaller(t *testing.T, q db.DBTX, role db.Role) db.AuthenticatedCaller {
	t.Helper()
	id, err := db.CreateCaller(context.Background(), q, db.NewCaller{
		Role:            role,
		PermittedEnv:    db.EnvProdBug,
		Quota:           provider.Resources{CPUCore: 8, RAMGB: 32, DiskGB: 100},
		CostBudgetTotal: decimal.RequireFromString("10"),
		BudgetExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	return db.AuthenticatedCaller{ID: id, Role: role}
}

func owner(callerID uuid.UUID) db.AuthenticatedCaller {
	return db.AuthenticatedCaller{ID: callerID, Role: db.RoleUser}
}

func status(t *testing.T, q db.DBTX, id uuid.UUID) db.Environment {
	t.Helper()
	e, err := db.GetEnvironment(context.Background(), q, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	return e
}

func TestDelete(t *testing.T) {
	tx, callerID := setup(t)
	ctx := context.Background()
	fake := &fakeProvider{}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})
	e := createRunning(t, m, tx, callerID)

	got, err := m.Delete(ctx, owner(callerID), e.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got.Status != db.EnvDeleting {
		t.Errorf("Delete() status = %s, want %s", got.Status, db.EnvDeleting)
	}
	m.Wait()

	final := status(t, tx, e.ID)
	if final.Status != db.EnvDeleted || final.DeletedAt == nil {
		t.Errorf("status, deleted_at = %s, %v, want DELETED with timestamp", final.Status, final.DeletedAt)
	}
	if !slices.Equal(fake.deleted, []string{e.ID.String()}) {
		t.Errorf("provider Delete calls = %v, want [%s]", fake.deleted, e.ID)
	}

	pool, err := db.LockCapacityPool(ctx, tx, provider.Docker)
	if err != nil {
		t.Fatalf("LockCapacityPool() error = %v", err)
	}
	if pool.Available != pool.Default {
		t.Errorf("available = %+v, want %+v (capacity released)", pool.Available, pool.Default)
	}
	usage, err := db.CallerUsage(ctx, tx, callerID)
	if err != nil {
		t.Fatalf("CallerUsage() error = %v", err)
	}
	if usage != (provider.Resources{}) {
		t.Errorf("caller usage = %+v, want zero (quota freed)", usage)
	}

	again, err := m.Delete(ctx, owner(callerID), e.ID)
	if err != nil {
		t.Fatalf("Delete() again error = %v", err)
	}
	m.Wait()
	if again.Status != db.EnvDeleted || len(fake.deleted) != 1 {
		t.Errorf("second Delete() = %s with %d provider calls, want DELETED with no new call", again.Status, len(fake.deleted))
	}
}

func TestDeleteAny(t *testing.T) {
	tx, callerID := setup(t)
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: &fakeProvider{}})
	e := createRunning(t, m, tx, callerID)

	if _, err := m.DeleteAny(context.Background(), e.ID); err != nil {
		t.Fatalf("DeleteAny() error = %v", err)
	}
	m.Wait()
	if got := status(t, tx, e.ID).Status; got != db.EnvDeleted {
		t.Errorf("status = %s, want %s", got, db.EnvDeleted)
	}
	if _, err := m.DeleteAny(context.Background(), uuid.New()); !errors.Is(err, db.ErrEnvNotFound) {
		t.Errorf("DeleteAny() unknown id error = %v, want ErrEnvNotFound", err)
	}
}

func TestDeleteNotOwner(t *testing.T) {
	tx, callerID := setup(t)
	fake := &fakeProvider{}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})
	e := createRunning(t, m, tx, callerID)

	for _, role := range []db.Role{db.RoleUser, db.RoleAgent} {
		_, err := m.Delete(context.Background(), otherCaller(t, tx, role), e.ID)
		if !errors.Is(err, db.ErrEnvNotFound) {
			t.Errorf("Delete() by %s error = %v, want ErrEnvNotFound", role, err)
		}
	}
	m.Wait()
	if got := status(t, tx, e.ID).Status; got != db.EnvRunning {
		t.Errorf("status = %s, want %s", got, db.EnvRunning)
	}
	if len(fake.deleted) != 0 {
		t.Errorf("provider Delete calls = %v, want none", fake.deleted)
	}
}

func TestDeleteUnknown(t *testing.T) {
	tx, callerID := setup(t)
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: &fakeProvider{}})

	if _, err := m.Delete(context.Background(), owner(callerID), uuid.New()); !errors.Is(err, db.ErrEnvNotFound) {
		t.Errorf("Delete() error = %v, want ErrEnvNotFound", err)
	}
}

func TestDeleteFailedEnvironment(t *testing.T) {
	tx, callerID := setup(t)
	fake := &fakeProvider{err: errors.New("pull failed")}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})
	e := createRunning(t, m, tx, callerID)
	if e.Status != db.EnvFailed {
		t.Fatalf("setup status = %s, want %s", e.Status, db.EnvFailed)
	}

	got, err := m.Delete(context.Background(), owner(callerID), e.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	m.Wait()
	if got.Status != db.EnvFailed {
		t.Errorf("Delete() status = %s, want %s unchanged", got.Status, db.EnvFailed)
	}
	if len(fake.deleted) != 0 {
		t.Errorf("provider Delete calls = %v, want none", fake.deleted)
	}
}

func TestDeleteProviderFailsThenRetry(t *testing.T) {
	tx, callerID := setup(t)
	fake := &fakeProvider{deleteErr: errors.New("docker unavailable")}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})
	e := createRunning(t, m, tx, callerID)

	if _, err := m.Delete(context.Background(), owner(callerID), e.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	m.Wait()
	if got := status(t, tx, e.ID).Status; got != db.EnvDeleting {
		t.Fatalf("status after failed teardown = %s, want %s", got, db.EnvDeleting)
	}

	fake.deleteErr = nil
	got, err := m.Delete(context.Background(), owner(callerID), e.ID)
	if err != nil {
		t.Fatalf("Delete() retry error = %v", err)
	}
	if got.Status != db.EnvDeleting {
		t.Errorf("Delete() retry status = %s, want %s", got.Status, db.EnvDeleting)
	}
	m.Wait()
	if got := status(t, tx, e.ID).Status; got != db.EnvDeleted {
		t.Errorf("status after retry = %s, want %s", got, db.EnvDeleted)
	}
	if len(fake.deleted) != 2 {
		t.Errorf("provider Delete calls = %d, want 2", len(fake.deleted))
	}
}

func TestDeleteProviderUnavailable(t *testing.T) {
	tx, callerID := setup(t)
	e := createRunning(t, NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: &fakeProvider{}}), tx, callerID)
	m := NewManager(tx, map[provider.Type]provider.Provider{})

	if _, err := m.Delete(context.Background(), owner(callerID), e.ID); !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("Delete() error = %v, want ErrProviderUnavailable", err)
	}
	if got := status(t, tx, e.ID).Status; got != db.EnvRunning {
		t.Errorf("status = %s, want %s", got, db.EnvRunning)
	}
}

func TestDeleteWhileProvisioning(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	if err := db.SetHourlyPrices(ctx, pool, provider.Docker, dockerPrices); err != nil {
		t.Fatalf("SetHourlyPrices() error = %v", err)
	}
	if err := db.SetCapacityPool(ctx, pool, provider.Docker, provider.Resources{CPUCore: 100, RAMGB: 100, DiskGB: 1000}); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}
	caller := otherCaller(t, pool, db.RoleUser)
	release := make(chan struct{})
	fake := &fakeProvider{onCreate: func() { <-release }}
	m := NewManager(pool, map[provider.Type]provider.Provider{provider.Docker: fake})

	created, err := m.Create(ctx, CreateRequest{CallerID: caller.ID, Type: db.EnvRegularTesting, Workload: "app", Template: dashboard})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := m.Delete(ctx, caller, created.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got.Status != db.EnvDeleting {
		t.Errorf("Delete() status = %s, want %s", got.Status, db.EnvDeleting)
	}
	close(release)
	m.Wait()

	if got := status(t, pool, created.ID).Status; got != db.EnvDeleted {
		t.Errorf("status = %s, want %s", got, db.EnvDeleted)
	}
	if len(fake.calls) != 1 {
		t.Errorf("provider Create calls = %d, want 1", len(fake.calls))
	}
	if n := len(fake.deleted); n == 0 || fake.deleted[n-1] != created.ID.String() {
		t.Errorf("provider Delete calls = %v, want a final delete of %s after provisioning finished", fake.deleted, created.ID)
	}
}
