package env

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func testTx(t *testing.T) pgx.Tx {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { tx.Rollback(context.Background()) })

	if _, err := tx.Exec(ctx, "CREATE SCHEMA eem_test; SET LOCAL search_path TO eem_test"); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	sort.Strings(files)
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	return tx
}

type createCall struct {
	id       string
	workload string
	temp     provider.Template
}

type fakeProvider struct {
	err      error
	onCreate func()
	calls    []createCall
}

func (f *fakeProvider) Create(ctx context.Context, id, workload string, temp provider.Template) (string, error) {
	f.calls = append(f.calls, createCall{id: id, workload: workload, temp: temp})
	if f.onCreate != nil {
		f.onCreate()
	}
	if f.err != nil {
		return "", f.err
	}
	return "container-" + id, nil
}

func (f *fakeProvider) Delete(ctx context.Context, id string) error { return nil }

func (f *fakeProvider) Status(ctx context.Context, id string) (provider.Status, error) {
	return "", nil
}

func (f *fakeProvider) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	return "", nil
}

func (f *fakeProvider) List(ctx context.Context) ([]string, error) { return nil, nil }

var dockerPrices = provider.Prices{
	CPUCore: decimal.RequireFromString("0.004048"),
	RAMGB:   decimal.RequireFromString("0.004445"),
	DiskGB:  decimal.RequireFromString("0.00011"),
}

var dashboard = provider.Template{
	Name:            "clear-dashboard",
	Version:         "v1",
	Providers:       []provider.Type{provider.Docker, provider.Cloud},
	DefaultProvider: provider.Docker,
	TTL:             4 * time.Hour,
	Workload:        provider.Workload{Resources: provider.Resources{CPUCore: 1, RAMGB: 4, DiskGB: 10}},
	Services: []provider.Service{
		{Name: "postgres", Image: "postgres:18", Resources: provider.Resources{CPUCore: 1, RAMGB: 4, DiskGB: 10}},
		{Name: "redis", Image: "redis:8", Resources: provider.Resources{CPUCore: 1, RAMGB: 1, DiskGB: 2}},
	},
}

