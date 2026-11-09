package env

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func TestWatchWaitsForRunning(t *testing.T) {
	tx, callerID := setup(t)
	fake := &fakeProvider{ready: []provider.Status{provider.StatusStarting, provider.StatusRunning}}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})

	e, err := m.Create(context.Background(), CreateRequest{CallerID: callerID, Type: db.EnvRegularTesting, Workload: "app", Template: dashboard})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	m.WaitReady()

	if got := status(t, tx, e.ID).Status; got != db.EnvRunning {
		t.Errorf("status = %s, want RUNNING", got)
	}
	if fake.readyCalls != 2 {
		t.Errorf("readiness checks = %d, want 2", fake.readyCalls)
	}
}

func TestWatchFailsNotReady(t *testing.T) {
	for _, s := range []provider.Status{provider.StatusUnhealthy, provider.StatusStopped} {
		t.Run(string(s), func(t *testing.T) {
			tx, callerID := setup(t)
			fake := &fakeProvider{ready: []provider.Status{s}}
			m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})

			e := createRunning(t, m, tx, callerID)

			if e.Status != db.EnvFailed {
				t.Fatalf("status = %s, want FAILED", e.Status)
			}
			if e.FailureReason == nil || !strings.Contains(*e.FailureReason, "instance "+string(s)) {
				t.Errorf("failure reason = %v, want instance %s", e.FailureReason, s)
			}
			if !slices.Contains(fake.deleted, e.ID.String()) {
				t.Errorf("deleted = %v, want %s removed", fake.deleted, e.ID)
			}
		})
	}
}

func TestWatchSkipsDeleted(t *testing.T) {
	tx, callerID := setup(t)
	release := make(chan struct{})
	fake := &fakeProvider{onCreate: func() { <-release }}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})

	e, err := m.Create(context.Background(), CreateRequest{CallerID: callerID, Type: db.EnvRegularTesting, Workload: "app", Template: dashboard})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := db.MarkEnvironmentDeleting(context.Background(), tx, e.ID); err != nil {
		t.Fatalf("MarkEnvironmentDeleting() error = %v", err)
	}
	close(release)
	m.WaitReady()

	if got := status(t, tx, e.ID).Status; got != db.EnvDeleting {
		t.Errorf("status = %s, want DELETING", got)
	}
	if fake.readyCalls != 0 {
		t.Errorf("readiness checks = %d, want 0 for a deleted environment", fake.readyCalls)
	}
}
