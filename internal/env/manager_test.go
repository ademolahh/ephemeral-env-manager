package env

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/dbtest"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func testTx(t *testing.T) pgx.Tx {
	t.Helper()
	return dbtest.Tx(t)
}

type createCall struct {
	id       string
	workload string
	temp     provider.Template
	ctxErr   error
}

type fakeProvider struct {
	mu          sync.Mutex
	err         error
	deleteErr   error
	status      provider.Status
	statusErr   error
	statusCalls int
	onCreate    func()
	calls       []createCall
	deleted     []string
}

func (f *fakeProvider) Create(ctx context.Context, id, workload string, temp provider.Template) (string, error) {
	if f.onCreate != nil {
		f.onCreate()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, createCall{id: id, workload: workload, temp: temp, ctxErr: ctx.Err()})
	if f.err != nil {
		return "", f.err
	}
	return "container-" + id, nil
}

func (f *fakeProvider) Delete(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return f.deleteErr
}

func (f *fakeProvider) Status(ctx context.Context, id string) (provider.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	return f.status, f.statusErr
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
	if err := db.SetCapacityPool(ctx, tx, provider.Docker, provider.Resources{CPUCore: 100, RAMGB: 100, DiskGB: 1000}); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
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

func countEnvironments(t *testing.T, q db.DBTX) int {
	t.Helper()
	var n int
	if err := q.QueryRow(context.Background(), `SELECT count(*) FROM environments`).Scan(&n); err != nil {
		t.Fatalf("count environments: %v", err)
	}
	return n
}

func TestCreate(t *testing.T) {
	tx, callerID := setup(t)
	ctx := context.Background()
	fake := &fakeProvider{}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})

	created, err := m.Create(ctx, CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "ghcr.io/acme/dashboard:abc123",
		Template: dashboard,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Status != db.EnvProvisioning {
		t.Errorf("Create() status = %s, want %s", created.Status, db.EnvProvisioning)
	}
	m.Wait()
	id := created.ID

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
	if err := db.SetCapacityPool(ctx, tx, provider.Cloud, provider.Resources{CPUCore: 100, RAMGB: 100, DiskGB: 1000}); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}
	docker := &fakeProvider{}
	cloud := &fakeProvider{}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: docker, provider.Cloud: cloud})

	created, err := m.Create(ctx, CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Provider: provider.Cloud,
		Workload: "app:latest",
		Template: dashboard,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	m.Wait()
	id := created.ID
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

	created, err := m.Create(ctx, CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "app:missing",
		Template: dashboard,
	})
	if err != nil {
		t.Fatalf("Create() error = %v, want nil (provisioning is async)", err)
	}
	m.Wait()

	env, err := db.GetEnvironment(ctx, tx, created.ID)
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

type panicProvider struct{ fakeProvider }

func (p *panicProvider) Create(ctx context.Context, id, workload string, temp provider.Template) (string, error) {
	panic("docker exploded")
}

func TestCreateProviderPanics(t *testing.T) {
	tx, callerID := setup(t)
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: &panicProvider{}})

	created, err := m.Create(context.Background(), CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "app",
		Template: dashboard,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	m.Wait()

	env, err := db.GetEnvironment(context.Background(), tx, created.ID)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if env.Status != db.EnvFailed {
		t.Errorf("status = %s, want %s", env.Status, db.EnvFailed)
	}
	if env.FailureReason == nil || !strings.Contains(*env.FailureReason, "docker exploded") {
		t.Errorf("failure_reason = %v, want panic message", env.FailureReason)
	}
}

func TestCreateReturnsBeforeProvisioning(t *testing.T) {
	tx, callerID := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	fake := &fakeProvider{onCreate: func() { <-release }}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})

	created, err := m.Create(ctx, CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "app:latest",
		Template: dashboard,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Status != db.EnvProvisioning {
		t.Errorf("Create() status = %s, want %s", created.Status, db.EnvProvisioning)
	}
	cancel()
	close(release)
	m.Wait()

	if len(fake.calls) != 1 || fake.calls[0].ctxErr != nil {
		t.Fatalf("provider calls = %+v, want one call with live context", fake.calls)
	}
	env, err := db.GetEnvironment(context.Background(), tx, created.ID)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if env.Status != db.EnvRunning {
		t.Errorf("status = %s, want %s (request cancel must not stop provisioning)", env.Status, db.EnvRunning)
	}
}

