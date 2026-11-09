package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/dbtest"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type fakeProvider struct {
	calls    []provider.Template
	execOut  string
	execErr  error
	execCmds [][]string
}

func (f *fakeProvider) Create(ctx context.Context, id, workload string, temp provider.Template) (string, error) {
	f.calls = append(f.calls, temp)
	return "container-" + id, nil
}

func (f *fakeProvider) Delete(ctx context.Context, id string) error { return nil }

func (f *fakeProvider) Status(ctx context.Context, id string) (provider.Status, error) {
	return provider.StatusRunning, nil
}

func (f *fakeProvider) Address(ctx context.Context, id string) (string, error) { return "", nil }

func (f *fakeProvider) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	f.execCmds = append(f.execCmds, cmd)
	return f.execOut, f.execErr
}

func (f *fakeProvider) List(ctx context.Context) ([]string, error) { return nil, nil }

type server struct {
	tx      pgx.Tx
	m       *env.Manager
	fake    *fakeProvider
	handler http.Handler
}

func newServer(t *testing.T) server {
	t.Helper()
	tx := dbtest.Tx(t)
	ctx := context.Background()
	if err := db.SetHourlyPrices(ctx, tx, provider.Docker, provider.Prices{
		CPUCore: 4048,
		RAMGB:   4445,
		DiskGB:  110,
	}); err != nil {
		t.Fatalf("SetHourlyPrices() error = %v", err)
	}
	if err := db.SetCapacityPool(ctx, tx, provider.Docker, provider.Resources{CPUCore: 100, RAMGB: 100, DiskGB: 1000}); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}
	fake := &fakeProvider{}
	m := env.NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})
	return server{tx: tx, m: m, fake: fake, handler: Routes(NewHandler(tx, m, testAdminSecret))}
}

func (s server) apiKey(t *testing.T, permitted db.EnvType, quota provider.Resources) string {
	t.Helper()
	ctx := context.Background()
	callerID, err := db.CreateCaller(ctx, s.tx, db.NewCaller{
		Role:            db.RoleUser,
		PermittedEnv:    permitted,
		Quota:           quota,
		CostBudgetTotal: 10000,
		BudgetExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	key, hash := auth.GenerateAPIKey()
	if _, err := db.CreateAPIKey(ctx, s.tx, callerID, hash, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}
	return key
}

func (s server) post(t *testing.T, key, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/envs", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	s.settle(t)
	return rec
}

func (s server) countEnvironments(t *testing.T) int {
	t.Helper()
	var n int
	if err := s.tx.QueryRow(context.Background(), `SELECT count(*) FROM environments`).Scan(&n); err != nil {
		t.Fatalf("count environments: %v", err)
	}
	return n
}

const validBody = `{
	"type": "REGULAR_TESTING",
	"workload": "ghcr.io/acme/dashboard:abc123",
	"template": {
		"name": "clear-dashboard",
		"version": "v1",
		"providers": ["docker"],
		"default_provider": "docker",
		"ttl": "4h",
		"workload": {
			"ports": [8080],
			"resources": {"cpu_core": 1, "ram_gb": 4, "disk_gb": 10},
			"env": {"APP_ENV": "test"}
		},
		"services": [
			{"name": "postgres", "image": "postgres:18", "resources": {"cpu_core": 1, "ram_gb": 4, "disk_gb": 10}, "healthcheck": "pg_isready"},
			{"name": "redis", "image": "redis:8", "resources": {"cpu_core": 1, "ram_gb": 1, "disk_gb": 2}}
		]
	}
}`

func withBody(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(validBody), &m); err != nil {
		t.Fatalf("unmarshal valid body: %v", err)
	}
	mutate(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return string(b)
}

func tpl(m map[string]any) map[string]any {
	return m["template"].(map[string]any)
}

func decodeEnv(t *testing.T, rec *httptest.ResponseRecorder) db.Environment {
	t.Helper()
	var e db.Environment
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode response %s: %v", rec.Body, err)
	}
	return e
}

func TestCreateEnv(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})

	rec := s.post(t, key, validBody, nil)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	got := decodeEnv(t, rec)
	if rec.Header().Get("Location") != "/envs/"+got.ID.String() {
		t.Errorf("Location = %q, want /envs/%s", rec.Header().Get("Location"), got.ID)
	}
	if got.Status != db.EnvProvisioning {
		t.Errorf("status = %s, want %s", got.Status, db.EnvProvisioning)
	}
	if got.Type != db.EnvRegularTesting || got.Provider != provider.Docker {
		t.Errorf("type, provider = %s, %s, want REGULAR_TESTING, docker", got.Type, got.Provider)
	}
	if want := (provider.Resources{CPUCore: 3, RAMGB: 9, DiskGB: 22}); got.Resources != want {
		t.Errorf("resources = %+v, want %+v", got.Resources, want)
	}
	if want := int64(54569); got.HourlyRate != want {
		t.Errorf("hourly_rate = %d, want %d", got.HourlyRate, want)
	}
	if got.TemplateName != "clear-dashboard" || got.TemplateVersion != "v1" {
		t.Errorf("template = %s/%s, want clear-dashboard/v1", got.TemplateName, got.TemplateVersion)
	}
	if d := got.ExpiresAt.Sub(got.CreatedAt); d != 4*time.Hour {
		t.Errorf("expires_at - created_at = %s, want 4h", d)
	}

	if len(s.fake.calls) != 1 {
		t.Fatalf("provider Create calls = %d, want 1", len(s.fake.calls))
	}
	sent := s.fake.calls[0]
	if sent.TTL != 4*time.Hour || len(sent.Services) != 2 || sent.Services[0].Healthcheck != "pg_isready" || sent.Workload.Env["APP_ENV"] != "test" {
		t.Errorf("provider template = %+v, want decoded request template", sent)
	}

	stored, err := db.GetEnvironment(context.Background(), s.tx, got.ID)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if stored.Status != db.EnvRunning {
		t.Errorf("stored status = %s, want %s", stored.Status, db.EnvRunning)
	}
}

