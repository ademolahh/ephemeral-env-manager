// Configure local Docker pricing and capacity after database migrations.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func main() {
	cpu := flag.Int("cpu", 100, "total Docker CPU cores")
	ram := flag.Int("ram", 100, "total Docker RAM in GB")
	disk := flag.Int("disk", 1000, "total Docker disk in GB")
	cpuPrice := flag.String("cpu-price", "0.004048", "hourly USD price per CPU core")
	ramPrice := flag.String("ram-price", "0.004445", "hourly USD price per GB of RAM")
	diskPrice := flag.String("disk-price", "0.00011", "hourly USD price per GB of disk")
	flag.Parse()
	if flag.NArg() != 0 || *cpu < 1 || *ram < 1 || *disk < 1 {
		log.Fatal("capacity values must be positive integers")
	}
	if os.Getenv("DATABASE_URL") == "" {
		log.Fatal("DATABASE_URL is required")
	}
	parsePrice := func(name, value string) decimal.Decimal {
		price, err := decimal.NewFromString(value)
		if err != nil || price.IsNegative() {
			log.Fatalf("%s must be a nonnegative decimal", name)
		}
		return price
	}
	prices := provider.Prices{
		CPUCore: parsePrice("cpu-price", *cpuPrice),
		RAMGB:   parsePrice("ram-price", *ramPrice),
		DiskGB:  parsePrice("disk-price", *diskPrice),
	}
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