func TestCreateIdempotent(t *testing.T) {
	tx, callerID := setup(t)
	ctx := context.Background()
	if err := db.SetCallerBudget(ctx, tx, callerID, provider.Resources{CPUCore: 100, RAMGB: 100, DiskGB: 1000}, decimal.RequireFromString("10"), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("SetCallerBudget() error = %v", err)
	}
	fake := &fakeProvider{}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})
	req := CreateRequest{
		CallerID:       callerID,
		Type:           db.EnvRegularTesting,
		Workload:       "app",
		Template:       dashboard,
		IdempotencyKey: "req-1",
	}

	first, err := m.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	m.Wait()

	second, err := m.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create() retry error = %v", err)
	}
	m.Wait()

	if second.ID != first.ID {
		t.Errorf("retry id = %s, want %s", second.ID, first.ID)
	}
	if second.Status != db.EnvRunning {
		t.Errorf("retry status = %s, want current status %s", second.Status, db.EnvRunning)
	}
	if len(fake.calls) != 1 {
		t.Errorf("provider Create calls = %d, want 1", len(fake.calls))
	}

	req.IdempotencyKey = "req-2"
	third, err := m.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create() new key error = %v", err)
	}
	m.Wait()
	if third.ID == first.ID {
		t.Error("new key returned existing environment")
	}

	req.IdempotencyKey = ""
	fourth, err := m.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create() no key error = %v", err)
	}
	m.Wait()
	fifth, err := m.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create() no key again error = %v", err)
	}
	m.Wait()
	if fourth.ID == fifth.ID {
		t.Error("requests without a key returned the same environment")
	}
	if n := countEnvironments(t, tx); n != 4 {
		t.Errorf("environments = %d, want 4", n)
	}
}

func TestCreateIdempotencyKeyPerCaller(t *testing.T) {
	tx, alice := setup(t)
	ctx := context.Background()
	bob, err := db.CreateCaller(ctx, tx, db.NewCaller{
		Role:            db.RoleUser,
		PermittedEnv:    db.EnvRegularTesting,
		Quota:           provider.Resources{CPUCore: 8, RAMGB: 32, DiskGB: 100},
		CostBudget:      decimal.RequireFromString("10"),
		BudgetExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: &fakeProvider{}})

	a, err := m.Create(ctx, CreateRequest{CallerID: alice, Type: db.EnvRegularTesting, Workload: "app", Template: dashboard, IdempotencyKey: "same"})
	if err != nil {
		t.Fatalf("Create(alice) error = %v", err)
	}
	m.Wait()
	b, err := m.Create(ctx, CreateRequest{CallerID: bob, Type: db.EnvRegularTesting, Workload: "app", Template: dashboard, IdempotencyKey: "same"})
	if err != nil {
		t.Fatalf("Create(bob) error = %v", err)
	}
	m.Wait()

	if a.ID == b.ID {
		t.Error("callers sharing a key got the same environment")
	}
	if b.CallerID != bob {
		t.Errorf("bob's environment caller = %s, want %s", b.CallerID, bob)
	}
}

func TestCreateIdempotentReplaySkipsChecks(t *testing.T) {
	tx, callerID := setup(t)
	ctx := context.Background()
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: &fakeProvider{}})
	req := CreateRequest{CallerID: callerID, Type: db.EnvRegularTesting, Workload: "app", Template: dashboard, IdempotencyKey: "k"}

	first, err := m.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	m.Wait()
	if err := db.SetCallerBudget(ctx, tx, callerID, provider.Resources{}, decimal.Zero, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetCallerBudget() error = %v", err)
	}

	again, err := m.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create() replay error = %v, want existing environment despite zero quota", err)
	}
	if again.ID != first.ID {
		t.Errorf("replay id = %s, want %s", again.ID, first.ID)
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

			e, err := m.Create(context.Background(), tt.req(callerID))
			if !errors.Is(err, tt.want) {
				t.Fatalf("Create() error = %v, want %v", err, tt.want)
			}
			if e.ID != uuid.Nil {
				t.Errorf("Create() id = %s, want nil", e.ID)
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

	e, err := m.Create(context.Background(), CreateRequest{
		CallerID: uuid.New(),
		Type:     db.EnvRegularTesting,
		Workload: "app",
		Template: dashboard,
	})
	if !errors.Is(err, db.ErrCallerNotFound) {
		t.Fatalf("Create() error = %v, want ErrCallerNotFound", err)
	}
	if e.ID != uuid.Nil {
		t.Errorf("Create() id = %s, want nil", e.ID)
	}
	if len(fake.calls) != 0 {
		t.Errorf("provider Create calls = %d, want 0", len(fake.calls))
	}
}
