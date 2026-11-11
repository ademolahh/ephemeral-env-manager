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
	if got.Currency != "USD cents" {
		t.Errorf("currency = %q, want USD cents", got.Currency)
	}
	if want := int64(10000); got.CostBudget != want {
		t.Errorf("cost budget = %d, want %d", got.CostBudget, want)
	}
	if want := (created.HourlyRate*4 + 9999) / 10000; got.CostUsed != want {
		t.Errorf("cost used = %d, want %d", got.CostUsed, want)
	}
	if want := got.CostBudget - got.CostUsed; got.CostRemaining != want {
		t.Errorf("cost remaining = %d, want %d", got.CostRemaining, want)
	}
	if want := (provider.Resources{CPUCore: 3, RAMGB: 9, DiskGB: 22}); got.ResourcesUsed != want {
		t.Errorf("resources used = %+v, want %+v", got.ResourcesUsed, want)
	}
}

func TestGetBudgetAccess(t *testing.T) {
	s := newServer(t)
	ownerID, ownerKey := s.callerWithRole(t, db.RoleUser)
	otherKey := s.keyWithRole(t, db.RoleUser)
	adminKey := s.adminToken(t)
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
	admin := s.adminToken(t)
	target, _ := s.callerWithRole(t, db.RoleUser)
	if _, err := s.tx.Exec(ctx, `UPDATE callers SET budget_set_at = now() - interval '1 day' WHERE id = $1`, target); err != nil {
		t.Fatalf("backdate budget_set_at: %v", err)
	}
	expires := futureRFC3339()

	rec := s.putBudget(t, admin, target.String(), `{
		"quota": {"cpu_core": 8, "ram_gb": 32, "disk_gb": 100},
		"cost_budget_total": 2550,
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
	if limits.CostBudgetTotal != 2550 {
		t.Errorf("cost_budget_total = %d, want 2550", limits.CostBudgetTotal)
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
	body := `{"quota": {"cpu_core": 999, "ram_gb": 999, "disk_gb": 999}, "cost_budget_total": 99900, "budget_expires_at": "` + futureRFC3339() + `"}`

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
	admin := s.adminToken(t)
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
		{name: "unknown caller", id: uuid.NewString(), body: `{"quota": {}, "cost_budget_total": 100, "budget_expires_at": "` + future + `"}`, code: http.StatusNotFound, want: "caller not found"},
		{name: "malformed json", id: target.String(), body: `{`, code: http.StatusBadRequest, want: "invalid request body"},
		{name: "unknown field", id: target.String(), body: `{"budget": 100}`, code: http.StatusBadRequest, want: "unknown field"},
		{name: "missing quota", id: target.String(), body: `{"cost_budget_total": 100, "budget_expires_at": "` + future + `"}`, code: http.StatusBadRequest, want: "required"},
		{name: "missing cost budget", id: target.String(), body: `{"quota": {}, "budget_expires_at": "` + future + `"}`, code: http.StatusBadRequest, want: "required"},
		{name: "missing expiry", id: target.String(), body: `{"quota": {}, "cost_budget_total": 100}`, code: http.StatusBadRequest, want: "required"},
		{name: "negative quota", id: target.String(), body: `{"quota": {"ram_gb": -1}, "cost_budget_total": 100, "budget_expires_at": "` + future + `"}`, code: http.StatusBadRequest, want: "quota must not be negative"},
		{name: "negative budget", id: target.String(), body: `{"quota": {}, "cost_budget_total": -1, "budget_expires_at": "` + future + `"}`, code: http.StatusBadRequest, want: "cost_budget_total must not be negative"},
		{name: "expiry in the past", id: target.String(), body: `{"quota": {}, "cost_budget_total": 100, "budget_expires_at": "` + past + `"}`, code: http.StatusBadRequest, want: "in the future"},
		{name: "expiry not a timestamp", id: target.String(), body: `{"quota": {}, "cost_budget_total": 100, "budget_expires_at": "tomorrow"}`, code: http.StatusBadRequest, want: "invalid request body"},
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
	admin := s.adminToken(t)
	target, key := s.callerWithRole(t, db.RoleUser)

	rec := s.putBudget(t, admin, target.String(), `{"quota": {}, "cost_budget_total": 0, "budget_expires_at": "`+futureRFC3339()+`"}`)
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
	if want := (created.HourlyRate*4 + 9999) / 10000; got.ProdBugCostUsed != want {
		t.Errorf("prod bug cost used = %d, want %d", got.ProdBugCostUsed, want)
	}
	if got.CostUsed != 0 || got.CostRemaining != got.CostBudget {
		t.Errorf("cost used, remaining = %d, %d, want 0 and the full budget (PROD_BUG is exempt)", got.CostUsed, got.CostRemaining)
	}
}

func (s server) postCaller(t *testing.T, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/callers", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func (s server) countCallers(t *testing.T) int {
	t.Helper()
	var n int
	if err := s.tx.QueryRow(context.Background(), `SELECT count(*) FROM callers`).Scan(&n); err != nil {
		t.Fatalf("count callers: %v", err)
	}
	return n
}

func decodeCaller(t *testing.T, rec *httptest.ResponseRecorder) callerResponse {
	t.Helper()
	var got callerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode caller: %v", err)
	}
	return got
}

func TestCreateCaller(t *testing.T) {
	s := newServer(t)

	rec := s.postCaller(t, s.adminToken(t), `{"github_login": "octocat", "github_id": "583231"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusCreated, rec.Body)
	}
	got := decodeCaller(t, rec)
	if want := "/callers/" + got.ID.String() + "/budget"; rec.Header().Get("Location") != want {
		t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), want)
	}
	if got.GithubLogin == nil || *got.GithubLogin != "octocat" || got.GithubID == nil || *got.GithubID != "583231" {
		t.Errorf("github = %v, %v, want octocat, 583231", got.GithubLogin, got.GithubID)
	}

	var login, githubID *string
	if err := s.tx.QueryRow(context.Background(), `SELECT github_login, github_id FROM callers WHERE id = $1`, got.ID).Scan(&login, &githubID); err != nil {
		t.Fatalf("read caller: %v", err)
	}
	if login == nil || *login != "octocat" || githubID == nil || *githubID != "583231" {
		t.Errorf("stored github = %v, %v, want octocat, 583231", login, githubID)
	}
}

