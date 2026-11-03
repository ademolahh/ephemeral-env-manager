package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func TestGetCapacity(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})
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
		Total:       provider.Resources{CPUCore: 100, RAMGB: 100, DiskGB: 1000},
		Used:        provider.Resources{CPUCore: 3, RAMGB: 9, DiskGB: 22},
		NormalLimit: provider.Resources{CPUCore: 90, RAMGB: 90, DiskGB: 900},
		NormalFree:  provider.Resources{CPUCore: 87, RAMGB: 81, DiskGB: 878},
		ProdBugFree: provider.Resources{CPUCore: 97, RAMGB: 91, DiskGB: 978},
	}
	if p != want {
		t.Errorf("pool = %+v, want %+v", p, want)
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
