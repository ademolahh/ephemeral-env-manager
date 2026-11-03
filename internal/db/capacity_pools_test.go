package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type poolRow struct {
	ID        uuid.UUID
	Default   provider.Resources
	Available provider.Resources
}

func getPool(t *testing.T, tx pgx.Tx, p provider.Type) poolRow {
	t.Helper()
	var r poolRow
	err := tx.QueryRow(context.Background(), `
		SELECT id,
			default_cpu_cores, default_ram_gb, default_disk_gb,
			available_cpu_cores, available_ram_gb, available_disk_gb
		FROM capacity_pools WHERE provider = $1`, p,
	).Scan(
		&r.ID,
		&r.Default.CPUCore, &r.Default.RAMGB, &r.Default.DiskGB,
		&r.Available.CPUCore, &r.Available.RAMGB, &r.Available.DiskGB,
	)
	if err != nil {
		t.Fatalf("get pool %s: %v", p, err)
	}
	return r
}

func allocate(t *testing.T, tx pgx.Tx, p provider.Type, r provider.Resources) {
	t.Helper()
	_, err := tx.Exec(context.Background(), `
		UPDATE capacity_pools SET
			available_cpu_cores = available_cpu_cores - $2,
			available_ram_gb    = available_ram_gb - $3,
			available_disk_gb   = available_disk_gb - $4
		WHERE provider = $1`,
		p, r.CPUCore, r.RAMGB, r.DiskGB,
	)
	if err != nil {
		t.Fatalf("allocate %+v from %s: %v", r, p, err)
	}
}

func TestSetCapacityPoolInsert(t *testing.T) {
	tx := testTx(t)
	total := provider.Resources{CPUCore: 16, RAMGB: 64, DiskGB: 500}

	if err := SetCapacityPool(context.Background(), tx, provider.Docker, total); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}

	got := getPool(t, tx, provider.Docker)
	if got.Default != total {
		t.Errorf("default = %+v, want %+v", got.Default, total)
	}
	if got.Available != total {
		t.Errorf("available = %+v, want %+v", got.Available, total)
	}
}

func TestSetCapacityPoolUpdate(t *testing.T) {
	initial := provider.Resources{CPUCore: 16, RAMGB: 64, DiskGB: 500}
	used := provider.Resources{CPUCore: 4, RAMGB: 16, DiskGB: 100}

	tests := []struct {
		name          string
		used          provider.Resources
		total         provider.Resources
		wantAvailable provider.Resources
	}{
		{
			name:          "same total idle",
			total:         initial,
			wantAvailable: initial,
		},
		{
			name:          "grow idle",
			total:         provider.Resources{CPUCore: 32, RAMGB: 128, DiskGB: 1000},
			wantAvailable: provider.Resources{CPUCore: 32, RAMGB: 128, DiskGB: 1000},
		},
		{
			name:          "shrink idle",
			total:         provider.Resources{CPUCore: 8, RAMGB: 32, DiskGB: 250},
			wantAvailable: provider.Resources{CPUCore: 8, RAMGB: 32, DiskGB: 250},
		},
		{
			name:          "same total in use",
			used:          used,
			total:         initial,
			wantAvailable: provider.Resources{CPUCore: 12, RAMGB: 48, DiskGB: 400},
		},
		{
			name:          "grow in use",
			used:          used,
			total:         provider.Resources{CPUCore: 20, RAMGB: 80, DiskGB: 600},
			wantAvailable: provider.Resources{CPUCore: 16, RAMGB: 64, DiskGB: 500},
		},
		{
			name:          "shrink in use",
			used:          used,
			total:         provider.Resources{CPUCore: 10, RAMGB: 40, DiskGB: 300},
			wantAvailable: provider.Resources{CPUCore: 6, RAMGB: 24, DiskGB: 200},
		},
		{
			name:          "shrink to usage",
			used:          used,
			total:         used,
			wantAvailable: provider.Resources{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			ctx := context.Background()

			if err := SetCapacityPool(ctx, tx, provider.Docker, initial); err != nil {
				t.Fatalf("SetCapacityPool() initial error = %v", err)
			}
			before := getPool(t, tx, provider.Docker)
			allocate(t, tx, provider.Docker, tt.used)

			if err := SetCapacityPool(ctx, tx, provider.Docker, tt.total); err != nil {
				t.Fatalf("SetCapacityPool() error = %v", err)
			}

			got := getPool(t, tx, provider.Docker)
			if got.ID != before.ID {
				t.Errorf("id = %s, want unchanged %s", got.ID, before.ID)
			}
			if got.Default != tt.total {
				t.Errorf("default = %+v, want %+v", got.Default, tt.total)
			}
			if got.Available != tt.wantAvailable {
				t.Errorf("available = %+v, want %+v", got.Available, tt.wantAvailable)
			}
		})
	}
}

