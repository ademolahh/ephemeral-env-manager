package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func (s server) keyWithRole(t *testing.T, role db.Role) string {
	t.Helper()
	_, key := s.callerWithRole(t, role)
	return key
}

func (s server) callerWithRole(t *testing.T, role db.Role) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	callerID, err := db.CreateCaller(ctx, s.tx, db.NewCaller{
		Role:            role,
		PermittedEnv:    db.EnvProdBug,
		Quota:           provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50},
		CostBudgetTotal: decimal.RequireFromString("100"),
		BudgetExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	key, hash := auth.GenerateAPIKey()
	if _, err := db.CreateAPIKey(ctx, s.tx, callerID, hash, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}
	return callerID, key
}

func (s server) delete(t *testing.T, key, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/envs/"+id, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	s.m.Wait()
	return rec
}

func (s server) createEnv(t *testing.T, key string) db.Environment {
	t.Helper()
	rec := s.post(t, key, validBody, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	return decodeEnv(t, rec)
}

func TestDeleteEnv(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})
	created := s.createEnv(t, key)

	rec := s.delete(t, key, created.ID.String())
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	if got := decodeEnv(t, rec); got.ID != created.ID || got.Status != db.EnvDeleting {
		t.Errorf("response = %s %s, want %s DELETING", got.ID, got.Status, created.ID)
	}

	stored, err := db.GetEnvironment(context.Background(), s.tx, created.ID)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if stored.Status != db.EnvDeleted {
		t.Errorf("stored status = %s, want %s", stored.Status, db.EnvDeleted)
	}

	again := s.delete(t, key, created.ID.String())
	if again.Code != http.StatusOK || decodeEnv(t, again).Status != db.EnvDeleted {
		t.Errorf("second delete = %d %s, want 200 DELETED", again.Code, again.Body)
	}
}

func TestDeleteEnvByAdmin(t *testing.T) {
	s := newServer(t)
	owner := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})
	created := s.createEnv(t, owner)

	if rec := s.delete(t, s.keyWithRole(t, db.RoleAdmin), created.ID.String()); rec.Code != http.StatusAccepted {
		t.Errorf("admin delete status = %d, want %d; body %s", rec.Code, http.StatusAccepted, rec.Body)
	}
}

func TestDeleteEnvNotFound(t *testing.T) {
	s := newServer(t)
	owner := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})
	created := s.createEnv(t, owner)

	tests := []struct {
		name string
		key  string
		id   string
	}{
		{name: "other user's environment", key: s.keyWithRole(t, db.RoleUser), id: created.ID.String()},
		{name: "other agent's environment", key: s.keyWithRole(t, db.RoleAgent), id: created.ID.String()},
		{name: "unknown id", key: owner, id: uuid.NewString()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := s.delete(t, tt.key, tt.id); rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d; body %s", rec.Code, http.StatusNotFound, rec.Body)
			}
		})
	}

	stored, err := db.GetEnvironment(context.Background(), s.tx, created.ID)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if stored.Status != db.EnvRunning {
		t.Errorf("status = %s, want %s", stored.Status, db.EnvRunning)
	}
}

func TestDeleteEnvBadRequest(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})

	if rec := s.delete(t, key, "not-a-uuid"); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if rec := s.delete(t, "", uuid.NewString()); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
