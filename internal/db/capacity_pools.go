package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

var (
	ErrPoolNotFound       = errors.New("capacity pool not found")
	ErrCapacityBelowUsage = errors.New("capacity pool total below current usage")
)

type CapacityPool struct {
	Default   provider.Resources
	Available provider.Resources
}

func SetCapacityPool(ctx context.Context, q DBTX, p provider.Type, total provider.Resources) error {
	tag, err := q.Exec(ctx, `
		INSERT INTO capacity_pools (
			id, provider,
			default_cpu_millis, default_ram_mb, default_disk_gb,
			available_cpu_millis, available_ram_mb, available_disk_gb
		)
		VALUES ($1, $2, $3, $4, $5, $3, $4, $5)
		ON CONFLICT (provider) DO UPDATE SET
			available_cpu_millis = capacity_pools.available_cpu_millis + EXCLUDED.default_cpu_millis - capacity_pools.default_cpu_millis,
			available_ram_mb    = capacity_pools.available_ram_mb + EXCLUDED.default_ram_mb - capacity_pools.default_ram_mb,
			available_disk_gb   = capacity_pools.available_disk_gb + EXCLUDED.default_disk_gb - capacity_pools.default_disk_gb,
			default_cpu_millis   = EXCLUDED.default_cpu_millis,
			default_ram_mb      = EXCLUDED.default_ram_mb,
			default_disk_gb     = EXCLUDED.default_disk_gb
		WHERE capacity_pools.default_cpu_millis - capacity_pools.available_cpu_millis <= EXCLUDED.default_cpu_millis
			AND capacity_pools.default_ram_mb - capacity_pools.available_ram_mb <= EXCLUDED.default_ram_mb
			AND capacity_pools.default_disk_gb - capacity_pools.available_disk_gb <= EXCLUDED.default_disk_gb`,
		uuid.New(), p, total.CPUMillis, total.RAMMB, total.DiskGB,
	)
	if err != nil {
		return fmt.Errorf("set capacity pool %s: %w", p, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set capacity pool %s: %w", p, ErrCapacityBelowUsage)
	}
	return nil
}

func LockCapacityPool(ctx context.Context, q DBTX, p provider.Type) (CapacityPool, error) {
	var c CapacityPool
	err := q.QueryRow(ctx, `
		SELECT default_cpu_millis, default_ram_mb, default_disk_gb,
			available_cpu_millis, available_ram_mb, available_disk_gb
		FROM capacity_pools WHERE provider = $1
		FOR UPDATE`, p,
	).Scan(
		&c.Default.CPUMillis, &c.Default.RAMMB, &c.Default.DiskGB,
		&c.Available.CPUMillis, &c.Available.RAMMB, &c.Available.DiskGB,
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
			available_cpu_millis = available_cpu_millis - $2,
			available_ram_mb    = available_ram_mb - $3,
			available_disk_gb   = available_disk_gb - $4
		WHERE provider = $1`,
		p, r.CPUMillis, r.RAMMB, r.DiskGB,
	)
	if err != nil {
		return fmt.Errorf("reserve capacity %s: %w", p, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("reserve capacity %s: %w", p, ErrPoolNotFound)
	}
	return nil
}

type ProviderPool struct {
	Provider provider.Type
	CapacityPool
}

func ListCapacityPools(ctx context.Context, q DBTX) ([]ProviderPool, error) {
	rows, err := q.Query(ctx, `
		SELECT provider, default_cpu_millis, default_ram_mb, default_disk_gb,
			available_cpu_millis, available_ram_mb, available_disk_gb
		FROM capacity_pools ORDER BY provider`)
	if err != nil {
		return nil, fmt.Errorf("list capacity pools: %w", err)
	}

	pools, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ProviderPool, error) {
		var p ProviderPool
		err := row.Scan(&p.Provider,
			&p.Default.CPUMillis, &p.Default.RAMMB, &p.Default.DiskGB,
			&p.Available.CPUMillis, &p.Available.RAMMB, &p.Available.DiskGB)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("list capacity pools: %w", err)
	}

	return pools, nil
}
