package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func TestGetCapacity(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUMillis: 10000, RAMMB: 20480, DiskGB: 50})
	s.createEnv(t, key)

	req := httptest.NewRequest(http.MethodGet, "/capacity", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var got capacityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Pools) != 1 {
		t.Fatalf("pools = %d, want 1", len(got.Pools))
	}
	p := got.Pools[0]
	want := poolResponse{
		Provider:    provider.Docker,
		Total:       provider.Resources{CPUMillis: 100000, RAMMB: 102400, DiskGB: 1000},
		Used:        provider.Resources{CPUMillis: 3000, RAMMB: 9216, DiskGB: 22},
		NormalLimit: provider.Resources{CPUMillis: 90000, RAMMB: 92160, DiskGB: 900},
		NormalFree:  provider.Resources{CPUMillis: 87000, RAMMB: 82944, DiskGB: 878},
		ProdBugFree: provider.Resources{CPUMillis: 97000, RAMMB: 93184, DiskGB: 978},
	}
	if p != want {
		t.Errorf("pool = %+v, want %+v", p, want)
	}
}

func (s server) put(t *testing.T, key, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func TestSetCapacity(t *testing.T) {
	s := newServer(t)
	admin := s.adminToken(t)
	s.createEnv(t, s.keyWithRole(t, db.RoleUser))

	rec := s.put(t, admin, "/capacity/docker", `{"total": {"cpu_millis": 200000, "ram_mb": 153600, "disk_gb": 2000}}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusNoContent, rec.Body)
	}

	pool, err := db.LockCapacityPool(context.Background(), s.tx, provider.Docker)
	if err != nil {
		t.Fatalf("LockCapacityPool() error = %v", err)
	}
	if want := (provider.Resources{CPUMillis: 200000, RAMMB: 153600, DiskGB: 2000}); pool.Default != want {
		t.Errorf("total = %+v, want %+v", pool.Default, want)
	}
	if want := (provider.Resources{CPUMillis: 197000, RAMMB: 144384, DiskGB: 1978}); pool.Available != want {
		t.Errorf("available = %+v, want %+v (running env still reserved)", pool.Available, want)
	}
}

func TestSetCapacityForbidden(t *testing.T) {
	s := newServer(t)
	body := `{"total": {"cpu_millis": 1000, "ram_mb": 1024, "disk_gb": 1}}`

	for name, key := range map[string]string{"user": s.keyWithRole(t, db.RoleUser), "agent": s.keyWithRole(t, db.RoleAgent)} {
		if rec := s.put(t, key, "/capacity/docker", body); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, http.StatusForbidden)
		}
	}
	if rec := s.put(t, "", "/capacity/docker", body); rec.Code != http.StatusUnauthorized {
		t.Errorf("no api key: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	pool, err := db.LockCapacityPool(context.Background(), s.tx, provider.Docker)
	if err != nil {
		t.Fatalf("LockCapacityPool() error = %v", err)
	}
	if pool.Default.CPUMillis != 100000 {
		t.Errorf("total cpu = %d, want unchanged 100000", pool.Default.CPUMillis)
	}
}

func TestSetCapacityRejected(t *testing.T) {
	s := newServer(t)
	admin := s.adminToken(t)
	s.createEnv(t, s.keyWithRole(t, db.RoleUser))

	tests := []struct {
		name string
		path string
		body string
		code int
		want string
	}{
		{name: "unknown provider", path: "/capacity/aws", body: `{"total": {"cpu_millis": 1000, "ram_mb": 1024, "disk_gb": 1}}`, code: http.StatusBadRequest, want: "provider not configured"},
		{name: "malformed json", path: "/capacity/docker", body: `{`, code: http.StatusBadRequest, want: "invalid request body"},
		{name: "unknown field", path: "/capacity/docker", body: `{"cpu_millis": 1000}`, code: http.StatusBadRequest, want: "unknown field"},
		{name: "missing total", path: "/capacity/docker", body: `{}`, code: http.StatusBadRequest, want: "total is required"},
		{name: "zero resource", path: "/capacity/docker", body: `{"total": {"cpu_millis": 1000, "ram_mb": 0, "disk_gb": 1}}`, code: http.StatusBadRequest, want: "must be positive"},
		{name: "negative resource", path: "/capacity/docker", body: `{"total": {"cpu_millis": -1000, "ram_mb": 1024, "disk_gb": 1}}`, code: http.StatusBadRequest, want: "must be positive"},
		{name: "below usage", path: "/capacity/docker", body: `{"total": {"cpu_millis": 2000, "ram_mb": 102400, "disk_gb": 1000}}`, code: http.StatusConflict, want: "below current usage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := s.put(t, admin, tt.path, tt.body)
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("got %d %s, want %d containing %q", rec.Code, rec.Body, tt.code, tt.want)
			}
		})
	}

	pool, err := db.LockCapacityPool(context.Background(), s.tx, provider.Docker)
	if err != nil {
		t.Fatalf("LockCapacityPool() error = %v", err)
	}
	if want := (provider.Resources{CPUMillis: 100000, RAMMB: 102400, DiskGB: 1000}); pool.Default != want {
		t.Errorf("total = %+v, want unchanged %+v", pool.Default, want)
	}
}

func TestGetCapacityRequiresAPIKey(t *testing.T) {
	s := newServer(t)
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/capacity", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
