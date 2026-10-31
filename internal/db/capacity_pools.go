package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func SetCapacityPool(ctx context.Context, q DBTX, p provider.Type, total provider.Resources) error {
	_, err := q.Exec(ctx, `
		INSERT INTO capacity_pools (
			id, provider,
			default_cpu_cores, default_ram_gb, default_disk_gb,
			available_cpu_cores, available_ram_gb, available_disk_gb
		)
		VALUES ($1, $2, $3, $4, $5, $3, $4, $5)
		ON CONFLICT (provider) DO UPDATE SET
			available_cpu_cores = capacity_pools.available_cpu_cores + EXCLUDED.default_cpu_cores - capacity_pools.default_cpu_cores,
			available_ram_gb    = capacity_pools.available_ram_gb + EXCLUDED.default_ram_gb - capacity_pools.default_ram_gb,
			available_disk_gb   = capacity_pools.available_disk_gb + EXCLUDED.default_disk_gb - capacity_pools.default_disk_gb,
			default_cpu_cores   = EXCLUDED.default_cpu_cores,
			default_ram_gb      = EXCLUDED.default_ram_gb,
			default_disk_gb     = EXCLUDED.default_disk_gb`,
		uuid.New(), p, total.CPUCore, total.RAMGB, total.DiskGB,
	)
	if err != nil {
		return fmt.Errorf("set capacity pool %s: %w", p, err)
	}
	return nil
}