func setup(t *testing.T) (pgx.Tx, uuid.UUID) {
	t.Helper()
	tx := testTx(t)
	ctx := context.Background()
	if err := db.SetHourlyPrices(ctx, tx, provider.Docker, dockerPrices); err != nil {
		t.Fatalf("SetHourlyPrices() error = %v", err)
	}
	callerID, err := db.CreateCaller(ctx, tx, db.NewCaller{
		Role:            db.RoleUser,
		PermittedEnv:    db.EnvRegularTesting,
		Quota:           provider.Resources{CPUCore: 8, RAMGB: 32, DiskGB: 100},
		CostBudget:      decimal.RequireFromString("10"),
		BudgetExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	return tx, callerID
}

func txNow(t *testing.T, tx pgx.Tx) time.Time {
	t.Helper()
	var now time.Time
	if err := tx.QueryRow(context.Background(), `SELECT now()`).Scan(&now); err != nil {
		t.Fatalf("select now: %v", err)
	}
	return now
}

func countEnvironments(t *testing.T, tx pgx.Tx) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM environments`).Scan(&n); err != nil {
		t.Fatalf("count environments: %v", err)
	}
	return n
}

func TestCreate(t *testing.T) {
	tx, callerID := setup(t)
	ctx := context.Background()
	fake := &fakeProvider{}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})

	id, err := m.Create(ctx, CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "ghcr.io/acme/dashboard:abc123",
		Template: dashboard,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if len(fake.calls) != 1 {
		t.Fatalf("provider Create calls = %d, want 1", len(fake.calls))
	}
	call := fake.calls[0]
	if call.id != id.String() {
		t.Errorf("provider id = %q, want %q", call.id, id)
	}
	if call.workload != "ghcr.io/acme/dashboard:abc123" {
		t.Errorf("provider workload = %q, want %q", call.workload, "ghcr.io/acme/dashboard:abc123")
	}
	if call.temp.Name != dashboard.Name || len(call.temp.Services) != len(dashboard.Services) {
		t.Errorf("provider template = %+v, want %+v", call.temp, dashboard)
	}

	env, err := db.GetEnvironment(ctx, tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if env.Status != db.EnvRunning {
		t.Errorf("status = %s, want %s", env.Status, db.EnvRunning)
	}
	if env.Provider != provider.Docker {
		t.Errorf("provider = %s, want %s", env.Provider, provider.Docker)
	}
	if env.Type != db.EnvRegularTesting {
		t.Errorf("type = %s, want %s", env.Type, db.EnvRegularTesting)
	}
	if env.CallerID != callerID {
		t.Errorf("caller_id = %s, want %s", env.CallerID, callerID)
	}
	if want := (provider.Resources{CPUCore: 3, RAMGB: 9, DiskGB: 22}); env.Resources != want {
		t.Errorf("resources = %+v, want %+v", env.Resources, want)
	}
	if want := dashboard.HourlyCost(dockerPrices); !env.HourlyRate.Equal(want) {
		t.Errorf("hourly_rate = %s, want %s", env.HourlyRate, want)
	}
	if env.TemplateName != "clear-dashboard" || env.TemplateVersion != "v1" {
		t.Errorf("template = %s/%s, want clear-dashboard/v1", env.TemplateName, env.TemplateVersion)
	}
	if want := txNow(t, tx).Add(4 * time.Hour); !env.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %v, want %v", env.ExpiresAt, want)
	}
	if env.FailureReason != nil {
		t.Errorf("failure_reason = %q, want nil", *env.FailureReason)
	}
}

func TestCreateExplicitProvider(t *testing.T) {
	tx, callerID := setup(t)
	ctx := context.Background()
	cloudPrices := provider.Prices{CPUCore: decimal.RequireFromString("0.05"), RAMGB: decimal.Zero, DiskGB: decimal.Zero}
	if err := db.SetHourlyPrices(ctx, tx, provider.Cloud, cloudPrices); err != nil {
		t.Fatalf("SetHourlyPrices() error = %v", err)
	}
	docker := &fakeProvider{}
	cloud := &fakeProvider{}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: docker, provider.Cloud: cloud})

	id, err := m.Create(ctx, CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Provider: provider.Cloud,
		Workload: "app:latest",
		Template: dashboard,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if len(docker.calls) != 0 || len(cloud.calls) != 1 {
		t.Errorf("provider calls docker=%d cloud=%d, want 0 and 1", len(docker.calls), len(cloud.calls))
	}

	env, err := db.GetEnvironment(ctx, tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if env.Provider != provider.Cloud {
		t.Errorf("provider = %s, want %s", env.Provider, provider.Cloud)
	}
	if want := dashboard.HourlyCost(cloudPrices); !env.HourlyRate.Equal(want) {
		t.Errorf("hourly_rate = %s, want %s", env.HourlyRate, want)
	}
}

func TestCreateProviderFails(t *testing.T) {
	tx, callerID := setup(t)
	ctx := context.Background()
	fake := &fakeProvider{err: errors.New("pull image app:missing: not found")}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})

	id, err := m.Create(ctx, CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "app:missing",
		Template: dashboard,
	})
	if !errors.Is(err, fake.err) {
		t.Fatalf("Create() error = %v, want provider error", err)
	}
	if id == uuid.Nil {
		t.Fatal("Create() id = nil, want id of failed environment")
	}
	if !strings.HasPrefix(err.Error(), "provision environment "+id.String()+":") {
		t.Errorf("Create() error = %v, want provision environment prefix", err)
	}

	env, err := db.GetEnvironment(ctx, tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if env.Status != db.EnvFailed {
		t.Errorf("status = %s, want %s", env.Status, db.EnvFailed)
	}
	if env.FailureReason == nil || *env.FailureReason != fake.err.Error() {
		t.Errorf("failure_reason = %v, want %q", env.FailureReason, fake.err.Error())
	}
}

func TestCreateProviderFailsAfterCancel(t *testing.T) {
	tx, callerID := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeProvider{err: context.Canceled, onCreate: cancel}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})

	id, err := m.Create(ctx, CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "app:latest",
		Template: dashboard,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create() error = %v, want context.Canceled", err)
	}

	env, err := db.GetEnvironment(context.Background(), tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if env.Status != db.EnvFailed {
		t.Errorf("status = %s, want %s", env.Status, db.EnvFailed)
	}
}

func TestCreateRejected(t *testing.T) {
	noDefault := dashboard
	noDefault.DefaultProvider = ""
	dockerOnly := dashboard
	dockerOnly.Providers = []provider.Type{provider.Docker}

	tests := []struct {
		name      string
		providers map[provider.Type]provider.Provider
		req       func(callerID uuid.UUID) CreateRequest
		want      error
	}{
		{
			name: "no provider",
			req: func(callerID uuid.UUID) CreateRequest {
				return CreateRequest{CallerID: callerID, Type: db.EnvRegularTesting, Workload: "app", Template: noDefault}
			},
			want: ErrNoProvider,
		},
		{
			name: "provider not in template",
			providers: map[provider.Type]provider.Provider{
				provider.Cloud: &fakeProvider{},
			},
			req: func(callerID uuid.UUID) CreateRequest {
				return CreateRequest{CallerID: callerID, Type: db.EnvRegularTesting, Provider: provider.Cloud, Workload: "app", Template: dockerOnly}
			},
			want: ErrProviderNotAllowed,
		},
		{
			name: "provider not configured",
			req: func(callerID uuid.UUID) CreateRequest {
				return CreateRequest{CallerID: callerID, Type: db.EnvRegularTesting, Provider: provider.Cloud, Workload: "app", Template: dashboard}
			},
			want: ErrProviderUnavailable,
		},
		{
			name: "no prices",
			providers: map[provider.Type]provider.Provider{
				provider.Cloud: &fakeProvider{},
			},
			req: func(callerID uuid.UUID) CreateRequest {
				return CreateRequest{CallerID: callerID, Type: db.EnvRegularTesting, Provider: provider.Cloud, Workload: "app", Template: dashboard}
			},
			want: db.ErrPricesNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, callerID := setup(t)
			fake := &fakeProvider{}
			providers := map[provider.Type]provider.Provider{provider.Docker: fake}
			for k, v := range tt.providers {
				providers[k] = v
			}
			m := NewManager(tx, providers)

			id, err := m.Create(context.Background(), tt.req(callerID))
			if !errors.Is(err, tt.want) {
				t.Fatalf("Create() error = %v, want %v", err, tt.want)
			}
			if id != uuid.Nil {
				t.Errorf("Create() id = %s, want nil", id)
			}
			for pt, p := range providers {
				if calls := p.(*fakeProvider).calls; len(calls) != 0 {
					t.Errorf("provider %s Create calls = %d, want 0", pt, len(calls))
				}
			}
			if n := countEnvironments(t, tx); n != 0 {
				t.Errorf("environments = %d, want 0", n)
			}
		})
	}
}

func TestCreateUnknownCaller(t *testing.T) {
	tx, _ := setup(t)
	fake := &fakeProvider{}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})

	id, err := m.Create(context.Background(), CreateRequest{
		CallerID: uuid.New(),
		Type:     db.EnvRegularTesting,
		Workload: "app",
		Template: dashboard,
	})
	if err == nil {
		t.Fatal("Create() error = nil, want error")
	}
	if id != uuid.Nil {
		t.Errorf("Create() id = %s, want nil", id)
	}
	if len(fake.calls) != 0 {
		t.Errorf("provider Create calls = %d, want 0", len(fake.calls))
	}
}
