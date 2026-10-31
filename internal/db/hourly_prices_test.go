package db

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type pricesRow struct {
	ID     uuid.UUID
	CPU    string
	RAM    string
	Disk   string
	Copies int
}

func getPrices(t *testing.T, tx pgx.Tx, p provider.Type) pricesRow {
	t.Helper()
	var r pricesRow
	err := tx.QueryRow(context.Background(), `
		SELECT id, cpu_cores::text, ram_gb::text, disk_gb::text,
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
	if got.CPU != want.CPUCore.String() || got.RAM != want.RAMGB.String() || got.Disk != want.DiskGB.String() {
		t.Errorf("prices = %s/%s/%s, want %s/%s/%s",
			got.CPU, got.RAM, got.Disk,
			want.CPUCore, want.RAMGB, want.DiskGB)
	}
	if got.Copies != 1 {
		t.Errorf("rows = %d, want 1", got.Copies)
	}
}

var dockerPrices = provider.Prices{
	CPUCore: decimal.RequireFromString("0.004048"),
	RAMGB:   decimal.RequireFromString("0.004445"),
	DiskGB:  decimal.RequireFromString("0.00011"),
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
		CPUCore: decimal.RequireFromString("0.01"),
		RAMGB:   decimal.Zero,
		DiskGB:  decimal.RequireFromString("0.000123456789"),
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
		CPUCore: decimal.RequireFromString("0.0416"),
		RAMGB:   decimal.RequireFromString("0.0052"),
		DiskGB:  decimal.RequireFromString("0.0001"),
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
			prices:   provider.Prices{CPUCore: decimal.RequireFromString("-0.1")},
			code:     "23514",
		},
		{
			name:     "negative ram",
			provider: provider.Docker,
			prices:   provider.Prices{RAMGB: decimal.RequireFromString("-0.1")},
			code:     "23514",
		},
		{
			name:     "negative disk",
			provider: provider.Docker,
			prices:   provider.Prices{DiskGB: decimal.RequireFromString("-0.1")},
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