func TestCreateEnvIdempotencyKey(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})
	headers := map[string]string{"Idempotency-Key": "deploy-42"}

	first := s.post(t, key, validBody, headers)
	second := s.post(t, key, validBody, headers)

	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("status = %d, %d, want 202, 202; bodies %s %s", first.Code, second.Code, first.Body, second.Body)
	}
	a, b := decodeEnv(t, first), decodeEnv(t, second)
	if a.ID != b.ID {
		t.Errorf("retry id = %s, want %s", b.ID, a.ID)
	}
	if b.Status != db.EnvRunning {
		t.Errorf("retry status = %s, want current status %s", b.Status, db.EnvRunning)
	}
	if len(s.fake.calls) != 1 {
		t.Errorf("provider Create calls = %d, want 1", len(s.fake.calls))
	}
	if n := s.countEnvironments(t); n != 1 {
		t.Errorf("environments = %d, want 1", n)
	}

	third := s.post(t, key, validBody, map[string]string{"Idempotency-Key": "deploy-43"})
	if third.Code != http.StatusAccepted || decodeEnv(t, third).ID == a.ID {
		t.Errorf("new key: status %d id %s, want 202 with new environment", third.Code, decodeEnv(t, third).ID)
	}
}

func TestCreateEnvUnauthorized(t *testing.T) {
	s := newServer(t)
	valid := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})

	revokedKey, hash := auth.GenerateAPIKey()
	callerID, err := db.CreateCaller(context.Background(), s.tx, db.NewCaller{
		Role:            db.RoleUser,
		PermittedEnv:    db.EnvRegularTesting,
		Quota:           provider.Resources{CPUCore: 10},
		CostBudgetTotal: 10000,
		BudgetExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	keyID, err := db.CreateAPIKey(context.Background(), s.tx, callerID, hash, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}
	if err := db.RevokeAPIKey(context.Background(), s.tx, keyID); err != nil {
		t.Fatalf("RevokeAPIKey() error = %v", err)
	}

	tests := []struct {
		name   string
		header string
	}{
		{name: "missing", header: ""},
		{name: "wrong scheme", header: "Basic " + valid},
		{name: "empty bearer", header: "Bearer "},
		{name: "unknown key", header: "Bearer eem_unknown"},
		{name: "revoked key", header: "Bearer " + revokedKey},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/envs", strings.NewReader(validBody))
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			s.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			if rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
	if n := s.countEnvironments(t); n != 0 {
		t.Errorf("environments = %d, want 0", n)
	}
}

func TestCreateEnvBadRequest(t *testing.T) {
	tests := []struct {
		name    string
		body    func(t *testing.T) string
		headers map[string]string
		want    string
	}{
		{name: "not json", body: func(*testing.T) string { return "{" }, want: "invalid request body"},
		{name: "unknown field", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) { m["image"] = "x" })
		}, want: "unknown field"},
		{name: "missing workload", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) { delete(m, "workload") })
		}, want: "workload is required"},
		{name: "missing template name", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) { delete(tpl(m), "name") })
		}, want: "name and version"},
		{name: "missing ttl", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) { delete(tpl(m), "ttl") })
		}, want: "ttl is required"},
		{name: "unparseable ttl", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) { tpl(m)["ttl"] = "four hours" })
		}, want: "positive duration"},
		{name: "negative ttl", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) { tpl(m)["ttl"] = "-1h" })
		}, want: "positive duration"},
		{name: "negative workload resources", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) {
				tpl(m)["workload"].(map[string]any)["resources"] = map[string]any{"cpu_core": -1, "ram_gb": 1, "disk_gb": 1}
			})
		}, want: "must be at least 1"},
		{name: "zero workload cpu", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) {
				tpl(m)["workload"].(map[string]any)["resources"] = map[string]any{"cpu_core": 0, "ram_gb": 1, "disk_gb": 1}
			})
		}, want: "workload resources must be at least 1"},
		{name: "workload resources omitted", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) { delete(tpl(m)["workload"].(map[string]any), "resources") })
		}, want: "workload resources must be at least 1"},
		{name: "zero service ram", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) {
				tpl(m)["services"].([]any)[0].(map[string]any)["resources"] = map[string]any{"cpu_core": 1, "ram_gb": 0, "disk_gb": 1}
			})
		}, want: "service postgres resources must be at least 1"},
		{name: "zero service disk", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) {
				tpl(m)["services"].([]any)[1].(map[string]any)["resources"] = map[string]any{"cpu_core": 1, "ram_gb": 1, "disk_gb": 0}
			})
		}, want: "service redis resources must be at least 1"},
		{name: "port out of range", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) { tpl(m)["workload"].(map[string]any)["ports"] = []int{70000} })
		}, want: "out of range"},
		{name: "invalid service name", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) {
				tpl(m)["services"].([]any)[0].(map[string]any)["name"] = "Postgres_DB"
			})
		}, want: "lowercase"},
		{name: "duplicate service", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) {
				tpl(m)["services"].([]any)[1].(map[string]any)["name"] = "postgres"
			})
		}, want: "reserved or duplicated"},
		{name: "service named workload", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) {
				tpl(m)["services"].([]any)[0].(map[string]any)["name"] = "workload"
			})
		}, want: "reserved or duplicated"},
		{name: "service without image", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) {
				delete(tpl(m)["services"].([]any)[0].(map[string]any), "image")
			})
		}, want: "image is required"},
		{name: "negative service resources", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) {
				tpl(m)["services"].([]any)[1].(map[string]any)["resources"] = map[string]any{"cpu_core": 1, "ram_gb": -4, "disk_gb": 1}
			})
		}, want: "must be at least 1"},
		{name: "invalid type", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) { m["type"] = "STAGING" })
		}, want: "invalid environment type"},
		{name: "provider not allowed", body: func(t *testing.T) string {
			return withBody(t, func(m map[string]any) { m["provider"] = "cloud" })
		}, want: "not allowed"},
		{name: "idempotency key too long", body: func(*testing.T) string { return validBody },
			headers: map[string]string{"Idempotency-Key": strings.Repeat("k", 256)}, want: "Idempotency-Key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(t)
			key := s.apiKey(t, db.EnvProdBug, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})

			rec := s.post(t, key, tt.body(t), tt.headers)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusBadRequest, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("body = %s, want error containing %q", rec.Body, tt.want)
			}
			if n := s.countEnvironments(t); n != 0 {
				t.Errorf("environments = %d, want 0", n)
			}
		})
	}
}

