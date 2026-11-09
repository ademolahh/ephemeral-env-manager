package env

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
	"github.com/ademolahh/ephemeral-env-manager/internal/proxy"
)

var testProxy = &proxy.Proxy{Domain: "env.localhost", PublicPort: 8000, Container: "eem-proxy"}

func createRouted(t *testing.T, tx pgx.Tx, callerID uuid.UUID, m *Manager) db.Environment {
	t.Helper()
	created, err := m.Create(context.Background(), CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "ghcr.io/acme/dashboard:abc123",
		Template: dashboard,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	m.WaitReady()

	e, err := db.GetEnvironment(context.Background(), tx, created.ID)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	return e
}

func withPorts() provider.Template {
	tpl := dashboard
	tpl.Workload.Ports = []int{8080}
	return tpl
}

func TestCreateReturnsEndpoint(t *testing.T) {
	tx, callerID := setup(t)
	fake := &fakeProvider{address: "eem-x-workload:8080"}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake}, WithProxy(testProxy))

	created, err := m.Create(context.Background(), CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "ghcr.io/acme/dashboard:abc123",
		Template: withPorts(),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	m.WaitReady()

	want := testProxy.URL(created.ID)
	if created.Status != db.EnvProvisioning {
		t.Errorf("status = %s, want %s", created.Status, db.EnvProvisioning)
	}
	if created.Endpoint == nil || *created.Endpoint != want {
		t.Errorf("endpoint = %v, want %s", created.Endpoint, want)
	}

	e, err := db.GetEnvironment(context.Background(), tx, created.ID)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if e.Endpoint == nil || *e.Endpoint != want {
		t.Errorf("endpoint after provisioning = %v, want %s", e.Endpoint, want)
	}
	if e.Status != db.EnvRunning {
		t.Errorf("status after provisioning = %s, want %s", e.Status, db.EnvRunning)
	}
	if e.Backend == nil || *e.Backend != "eem-x-workload:8080" {
		t.Errorf("backend = %v, want eem-x-workload:8080", e.Backend)
	}

	backend, err := db.RouteBackend(context.Background(), tx, e.ID)
	if err != nil || backend != "eem-x-workload:8080" {
		t.Errorf("RouteBackend() = %q, %v, want eem-x-workload:8080", backend, err)
	}
}

func TestCreateFailedKeepsEndpointWithoutRoute(t *testing.T) {
	tx, callerID := setup(t)
	fake := &fakeProvider{address: "eem-x-workload:8080", err: errors.New("pull failed")}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake}, WithProxy(testProxy))

	created, err := m.Create(context.Background(), CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "ghcr.io/acme/dashboard:abc123",
		Template: withPorts(),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	m.WaitReady()

	if _, err := db.RouteBackend(context.Background(), tx, created.ID); !errors.Is(err, db.ErrRouteNotFound) {
		t.Errorf("RouteBackend() error = %v, want ErrRouteNotFound", err)
	}

	e, err := db.GetEnvironment(context.Background(), tx, created.ID)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if e.Status != db.EnvFailed || e.Backend != nil {
		t.Errorf("status, backend = %s, %v, want %s, nil", e.Status, e.Backend, db.EnvFailed)
	}
}

func TestCreateWithoutProxySetsNoRoute(t *testing.T) {
	tx, callerID := setup(t)
	fake := &fakeProvider{address: "eem-x-workload:8080"}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})

	e := createRouted(t, tx, callerID, m)

	if e.Status != db.EnvRunning {
		t.Errorf("status = %s, want %s", e.Status, db.EnvRunning)
	}
	if e.Endpoint != nil || e.Backend != nil {
		t.Errorf("endpoint, backend = %v, %v, want nil, nil", e.Endpoint, e.Backend)
	}
}

func TestCreateWorkloadWithoutPortsSetsNoRoute(t *testing.T) {
	tx, callerID := setup(t)
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: &fakeProvider{}}, WithProxy(testProxy))

	e := createRouted(t, tx, callerID, m)

	if e.Status != db.EnvRunning {
		t.Errorf("status = %s, want %s", e.Status, db.EnvRunning)
	}
	if e.Endpoint != nil || e.Backend != nil {
		t.Errorf("endpoint, backend = %v, %v, want nil, nil", e.Endpoint, e.Backend)
	}
}

func TestCreateAddressErrorStillRuns(t *testing.T) {
	tx, callerID := setup(t)
	fake := &fakeProvider{addressErr: errors.New("inspect failed")}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake}, WithProxy(testProxy))

	e := createRouted(t, tx, callerID, m)

	if e.Status != db.EnvRunning {
		t.Errorf("status = %s, want %s", e.Status, db.EnvRunning)
	}
	if e.Endpoint != nil || e.Backend != nil {
		t.Errorf("endpoint, backend = %v, %v, want nil, nil", e.Endpoint, e.Backend)
	}
}

func TestCreateFailedSetsNoRoute(t *testing.T) {
	tx, callerID := setup(t)
	fake := &fakeProvider{address: "eem-x-workload:8080", err: errors.New("pull failed")}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake}, WithProxy(testProxy))

	e := createRouted(t, tx, callerID, m)

	if e.Status != db.EnvFailed {
		t.Errorf("status = %s, want %s", e.Status, db.EnvFailed)
	}
	if e.Endpoint != nil || e.Backend != nil {
		t.Errorf("endpoint, backend = %v, %v, want nil, nil", e.Endpoint, e.Backend)
	}
}

func TestResolveStuckSetsRoute(t *testing.T) {
	tx, callerID := setup(t)
	ctx := context.Background()
	fake := &fakeProvider{address: "eem-x-workload:8080", status: provider.StatusRunning}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake}, WithProxy(testProxy))

	id, err := db.CreateEnvironment(ctx, tx, db.NewEnvironment{
		CallerID:        callerID,
		Type:            db.EnvRegularTesting,
		Provider:        provider.Docker,
		Resources:       dashboard.TotalResources(),
		HourlyRate:      dashboard.HourlyCost(dockerPrices),
		TTL:             dashboard.TTL,
		TemplateName:    dashboard.Name,
		TemplateVersion: dashboard.Version,
	})
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	stuck, err := db.GetEnvironment(ctx, tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}

	status, err := m.ResolveStuck(ctx, stuck)
	if err != nil {
		t.Fatalf("ResolveStuck() error = %v", err)
	}
	if status != db.EnvRunning {
		t.Fatalf("ResolveStuck() = %s, want %s", status, db.EnvRunning)
	}

	e, err := db.GetEnvironment(ctx, tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if e.Backend == nil || *e.Backend != "eem-x-workload:8080" {
		t.Errorf("backend = %v, want eem-x-workload:8080", e.Backend)
	}
}
