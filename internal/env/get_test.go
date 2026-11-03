package env

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func TestGet(t *testing.T) {
	tx, callerID := setup(t)
	fake := &fakeProvider{status: provider.StatusStarting}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})
	e := createRunning(t, m, tx, callerID)

	got, status, err := m.Get(context.Background(), owner(callerID), e.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != e.ID || got.Status != db.EnvRunning {
		t.Errorf("Get() env = %s %s, want %s RUNNING", got.ID, got.Status, e.ID)
	}
	if status != provider.StatusStarting {
		t.Errorf("Get() instance status = %s, want %s", status, provider.StatusStarting)
	}
}

func TestGetAccess(t *testing.T) {
	tx, callerID := setup(t)
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: &fakeProvider{status: provider.StatusRunning}})
	e := createRunning(t, m, tx, callerID)

	for _, role := range []db.Role{db.RoleUser, db.RoleAgent} {
		if _, _, err := m.Get(context.Background(), otherCaller(t, tx, role), e.ID); !errors.Is(err, db.ErrEnvNotFound) {
			t.Errorf("Get() by other %s error = %v, want ErrEnvNotFound", role, err)
		}
	}
	if _, _, err := m.Get(context.Background(), db.AuthenticatedCaller{}, e.ID); !errors.Is(err, db.ErrEnvNotFound) {
		t.Errorf("Get() by zero caller error = %v, want ErrEnvNotFound", err)
	}
	if _, _, err := m.Get(context.Background(), owner(callerID), uuid.New()); !errors.Is(err, db.ErrEnvNotFound) {
		t.Errorf("Get() unknown id error = %v, want ErrEnvNotFound", err)
	}
}

func TestGetAny(t *testing.T) {
	tx, callerID := setup(t)
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: &fakeProvider{status: provider.StatusRunning}})
	e := createRunning(t, m, tx, callerID)

	got, status, err := m.GetAny(context.Background(), e.ID)
	if err != nil {
		t.Fatalf("GetAny() error = %v", err)
	}
	if got.ID != e.ID || status != provider.StatusRunning {
		t.Errorf("GetAny() = %s %s, want %s %s", got.ID, status, e.ID, provider.StatusRunning)
	}
	if _, _, err := m.GetAny(context.Background(), uuid.New()); !errors.Is(err, db.ErrEnvNotFound) {
		t.Errorf("GetAny() unknown id error = %v, want ErrEnvNotFound", err)
	}
}

func TestGetFinishedSkipsProvider(t *testing.T) {
	tx, callerID := setup(t)
	fake := &fakeProvider{err: errors.New("pull failed")}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})
	e := createRunning(t, m, tx, callerID)

	got, status, err := m.Get(context.Background(), owner(callerID), e.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != db.EnvFailed || status != "" {
		t.Errorf("Get() = %s with instance status %q, want FAILED with none", got.Status, status)
	}
	if fake.statusCalls != 0 {
		t.Errorf("provider Status calls = %d, want 0", fake.statusCalls)
	}
}

func TestGetProviderUnreachable(t *testing.T) {
	tx, callerID := setup(t)
	fake := &fakeProvider{statusErr: errors.New("docker daemon not responding")}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})
	e := createRunning(t, m, tx, callerID)

	got, status, err := m.Get(context.Background(), owner(callerID), e.ID)
	if err != nil {
		t.Fatalf("Get() error = %v, want env with unknown status", err)
	}
	if got.ID != e.ID || status != provider.StatusUnknown {
		t.Errorf("Get() = %s with %s, want %s with unknown", got.ID, status, e.ID)
	}

	unconfigured := NewManager(tx, map[provider.Type]provider.Provider{})
	if _, status, err := unconfigured.Get(context.Background(), owner(callerID), e.ID); err != nil || status != provider.StatusUnknown {
		t.Errorf("Get() without provider = %s, %v, want unknown", status, err)
	}
}
