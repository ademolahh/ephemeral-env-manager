package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func SetHourlyPrices(ctx context.Context, q DBTX, p provider.Type, prices provider.Prices) error {
	_, err := q.Exec(ctx, `
		INSERT INTO hourly_prices (id, provider, cpu_cores, ram_gb, disk_gb)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (provider) DO UPDATE SET
			cpu_cores = EXCLUDED.cpu_cores,
			ram_gb    = EXCLUDED.ram_gb,
			disk_gb   = EXCLUDED.disk_gb`,
		uuid.New(), p, prices.CPUCore, prices.RAMGB, prices.DiskGB,
	)
	if err != nil {
		return fmt.Errorf("set hourly prices %s: %w", p, err)
	}
	return nil
}
