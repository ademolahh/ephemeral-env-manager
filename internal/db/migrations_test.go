package db_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ademolahh/ephemeral-env-manager/internal/dbtest"
)

func applyMigration(t *testing.T, tx pgx.Tx, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	if _, err := tx.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("apply %s: %v", filepath.Base(file), err)
	}
}

func TestMillicoresMigrationScalesExistingRows(t *testing.T) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbtest.DatabaseURL(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { tx.Rollback(context.Background()) })

	if _, err := tx.Exec(ctx, "CREATE SCHEMA eem_migration_test; SET LOCAL search_path TO eem_migration_test"); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	var target string
	for _, f := range dbtest.Migrations(t) {
		if strings.HasPrefix(filepath.Base(f), "000009_") {
			target = f
			break
		}
		applyMigration(t, tx, f)
	}
	if target == "" {
		t.Fatal("migration 000009 not found")
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO callers (id, quota_cpu_cores, quota_ram_gb, quota_disk_gb, cost_budget_total, budget_expires_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 4, 8, 50, 100, now() + interval '1 day');
		INSERT INTO capacity_pools (id, provider, default_cpu_cores, default_ram_gb, default_disk_gb,
			available_cpu_cores, available_ram_gb, available_disk_gb)
		VALUES (gen_random_uuid(), 'docker', 100, 100, 1000, 98, 96, 990);
		INSERT INTO environments (id, expires_at, type, provider, cpu_cores, ram_gb, disk_gb, hourly_rate,
			caller_id, template_name, template_version, estimated_cost, reserved_cost)
		VALUES (gen_random_uuid(), now(), 'PR_REVIEW', 'docker', 2, 4, 10, 1,
			'00000000-0000-0000-0000-000000000001', 't', 'v1', 0, 0)`); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	applyMigration(t, tx, target)

	var quotaCPU, quotaRAM, poolCPU, poolAvailRAM, envCPU, envRAM, envDisk int
	if err := tx.QueryRow(ctx, `
		SELECT c.quota_cpu_millis, c.quota_ram_mb, p.default_cpu_millis, p.available_ram_mb,
			e.cpu_millis, e.ram_mb, e.disk_gb
		FROM callers c, capacity_pools p, environments e`).Scan(
		&quotaCPU, &quotaRAM, &poolCPU, &poolAvailRAM, &envCPU, &envRAM, &envDisk); err != nil {
		t.Fatalf("read migrated rows: %v", err)
	}

	got := []int{quotaCPU, quotaRAM, poolCPU, poolAvailRAM, envCPU, envRAM, envDisk}
	want := []int{4000, 8192, 100000, 98304, 2000, 4096, 10}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("migrated values = %v, want %v", got, want)
			break
		}
	}

	applyMigration(t, tx, strings.Replace(target, ".up.sql", ".down.sql", 1))
	if err := tx.QueryRow(ctx, `SELECT cpu_cores, ram_gb FROM environments`).Scan(&envCPU, &envRAM); err != nil {
		t.Fatalf("read rolled back environment: %v", err)
	}
	if envCPU != 2 || envRAM != 4 {
		t.Errorf("rolled back environment = %d cpu, %d ram, want 2 and 4", envCPU, envRAM)
	}
}
