// Generate an API key for a new caller or an existing caller.
// Run with: DATABASE_URL=... go run ./scripts/generate_api_key.go [-caller-id UUID]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func main() {
	callerFlag := flag.String("caller-id", "", "existing caller UUID; omit to create a new caller")
	expiresIn := flag.Duration("expires-in", 30*24*time.Hour, "API key validity (for example, 720h)")
	flag.Parse()
	if flag.NArg() != 0 || *expiresIn <= 0 {
		log.Fatal("usage: go run ./scripts/generate_api_key.go [-caller-id UUID] [-expires-in 720h]")
	}
	if os.Getenv("DATABASE_URL") == "" {
		log.Fatal("DATABASE_URL is required")
	}

	ctx := context.Background()
	pool, err := db.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	var callerID uuid.UUID
	if *callerFlag != "" {
		callerID, err = uuid.Parse(*callerFlag)
		if err != nil {
			log.Fatalf("invalid caller ID: %v", err)
		}
		if callerID == uuid.Nil {
			log.Fatal("caller ID cannot be empty")
		}
	}

	capacity, err := db.LockCapacityPool(ctx, pool, provider.Docker)
	if err != nil {
		log.Fatalf("get Docker capacity for caller quota: %v (run make seed-docker first)", err)
	}
	expiresAt := time.Now().Add(*expiresIn)
	quota := capacity.Default
	budget := decimal.NewFromInt(100)
	key, hash := auth.GenerateAPIKey()
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if callerID == uuid.Nil {
			var err error
			callerID, err = db.CreateCaller(ctx, tx, db.NewCaller{
				Role:            db.RoleAdmin,
				PermittedEnv:    db.EnvProdBug,
				Quota:           quota,
				CostBudget:      budget,
				BudgetExpiresAt: expiresAt,
			})
			if err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `
				UPDATE callers SET role = $2, permitted_env = $3,
					quota_cpu_cores = $4, quota_ram_gb = $5, quota_disk_gb = $6
				WHERE id = $1`, callerID, db.RoleAdmin, db.EnvProdBug,
				quota.CPUCore, quota.RAMGB, quota.DiskGB)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("caller %s not found", callerID)
			}
		}
		_, err := db.CreateAPIKey(ctx, tx, callerID, hash, expiresAt)
		return err
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Caller ID: %s\nAPI key: %s\nExpires: %s\n", callerID, key, expiresAt.Format(time.RFC3339))
}
