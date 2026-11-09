// Configure local Docker pricing and capacity after database migrations.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func main() {
	cpu := flag.Int("cpu", 100, "total Docker CPU cores")
	ram := flag.Int("ram", 100, "total Docker RAM in GB")
	disk := flag.Int("disk", 1000, "total Docker disk in GB")
	cpuPrice := flag.Int64("cpu-price", 4048, "hourly price per CPU core in micro-USD")
	ramPrice := flag.Int64("ram-price", 4445, "hourly price per GB of RAM in micro-USD")
	diskPrice := flag.Int64("disk-price", 110, "hourly price per GB of disk in micro-USD")
	flag.Parse()
	if flag.NArg() != 0 || *cpu < 1 || *ram < 1 || *disk < 1 {
		log.Fatal("capacity values must be positive integers")
	}
	if *cpuPrice < 0 || *ramPrice < 0 || *diskPrice < 0 {
		log.Fatal("prices must not be negative")
	}
	if os.Getenv("DATABASE_URL") == "" {
		log.Fatal("DATABASE_URL is required")
	}
	prices := provider.Prices{CPUCore: *cpuPrice, RAMGB: *ramPrice, DiskGB: *diskPrice}
	capacity := provider.Resources{CPUCore: *cpu, RAMGB: *ram, DiskGB: *disk}

	ctx := context.Background()
	pool, err := db.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := db.SetHourlyPrices(ctx, tx, provider.Docker, prices); err != nil {
			return err
		}
		return db.SetCapacityPool(ctx, tx, provider.Docker, capacity)
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Docker pricing and capacity configured: %d CPU, %d GB RAM, %d GB disk\n", *cpu, *ram, *disk)
}
