package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

var ErrPoolNotFound = errors.New("capacity pool not found")

type CapacityPool struct {
	Default   provider.Resources
	Available provider.Resources
}

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

func LockCapacityPool(ctx context.Context, q DBTX, p provider.Type) (CapacityPool, error) {
	var c CapacityPool
	err := q.QueryRow(ctx, `
		SELECT default_cpu_cores, default_ram_gb, default_disk_gb,
			available_cpu_cores, available_ram_gb, available_disk_gb
		FROM capacity_pools WHERE provider = $1
		FOR UPDATE`, p,
	).Scan(
		&c.Default.CPUCore, &c.Default.RAMGB, &c.Default.DiskGB,
		&c.Available.CPUCore, &c.Available.RAMGB, &c.Available.DiskGB,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return CapacityPool{}, fmt.Errorf("lock capacity pool %s: %w", p, ErrPoolNotFound)
	}

	if err != nil {
		return CapacityPool{}, fmt.Errorf("lock capacity pool %s: %w", p, err)
	}

	return c, nil
}

func ReserveCapacity(ctx context.Context, q DBTX, p provider.Type, r provider.Resources) error {
	tag, err := q.Exec(ctx, `
		UPDATE capacity_pools SET
			available_cpu_cores = available_cpu_cores - $2,
			available_ram_gb    = available_ram_gb - $3,
			available_disk_gb   = available_disk_gb - $4
		WHERE provider = $1`,
		p, r.CPUCore, r.RAMGB, r.DiskGB,
	)
	if err != nil {
		return fmt.Errorf("reserve capacity %s: %w", p, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("reserve capacity %s: %w", p, ErrPoolNotFound)
	}
	return nil
}
