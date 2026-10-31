package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

var ErrPricesNotFound = errors.New("hourly prices not found")

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

func GetHourlyPrices(ctx context.Context, q DBTX, p provider.Type) (provider.Prices, error) {
	var prices provider.Prices
	err := q.QueryRow(ctx, `
		SELECT cpu_cores, ram_gb, disk_gb
		FROM hourly_prices WHERE provider = $1`, p,
	).Scan(&prices.CPUCore, &prices.RAMGB, &prices.DiskGB)
	if errors.Is(err, pgx.ErrNoRows) {
		return provider.Prices{}, fmt.Errorf("get hourly prices %s: %w", p, ErrPricesNotFound)
	}
	if err != nil {
		return provider.Prices{}, fmt.Errorf("get hourly prices %s: %w", p, err)
	}
	return prices, nil
}
