package env

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

// leftProvisioning records an environment as a crashed eem process would
// leave it: reserved and PROVISIONING, with nobody watching it.
func leftProvisioning(t *testing.T, tx pgx.Tx, callerID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	res := provider.Resources{CPUMillis: 500, RAMMB: 512, DiskGB: 1}
	id, err := db.CreateEnvironment(ctx, tx, db.NewEnvironment{
		CallerID:        callerID,
		Type:            db.EnvRegularTesting,
		Provider:        provider.Docker,
		Resources:       res,
		TTL:             time.Hour,
		TemplateName:    "t",
		TemplateVersion: "v1",
	})
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	if err := db.ReserveCapacity(ctx, tx, provider.Docker, res); err != nil {
		t.Fatalf("ReserveCapacity() error = %v", err)
	}
	return id
}

func TestResumeWatchesProvisioningAfterRestart(t *testing.T) {
	tests := []struct {
		name    string
		status  provider.Status
		want    db.EnvStatus
		deleted bool
	}{
		{name: "containers ready", status: provider.StatusRunning, want: db.EnvRunning},
		{name: "containers never created", status: provider.StatusMissing, want: db.EnvFailed, deleted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, callerID := setup(t)
			id := leftProvisioning(t, tx, callerID)

			fake := &fakeProvider{status: tt.status}
			m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: fake})
			n, err := m.Resume(context.Background())
			if err != nil || n != 1 {
				t.Fatalf("Resume() = %d, %v, want 1 environment", n, err)
			}
			m.Wait()

			e, err := db.GetEnvironment(context.Background(), tx, id)
			if err != nil {
				t.Fatalf("GetEnvironment() error = %v", err)
			}
			if e.Status != tt.want {
				t.Errorf("status = %s, want %s", e.Status, tt.want)
			}
			if got := slices.Contains(fake.deleted, id.String()); got != tt.deleted {
				t.Errorf("containers deleted = %v, want %v", got, tt.deleted)
			}
		})
	}
}
