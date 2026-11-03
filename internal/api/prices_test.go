package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func TestSetPrices(t *testing.T) {
	s := newServer(t)
	admin := s.adminToken(t)

	rec := s.put(t, admin, "/prices/docker", `{"cpu_core": "0.01", "ram_gb": 0.02, "disk_gb": "0.001"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusNoContent, rec.Body)
	}

	prices, err := db.GetHourlyPrices(context.Background(), s.tx, provider.Docker)
	if err != nil {
		t.Fatalf("GetHourlyPrices() error = %v", err)
	}
	for name, got := range map[string]struct{ got, want decimal.Decimal }{
		"cpu_core": {prices.CPUCore, decimal.RequireFromString("0.01")},
		"ram_gb":   {prices.RAMGB, decimal.RequireFromString("0.02")},
		"disk_gb":  {prices.DiskGB, decimal.RequireFromString("0.001")},
	} {
		if !got.got.Equal(got.want) {
			t.Errorf("%s = %s, want %s", name, got.got, got.want)
		}
	}

	created := s.createEnv(t, s.keyWithRole(t, db.RoleUser))
	if want := decimal.RequireFromString("0.232"); !created.HourlyRate.Equal(want) {
		t.Errorf("new env hourly rate = %s, want %s", created.HourlyRate, want)
	}
}

func TestSetPricesForbidden(t *testing.T) {
	s := newServer(t)
	body := `{"cpu_core": "9", "ram_gb": "9", "disk_gb": "9"}`

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
	if prices.CPUCore.Equal(decimal.NewFromInt(9)) {
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
		{name: "unknown provider", path: "/prices/aws", body: `{"cpu_core": "1", "ram_gb": "1", "disk_gb": "1"}`, code: http.StatusBadRequest, want: "provider not configured"},
		{name: "unconfigured provider", path: "/prices/cloud", body: `{"cpu_core": "1", "ram_gb": "1", "disk_gb": "1"}`, code: http.StatusBadRequest, want: "provider not configured"},
		{name: "malformed json", path: "/prices/docker", body: `{`, code: http.StatusBadRequest, want: "invalid request body"},
		{name: "unknown field", path: "/prices/docker", body: `{"cpu": "1"}`, code: http.StatusBadRequest, want: "unknown field"},
		{name: "missing price", path: "/prices/docker", body: `{"cpu_core": "1", "ram_gb": "1"}`, code: http.StatusBadRequest, want: "required"},
		{name: "negative price", path: "/prices/docker", body: `{"cpu_core": "1", "ram_gb": "-0.01", "disk_gb": "1"}`, code: http.StatusBadRequest, want: "must not be negative"},
		{name: "not a number", path: "/prices/docker", body: `{"cpu_core": "cheap", "ram_gb": "1", "disk_gb": "1"}`, code: http.StatusBadRequest, want: "invalid request body"},
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
