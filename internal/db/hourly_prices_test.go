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

type pricesRow struct {
	ID     uuid.UUID
	CPU    int64
	RAM    int64
	Disk   int64
	Copies int
}

func getPrices(t *testing.T, tx pgx.Tx, p provider.Type) pricesRow {
	t.Helper()
	var r pricesRow
	err := tx.QueryRow(context.Background(), `
		SELECT id, cpu_cores, ram_gb, disk_gb,
			(SELECT count(*) FROM hourly_prices WHERE provider = $1)
		FROM hourly_prices WHERE provider = $1`, p,
	).Scan(&r.ID, &r.CPU, &r.RAM, &r.Disk, &r.Copies)
	if err != nil {
		t.Fatalf("get prices %s: %v", p, err)
	}
	return r
}

func checkPrices(t *testing.T, got pricesRow, want provider.Prices) {
	t.Helper()
	if got.CPU != want.PerCPUCore || got.RAM != want.PerRAMGB || got.Disk != want.PerDiskGB {
		t.Errorf("prices = %d/%d/%d, want %d/%d/%d",
			got.CPU, got.RAM, got.Disk,
			want.PerCPUCore, want.PerRAMGB, want.PerDiskGB)
	}
	if got.Copies != 1 {
		t.Errorf("rows = %d, want 1", got.Copies)
	}
}

var dockerPrices = provider.Prices{
	PerCPUCore: 4048,
	PerRAMGB:   4445,
	PerDiskGB:  110,
}

func TestSetHourlyPricesInsert(t *testing.T) {
	tx := testTx(t)

	if err := SetHourlyPrices(context.Background(), tx, provider.Docker, dockerPrices); err != nil {
		t.Fatalf("SetHourlyPrices() error = %v", err)
	}

	checkPrices(t, getPrices(t, tx, provider.Docker), dockerPrices)
}

func TestSetHourlyPricesUpdate(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()

	if err := SetHourlyPrices(ctx, tx, provider.Docker, dockerPrices); err != nil {
		t.Fatalf("SetHourlyPrices() initial error = %v", err)
	}
	before := getPrices(t, tx, provider.Docker)

	updated := provider.Prices{
		PerCPUCore: 10000,
		PerRAMGB:   0,
		PerDiskGB:  123,
	}
	if err := SetHourlyPrices(ctx, tx, provider.Docker, updated); err != nil {
		t.Fatalf("SetHourlyPrices() error = %v", err)
	}

	got := getPrices(t, tx, provider.Docker)
	if got.ID != before.ID {
		t.Errorf("id = %s, want unchanged %s", got.ID, before.ID)
	}
	checkPrices(t, got, updated)
}

func TestSetHourlyPricesProvidersIndependent(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	cloudPrices := provider.Prices{
		PerCPUCore: 41600,
		PerRAMGB:   5200,
		PerDiskGB:  100,
	}

	if err := SetHourlyPrices(ctx, tx, provider.Docker, dockerPrices); err != nil {
		t.Fatalf("SetHourlyPrices(docker) error = %v", err)
	}
	if err := SetHourlyPrices(ctx, tx, provider.Cloud, cloudPrices); err != nil {
		t.Fatalf("SetHourlyPrices(cloud) error = %v", err)
	}

	checkPrices(t, getPrices(t, tx, provider.Docker), dockerPrices)
	checkPrices(t, getPrices(t, tx, provider.Cloud), cloudPrices)
}

func TestSetHourlyPricesErrors(t *testing.T) {
	tests := []struct {
		name     string
		provider provider.Type
		prices   provider.Prices
		code     string
	}{
		{
			name:     "negative cpu",
			provider: provider.Docker,
			prices:   provider.Prices{PerCPUCore: -1},
			code:     "23514",
		},
		{
			name:     "negative ram",
			provider: provider.Docker,
			prices:   provider.Prices{PerRAMGB: -1},
			code:     "23514",
		},
		{
			name:     "negative disk",
			provider: provider.Docker,
			prices:   provider.Prices{PerDiskGB: -1},
			code:     "23514",
		},
		{
			name:     "invalid provider",
			provider: "aws",
			prices:   dockerPrices,
			code:     "22P02",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			err := SetHourlyPrices(context.Background(), tx, tt.provider, tt.prices)
			if got := pgCode(err); got != tt.code {
				t.Fatalf("SetHourlyPrices() error = %v, want code %q", err, tt.code)
			}
			if want := "set hourly prices " + string(tt.provider) + ":"; !strings.HasPrefix(err.Error(), want) {
				t.Errorf("SetHourlyPrices() error = %v, want %q prefix", err, want)
			}
		})
	}
}

func TestGetHourlyPrices(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()

	if err := SetHourlyPrices(ctx, tx, provider.Docker, dockerPrices); err != nil {
		t.Fatalf("SetHourlyPrices() error = %v", err)
	}

	got, err := GetHourlyPrices(ctx, tx, provider.Docker)
	if err != nil {
		t.Fatalf("GetHourlyPrices() error = %v", err)
	}
	if got != dockerPrices {
		t.Errorf("GetHourlyPrices() = %+v, want %+v", got, dockerPrices)
	}
}

func TestGetHourlyPricesNotFound(t *testing.T) {
	tx := testTx(t)
	if err := SetHourlyPrices(context.Background(), tx, provider.Docker, dockerPrices); err != nil {
		t.Fatalf("SetHourlyPrices() error = %v", err)
	}

	_, err := GetHourlyPrices(context.Background(), tx, provider.Cloud)
	if !errors.Is(err, ErrPricesNotFound) {
		t.Errorf("GetHourlyPrices() error = %v, want ErrPricesNotFound", err)
	}
}

func TestGetHourlyPricesQueryError(t *testing.T) {
	tx := testTx(t)
	_, err := GetHourlyPrices(context.Background(), tx, "aws")
	if errors.Is(err, ErrPricesNotFound) {
		t.Fatalf("GetHourlyPrices() error = %v, want query error", err)
	}
	if got := pgCode(err); got != "22P02" {
		t.Errorf("GetHourlyPrices() error = %v, want invalid enum value", err)
	}
}