func TestCreateCallerMinimalPrivilege(t *testing.T) {
	s := newServer(t)
	before := time.Now()

	rec := s.postCaller(t, s.adminToken(t), `{}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusCreated, rec.Body)
	}
	got := decodeCaller(t, rec)
	if got.PermittedEnv != db.EnvRegularTesting {
		t.Errorf("returned permitted_env = %s, want REGULAR_TESTING", got.PermittedEnv)
	}
	if got.GithubLogin != nil || got.GithubID != nil {
		t.Errorf("github = %v, %v, want null", got.GithubLogin, got.GithubID)
	}

	limits, err := db.LockCaller(context.Background(), s.tx, got.ID)
	if err != nil {
		t.Fatalf("LockCaller() error = %v", err)
	}
	if limits.PermittedEnv != db.EnvRegularTesting {
		t.Errorf("stored permitted_env = %s, want REGULAR_TESTING", limits.PermittedEnv)
	}
	if limits.Quota != defaultQuota || limits.CostBudgetTotal != defaultCostBudget {
		t.Errorf("stored quota, budget = %+v, %d, want %+v, %d", limits.Quota, limits.CostBudgetTotal, defaultQuota, defaultCostBudget)
	}
	wantExpiry := before.Add(defaultBudgetPeriod)
	if got.BudgetExpiresAt.Before(wantExpiry.Add(-time.Minute)) || got.BudgetExpiresAt.After(wantExpiry.Add(time.Minute)) {
		t.Errorf("budget_expires_at = %v, want about %v", got.BudgetExpiresAt, wantExpiry)
	}
}

func TestCreateCallerHasNoAPIKey(t *testing.T) {
	s := newServer(t)
	rec := s.postCaller(t, s.adminToken(t), `{}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusCreated, rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"key"`) {
		t.Errorf("response %s contains an api key", rec.Body)
	}

	var keys int
	if err := s.tx.QueryRow(context.Background(), `SELECT count(*) FROM api_keys WHERE caller_id = $1`, decodeCaller(t, rec).ID).Scan(&keys); err != nil {
		t.Fatalf("count api keys: %v", err)
	}
	if keys != 0 {
		t.Errorf("api keys = %d, want 0", keys)
	}
}

func TestCreateCallerRejected(t *testing.T) {
	s := newServer(t)
	admin := s.adminToken(t)
	if rec := s.postCaller(t, admin, `{"github_id": "583231"}`); rec.Code != http.StatusCreated {
		t.Fatalf("seed caller status = %d, want 201; body %s", rec.Code, rec.Body)
	}
	before := s.countCallers(t)

	tests := []struct {
		name string
		body string
		code int
		want string
	}{
		{name: "malformed json", body: `{`, code: http.StatusBadRequest, want: "invalid request body"},
		{name: "role", body: `{"role": "admin"}`, code: http.StatusBadRequest, want: "unknown field"},
		{name: "permitted env", body: `{"permitted_env": "PROD_BUG"}`, code: http.StatusBadRequest, want: "unknown field"},
		{name: "quota", body: `{"quota": {"cpu_core": 100}}`, code: http.StatusBadRequest, want: "unknown field"},
		{name: "duplicate github id", body: `{"github_id": "583231"}`, code: http.StatusConflict, want: "github id already linked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := s.postCaller(t, admin, tt.body)
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("got %d %s, want %d containing %q", rec.Code, rec.Body, tt.code, tt.want)
			}
		})
	}

	if got := s.countCallers(t); got != before {
		t.Errorf("callers = %d, want unchanged %d", got, before)
	}
}

func TestCreateCallerRequiresAdmin(t *testing.T) {
	s := newServer(t)
	before := s.countCallers(t)
	for name, tc := range map[string]struct {
		key  string
		want int
	}{
		"no credentials": {"", http.StatusUnauthorized},
		"user key":       {s.keyWithRole(t, db.RoleUser), http.StatusForbidden},
		"agent key":      {s.keyWithRole(t, db.RoleAgent), http.StatusForbidden},
	} {
		if rec := s.postCaller(t, tc.key, `{"github_id": "1"}`); rec.Code != tc.want {
			t.Errorf("%s: POST /callers = %d, want %d", name, rec.Code, tc.want)
		}
	}
	if got := s.countCallers(t); got != before+2 {
		t.Errorf("callers = %d, want %d (only the two key owners)", got, before+2)
	}
}
