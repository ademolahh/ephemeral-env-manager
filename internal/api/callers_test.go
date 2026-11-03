package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func (s server) getBudget(t *testing.T, key, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/callers/"+id+"/budget", nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func TestGetBudgetShowsGivenAndUsed(t *testing.T) {
	s := newServer(t)
	_, key := s.callerWithRole(t, db.RoleUser)
	created := s.createEnv(t, key)

	rec := s.getBudget(t, key, "me")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var got budgetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode budget: %v", err)
	}
	if got.Currency != "USD" {
		t.Errorf("currency = %q, want USD", got.Currency)
	}
	if want := decimal.NewFromInt(100); !got.CostBudget.Equal(want) {
		t.Errorf("cost budget = %s, want %s", got.CostBudget, want)
	}
	if want := created.HourlyRate.Mul(decimal.NewFromInt(4)); !got.CostUsed.Equal(want) {
		t.Errorf("cost used = %s, want %s", got.CostUsed, want)
	}
	if want := got.CostBudget.Sub(got.CostUsed); !got.CostRemaining.Equal(want) {
		t.Errorf("cost remaining = %s, want %s", got.CostRemaining, want)
	}
	if want := (provider.Resources{CPUCore: 3, RAMGB: 9, DiskGB: 22}); got.ResourcesUsed != want {
		t.Errorf("resources used = %+v, want %+v", got.ResourcesUsed, want)
	}
}