func TestCreateEnvBodyTooLarge(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10})
	body := withBody(t, func(m map[string]any) {
		tpl(m)["workload"].(map[string]any)["env"] = map[string]any{"BIG": strings.Repeat("x", maxBodyBytes)}
	})

	if rec := s.post(t, key, body, nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestCreateEnvRejected(t *testing.T) {
	tests := []struct {
		name      string
		permitted db.EnvType
		quota     provider.Resources
		body      func(t *testing.T) string
		status    int
		want      string
	}{
		{
			name:      "type not permitted",
			permitted: db.EnvRegularTesting,
			quota:     provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50},
			body: func(t *testing.T) string {
				return withBody(t, func(m map[string]any) { m["type"] = "PROD_BUG" })
			},
			status: http.StatusForbidden,
			want:   "not permitted",
		},
		{
			name:      "quota exceeded",
			permitted: db.EnvRegularTesting,
			quota:     provider.Resources{CPUCore: 2, RAMGB: 20, DiskGB: 50},
			body:      func(*testing.T) string { return validBody },
			status:    http.StatusForbidden,
			want:      "quota exceeded",
		},
		{
			name:      "capacity exhausted",
			permitted: db.EnvRegularTesting,
			quota:     provider.Resources{CPUCore: 1000, RAMGB: 1000, DiskGB: 1000},
			body: func(t *testing.T) string {
				return withBody(t, func(m map[string]any) {
					tpl(m)["workload"].(map[string]any)["resources"] = map[string]any{"cpu_core": 95, "ram_gb": 1, "disk_gb": 1}
				})
			},
			status: http.StatusServiceUnavailable,
			want:   "capacity pool exhausted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(t)
			key := s.apiKey(t, tt.permitted, tt.quota)

			rec := s.post(t, key, tt.body(t), nil)

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.status, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("body = %s, want error containing %q", rec.Body, tt.want)
			}
			if len(s.fake.calls) != 0 {
				t.Errorf("provider Create calls = %d, want 0", len(s.fake.calls))
			}
		})
	}
}

func TestCreateEnvInternalErrorHidesDetails(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})
	if _, err := s.tx.Exec(context.Background(), `ALTER TABLE environments RENAME COLUMN template_name TO tpl_name`); err != nil {
		t.Fatalf("break schema: %v", err)
	}

	rec := s.post(t, key, validBody, nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusInternalServerError, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "template_name") {
		t.Errorf("body = %s, leaks database error", rec.Body)
	}
}

func (s server) settle(t *testing.T) {
	t.Helper()
	s.m.WaitReady()
}
