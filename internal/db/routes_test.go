package db

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestSetEnvironmentEndpoint(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	id := createTestEnvironment(t, tx, newEnvironment(createTestCaller(t, tx, newCaller())))

	if err := SetEnvironmentEndpoint(ctx, tx, id, "http://env-1.env.localhost:8000"); err != nil {
		t.Fatalf("SetEnvironmentEndpoint() error = %v", err)
	}

	got, err := GetEnvironment(ctx, tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if got.Endpoint == nil || *got.Endpoint != "http://env-1.env.localhost:8000" {
		t.Errorf("endpoint = %v, want http://env-1.env.localhost:8000", got.Endpoint)
	}
	if got.Backend != nil {
		t.Errorf("backend = %v, want nil", *got.Backend)
	}
}

func TestSetEnvironmentEndpointNotFound(t *testing.T) {
	tx := testTx(t)
	err := SetEnvironmentEndpoint(context.Background(), tx, uuid.New(), "http://x")
	if !errors.Is(err, ErrEnvNotFound) {
		t.Errorf("SetEnvironmentEndpoint() error = %v, want %v", err, ErrEnvNotFound)
	}
}

func TestSetEnvironmentBackend(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	id := createTestEnvironment(t, tx, newEnvironment(createTestCaller(t, tx, newCaller())))

	if err := SetEnvironmentBackend(ctx, tx, id, "eem-1-workload:8080"); err != nil {
		t.Fatalf("SetEnvironmentBackend() error = %v", err)
	}

	got, err := GetEnvironment(ctx, tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if got.Backend == nil || *got.Backend != "eem-1-workload:8080" {
		t.Errorf("backend = %v, want eem-1-workload:8080", got.Backend)
	}
	if got.Endpoint != nil {
		t.Errorf("endpoint = %v, want nil", *got.Endpoint)
	}
}

func TestSetEnvironmentBackendNotFound(t *testing.T) {
	tx := testTx(t)
	err := SetEnvironmentBackend(context.Background(), tx, uuid.New(), "x:1")
	if !errors.Is(err, ErrEnvNotFound) {
		t.Errorf("SetEnvironmentBackend() error = %v, want %v", err, ErrEnvNotFound)
	}
}

func TestNewEnvironmentHasNoRoute(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	id := createTestEnvironment(t, tx, newEnvironment(createTestCaller(t, tx, newCaller())))

	got, err := GetEnvironment(ctx, tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if got.Endpoint != nil || got.Backend != nil {
		t.Errorf("endpoint, backend = %v, %v, want nil, nil", got.Endpoint, got.Backend)
	}
}

func TestRouteBackend(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())

	for _, status := range []EnvStatus{EnvProvisioning, EnvRunning, EnvDeleting, EnvDeleted, EnvFailed} {
		t.Run(string(status), func(t *testing.T) {
			id := createTestEnvironment(t, tx, newEnvironment(callerID))
			backend := "eem-" + id.String() + "-workload:8080"
			if err := SetEnvironmentBackend(ctx, tx, id, backend); err != nil {
				t.Fatalf("SetEnvironmentBackend() error = %v", err)
			}
			forceStatus(t, tx, id, status)

			got, err := RouteBackend(ctx, tx, id)
			if status == EnvRunning {
				if err != nil || got != backend {
					t.Errorf("RouteBackend() = %q, %v, want %q", got, err, backend)
				}
				return
			}
			if !errors.Is(err, ErrRouteNotFound) {
				t.Errorf("RouteBackend() = %q, %v, want ErrRouteNotFound", got, err)
			}
		})
	}
}

func TestRouteBackendWithoutBackend(t *testing.T) {
	tx := testTx(t)
	id := createTestEnvironment(t, tx, newEnvironment(createTestCaller(t, tx, newCaller())))
	forceStatus(t, tx, id, EnvRunning)

	if _, err := RouteBackend(context.Background(), tx, id); !errors.Is(err, ErrRouteNotFound) {
		t.Errorf("RouteBackend() error = %v, want ErrRouteNotFound", err)
	}
}

func TestRouteBackendUnknown(t *testing.T) {
	tx := testTx(t)
	if _, err := RouteBackend(context.Background(), tx, uuid.New()); !errors.Is(err, ErrRouteNotFound) {
		t.Errorf("RouteBackend() error = %v, want ErrRouteNotFound", err)
	}
}
