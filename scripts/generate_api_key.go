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

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func main() {
	callerFlag := flag.String("caller-id", "", "existing caller UUID; omit to create a new caller")
	expiresIn := flag.Duration("expires-in", 30*24*time.Hour, "API key validity (for example, 720h)")
	role := flag.String("role", string(db.RoleUser), "role of a new caller: user or agent")
	flag.Parse()
	if flag.NArg() != 0 || *expiresIn <= 0 || (*role != string(db.RoleUser) && *role != string(db.RoleAgent)) {
		log.Fatal("usage: make api-key [ARGS=\"-caller-id UUID -role user|agent -expires-in 720h\"]")
	}

	permitted := db.EnvRegularTesting
	if db.Role(*role) == db.RoleAgent {
		permitted = db.EnvPRReview
	}
	if os.Getenv("DATABASE_URL") == "" {
		log.Fatal("DATABASE_URL is required")
	}

	var callerID uuid.UUID
	if *callerFlag != "" {
		id, err := uuid.Parse(*callerFlag)
		if err != nil || id == uuid.Nil {
			log.Fatalf("invalid caller ID %q", *callerFlag)
		}
		callerID = id
	}

	ctx := context.Background()
	pool, err := db.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	expiresAt := time.Now().Add(*expiresIn)
	key, hash := auth.GenerateAPIKey()
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if callerID == uuid.Nil {
			id, err := db.CreateCaller(ctx, tx, db.NewCaller{
				Role:            db.Role(*role),
				PermittedEnv:    permitted,
				Quota:           provider.Resources{CPUCore: 5, RAMGB: 16, DiskGB: 50},
				CostBudgetTotal: 10000,
				BudgetExpiresAt: time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second),
			})
			if err != nil {
				return err
			}
			callerID = id
		} else if _, err := db.LockCaller(ctx, tx, callerID); err != nil {
			return err
		}

		_, err := db.CreateAPIKey(ctx, tx, callerID, hash, expiresAt)
		return err
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Caller ID: %s\nAPI key: %s\nExpires: %s\n", callerID, key, expiresAt.Format(time.RFC3339))
}