func TestSetCapacityPoolShrinkBelowUsage(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()

	if err := SetCapacityPool(ctx, tx, provider.Docker, provider.Resources{CPUCore: 16, RAMGB: 64, DiskGB: 500}); err != nil {
		t.Fatalf("SetCapacityPool() initial error = %v", err)
	}
	allocate(t, tx, provider.Docker, provider.Resources{CPUCore: 10})

	err := SetCapacityPool(ctx, tx, provider.Docker, provider.Resources{CPUCore: 8, RAMGB: 64, DiskGB: 500})
	if !errors.Is(err, ErrCapacityBelowUsage) {
		t.Fatalf("SetCapacityPool() error = %v, want %v", err, ErrCapacityBelowUsage)
	}
	if !strings.HasPrefix(err.Error(), "set capacity pool docker:") {
		t.Errorf("SetCapacityPool() error = %v, want set capacity pool docker prefix", err)
	}

	got := getPool(t, tx, provider.Docker)
	if want := (provider.Resources{CPUCore: 16, RAMGB: 64, DiskGB: 500}); got.Default != want {
		t.Errorf("default = %+v, want unchanged %+v", got.Default, want)
	}
	if want := (provider.Resources{CPUCore: 6, RAMGB: 64, DiskGB: 500}); got.Available != want {
		t.Errorf("available = %+v, want unchanged %+v", got.Available, want)
	}

	if err := SetCapacityPool(ctx, tx, provider.Docker, provider.Resources{CPUCore: 10, RAMGB: 64, DiskGB: 500}); err != nil {
		t.Fatalf("SetCapacityPool() down to usage error = %v", err)
	}
	if got := getPool(t, tx, provider.Docker).Available.CPUCore; got != 0 {
		t.Errorf("available cpu = %d, want 0 when total equals usage", got)
	}
}

func TestSetCapacityPoolNegative(t *testing.T) {
	tx := testTx(t)
	err := SetCapacityPool(context.Background(), tx, provider.Docker, provider.Resources{CPUCore: -1})
	if got := pgCode(err); got != "23514" {
		t.Errorf("SetCapacityPool() error = %v, want check violation", err)
	}
}

func TestSetCapacityPoolInvalidProvider(t *testing.T) {
	tx := testTx(t)
	err := SetCapacityPool(context.Background(), tx, "aws", provider.Resources{CPUCore: 1})
	if got := pgCode(err); got != "22P02" {
		t.Errorf("SetCapacityPool() error = %v, want invalid enum value", err)
	}
}

func TestSetCapacityPoolProvidersIndependent(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	docker := provider.Resources{CPUCore: 16, RAMGB: 64, DiskGB: 500}
	cloud := provider.Resources{CPUCore: 64, RAMGB: 256, DiskGB: 2000}

	if err := SetCapacityPool(ctx, tx, provider.Docker, docker); err != nil {
		t.Fatalf("SetCapacityPool(docker) error = %v", err)
	}
	if err := SetCapacityPool(ctx, tx, provider.Cloud, cloud); err != nil {
		t.Fatalf("SetCapacityPool(cloud) error = %v", err)
	}

	if got := getPool(t, tx, provider.Docker); got.Default != docker || got.Available != docker {
		t.Errorf("docker pool = %+v, want default and available %+v", got, docker)
	}
	if got := getPool(t, tx, provider.Cloud); got.Default != cloud || got.Available != cloud {
		t.Errorf("cloud pool = %+v, want default and available %+v", got, cloud)
	}
}
