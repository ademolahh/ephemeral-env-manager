package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func (s server) get(t *testing.T, key, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/envs/"+id, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func TestGetEnv(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})
	created := s.createEnv(t, key)

	rec := s.get(t, key, created.ID.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusOK, rec.Body)
	}
	got := decodeEnv(t, rec)
	if got.ID != created.ID || got.Status != db.EnvRunning {
		t.Errorf("env = %s %s, want %s RUNNING", got.ID, got.Status, created.ID)
	}
	if got.InstanceStatus != provider.StatusRunning {
		t.Errorf("instance_status = %q, want %q", got.InstanceStatus, provider.StatusRunning)
	}
	if got.Resources != created.Resources || got.TemplateName != created.TemplateName || !got.ExpiresAt.Equal(created.ExpiresAt) {
		t.Errorf("env = %+v, want fields matching create response %+v", got, created)
	}
}

func TestGetEnvDeletedHasNoInstanceStatus(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})
	created := s.createEnv(t, key)
	s.delete(t, key, created.ID.String())

	rec := s.get(t, key, created.ID.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != string(db.EnvDeleted) {
		t.Errorf("status = %v, want DELETED", body["status"])
	}
	if _, ok := body["instance_status"]; ok {
		t.Errorf("instance_status present for a deleted env: %s", rec.Body)
	}
}

func TestGetEnvAccess(t *testing.T) {
	s := newServer(t)
	owner := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})
	created := s.createEnv(t, owner)

	tests := []struct {
		name string
		key  string
		id   string
		want int
	}{
		{name: "admin", key: s.adminToken(t), id: created.ID.String(), want: http.StatusOK},
		{name: "other user", key: s.keyWithRole(t, db.RoleUser), id: created.ID.String(), want: http.StatusNotFound},
		{name: "unknown id", key: owner, id: uuid.NewString(), want: http.StatusNotFound},
		{name: "invalid id", key: owner, id: "nope", want: http.StatusBadRequest},
		{name: "no api key", key: "", id: created.ID.String(), want: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := s.get(t, tt.key, tt.id); rec.Code != tt.want {
				t.Errorf("status = %d, want %d; body %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
}