func TestGetBudgetAccess(t *testing.T) {
	s := newServer(t)
	ownerID, ownerKey := s.callerWithRole(t, db.RoleUser)
	otherKey := s.keyWithRole(t, db.RoleUser)
	adminKey := s.keyWithRole(t, db.RoleAdmin)
	for _, tc := range []struct {
		name, key, id string
		want          int
	}{
		{"owner", ownerKey, ownerID.String(), http.StatusOK},
		{"admin", adminKey, ownerID.String(), http.StatusOK},
		{"other caller", otherKey, ownerID.String(), http.StatusNotFound},
		{"unknown caller", adminKey, uuid.NewString(), http.StatusNotFound},
		{"invalid id", ownerKey, "bad-id", http.StatusBadRequest},
		{"missing key", "", "me", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := s.getBudget(t, tc.key, tc.id); rec.Code != tc.want {
				t.Errorf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func (s server) putBudget(t *testing.T, key, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/callers/"+id+"/budget", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func futureRFC3339() string {
	return time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
}

func TestSetBudget(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	_, admin := s.callerWithRole(t, db.RoleAdmin)
	target, _ := s.callerWithRole(t, db.RoleUser)
	if _, err := s.tx.Exec(ctx, `UPDATE callers SET budget_set_at = now() - interval '1 day' WHERE id = $1`, target); err != nil {
		t.Fatalf("backdate budget_set_at: %v", err)
	}
	expires := futureRFC3339()

	rec := s.putBudget(t, admin, target.String(), `{
		"quota": {"cpu_core": 8, "ram_gb": 32, "disk_gb": 100},
		"cost_budget_total": "25.50",
		"budget_expires_at": "`+expires+`"
	}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusNoContent, rec.Body)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body)
	}

	limits, err := db.LockCaller(ctx, s.tx, target)
	if err != nil {
		t.Fatalf("LockCaller() error = %v", err)
	}
	if want := (provider.Resources{CPUCore: 8, RAMGB: 32, DiskGB: 100}); limits.Quota != want {
		t.Errorf("quota = %+v, want %+v", limits.Quota, want)
	}
	if !limits.CostBudgetTotal.Equal(decimal.RequireFromString("25.50")) {
		t.Errorf("cost_budget_total = %s, want 25.50", limits.CostBudgetTotal)
	}
	var storedExpiry, now time.Time
	if err := s.tx.QueryRow(ctx, `SELECT budget_expires_at, now() FROM callers WHERE id = $1`, target).Scan(&storedExpiry, &now); err != nil {
		t.Fatalf("read expiry: %v", err)
	}
	if want, _ := time.Parse(time.RFC3339, expires); !storedExpiry.Equal(want) {
		t.Errorf("budget_expires_at = %v, want %v", storedExpiry, want)
	}
	if !limits.BudgetSetAt.Equal(now) {
		t.Errorf("budget_set_at = %v, want reset to %v", limits.BudgetSetAt, now)
	}
}

func TestSetBudgetForbidden(t *testing.T) {
	s := newServer(t)
	target, user := s.callerWithRole(t, db.RoleUser)
	_, agent := s.callerWithRole(t, db.RoleAgent)
	body := `{"quota": {"cpu_core": 999, "ram_gb": 999, "disk_gb": 999}, "cost_budget_total": "999", "budget_expires_at": "` + futureRFC3339() + `"}`

	for name, key := range map[string]string{"user on own budget": user, "agent": agent} {
		if rec := s.putBudget(t, key, target.String(), body); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, http.StatusForbidden)
		}
	}
	if rec := s.putBudget(t, "", target.String(), body); rec.Code != http.StatusUnauthorized {
		t.Errorf("no api key: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	limits, err := db.LockCaller(context.Background(), s.tx, target)
	if err != nil {
		t.Fatalf("LockCaller() error = %v", err)
	}
	if limits.Quota.CPUCore == 999 {
		t.Error("quota changed by a non-admin")
	}
}

func TestSetBudgetRejected(t *testing.T) {
	s := newServer(t)
	_, admin := s.callerWithRole(t, db.RoleAdmin)
	target, _ := s.callerWithRole(t, db.RoleUser)
	future := futureRFC3339()
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	tests := []struct {
		name string
		id   string
		body string
		code int
		want string
	}{
		{name: "invalid id", id: "nope", body: `{}`, code: http.StatusBadRequest, want: "invalid caller id"},
		{name: "unknown caller", id: uuid.NewString(), body: `{"quota": {}, "cost_budget_total": "1", "budget_expires_at": "` + future + `"}`, code: http.StatusNotFound, want: "caller not found"},
		{name: "malformed json", id: target.String(), body: `{`, code: http.StatusBadRequest, want: "invalid request body"},
		{name: "unknown field", id: target.String(), body: `{"budget": "1"}`, code: http.StatusBadRequest, want: "unknown field"},
		{name: "missing quota", id: target.String(), body: `{"cost_budget_total": "1", "budget_expires_at": "` + future + `"}`, code: http.StatusBadRequest, want: "required"},
		{name: "missing cost budget", id: target.String(), body: `{"quota": {}, "budget_expires_at": "` + future + `"}`, code: http.StatusBadRequest, want: "required"},
		{name: "missing expiry", id: target.String(), body: `{"quota": {}, "cost_budget_total": "1"}`, code: http.StatusBadRequest, want: "required"},
		{name: "negative quota", id: target.String(), body: `{"quota": {"ram_gb": -1}, "cost_budget_total": "1", "budget_expires_at": "` + future + `"}`, code: http.StatusBadRequest, want: "quota must not be negative"},
		{name: "negative budget", id: target.String(), body: `{"quota": {}, "cost_budget_total": "-0.01", "budget_expires_at": "` + future + `"}`, code: http.StatusBadRequest, want: "cost_budget_total must not be negative"},
		{name: "expiry in the past", id: target.String(), body: `{"quota": {}, "cost_budget_total": "1", "budget_expires_at": "` + past + `"}`, code: http.StatusBadRequest, want: "in the future"},
		{name: "expiry not a timestamp", id: target.String(), body: `{"quota": {}, "cost_budget_total": "1", "budget_expires_at": "tomorrow"}`, code: http.StatusBadRequest, want: "invalid request body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := s.putBudget(t, admin, tt.id, tt.body)
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("got %d %s, want %d containing %q", rec.Code, rec.Body, tt.code, tt.want)
			}
		})
	}
}

func TestSetBudgetZeroQuotaBlocksNewEnvironments(t *testing.T) {
	s := newServer(t)
	_, admin := s.callerWithRole(t, db.RoleAdmin)
	target, key := s.callerWithRole(t, db.RoleUser)

	rec := s.putBudget(t, admin, target.String(), `{"quota": {}, "cost_budget_total": "0", "budget_expires_at": "`+futureRFC3339()+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set budget status = %d, want %d; body %s", rec.Code, http.StatusNoContent, rec.Body)
	}

	if rec := s.post(t, key, validBody, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "quota exceeded") {
		t.Errorf("create after zero quota = %d %s, want 403 quota exceeded", rec.Code, rec.Body)
	}
}

func TestGetBudgetShowsProdBugUsage(t *testing.T) {
	s := newServer(t)
	_, key := s.callerWithRole(t, db.RoleUser)
	rec := s.post(t, key, withBody(t, func(m map[string]any) { m["type"] = "PROD_BUG" }), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	created := decodeEnv(t, rec)

	rec = s.getBudget(t, key, "me")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var got budgetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode budget: %v", err)
	}
	if want := created.HourlyRate.Mul(decimal.NewFromInt(4)); !got.ProdBugCostUsed.Equal(want) {
		t.Errorf("prod bug cost used = %s, want %s", got.ProdBugCostUsed, want)
	}
	if !got.CostUsed.IsZero() || !got.CostRemaining.Equal(got.CostBudget) {
		t.Errorf("cost used, remaining = %s, %s, want 0 and the full budget (PROD_BUG is exempt)", got.CostUsed, got.CostRemaining)
	}
}
