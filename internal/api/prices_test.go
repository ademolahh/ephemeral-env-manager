package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func TestSetPrices(t *testing.T) {
	s := newServer(t)
	admin := s.adminToken(t)

	rec := s.put(t, admin, "/prices/docker", `{"cpu_core": 10000, "ram_gb": 20000, "disk_gb": 1000}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusNoContent, rec.Body)
	}

	prices, err := db.GetHourlyPrices(context.Background(), s.tx, provider.Docker)
	if err != nil {
		t.Fatalf("GetHourlyPrices() error = %v", err)
	}
	if want := (provider.Prices{CPUCore: 10000, RAMGB: 20000, DiskGB: 1000}); prices != want {
		t.Errorf("prices = %+v, want %+v", prices, want)
	}

	created := s.createEnv(t, s.keyWithRole(t, db.RoleUser))
	if want := int64(232000); created.HourlyRate != want {
		t.Errorf("new env hourly rate = %d, want %d", created.HourlyRate, want)
	}
}

func TestSetPricesForbidden(t *testing.T) {
	s := newServer(t)
	body := `{"cpu_core": 9000000, "ram_gb": 9000000, "disk_gb": 9000000}`

	for name, key := range map[string]string{"user": s.keyWithRole(t, db.RoleUser), "agent": s.keyWithRole(t, db.RoleAgent)} {
		if rec := s.put(t, key, "/prices/docker", body); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, http.StatusForbidden)
		}
	}
	if rec := s.put(t, "", "/prices/docker", body); rec.Code != http.StatusUnauthorized {
		t.Errorf("no api key: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	prices, err := db.GetHourlyPrices(context.Background(), s.tx, provider.Docker)
	if err != nil {
		t.Fatalf("GetHourlyPrices() error = %v", err)
	}
	if prices.CPUCore == 9000000 {
		t.Error("prices changed by a non-admin")
	}
}

func TestSetPricesRejected(t *testing.T) {
	s := newServer(t)
	admin := s.adminToken(t)

	tests := []struct {
		name string
		path string
		body string
		code int
		want string
	}{
		{name: "unknown provider", path: "/prices/aws", body: `{"cpu_core": 1000000, "ram_gb": 1000000, "disk_gb": 1000000}`, code: http.StatusBadRequest, want: "provider not configured"},
		{name: "unconfigured provider", path: "/prices/cloud", body: `{"cpu_core": 1000000, "ram_gb": 1000000, "disk_gb": 1000000}`, code: http.StatusBadRequest, want: "provider not configured"},
		{name: "malformed json", path: "/prices/docker", body: `{`, code: http.StatusBadRequest, want: "invalid request body"},
		{name: "unknown field", path: "/prices/docker", body: `{"cpu": 1000000}`, code: http.StatusBadRequest, want: "unknown field"},
		{name: "missing price", path: "/prices/docker", body: `{"cpu_core": 1000000, "ram_gb": 1000000}`, code: http.StatusBadRequest, want: "required"},
		{name: "negative price", path: "/prices/docker", body: `{"cpu_core": 1000000, "ram_gb": -10000, "disk_gb": 1000000}`, code: http.StatusBadRequest, want: "must not be negative"},
		{name: "not a number", path: "/prices/docker", body: `{"cpu_core": "cheap", "ram_gb": 1000000, "disk_gb": 1000000}`, code: http.StatusBadRequest, want: "invalid request body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := s.put(t, admin, tt.path, tt.body)
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("got %d %s, want %d containing %q", rec.Code, rec.Body, tt.code, tt.want)
			}
		})
	}
}
