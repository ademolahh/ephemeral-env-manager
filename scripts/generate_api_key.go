// Generate an API key for an existing caller. Callers sign up with POST /callers.
// Run with: DATABASE_URL=... go run ./scripts/generate_api_key.go -caller-id UUID
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
)

func main() {
	callerFlag := flag.String("caller-id", "", "caller UUID to issue the key for")
	expiresIn := flag.Duration("expires-in", 30*24*time.Hour, "API key validity (for example, 720h)")
	flag.Parse()
	if flag.NArg() != 0 || *callerFlag == "" || *expiresIn <= 0 {
		log.Fatal("usage: go run ./scripts/generate_api_key.go -caller-id UUID [-expires-in 720h]")
	}
	if os.Getenv("DATABASE_URL") == "" {
		log.Fatal("DATABASE_URL is required")
	}

	callerID, err := uuid.Parse(*callerFlag)
	if err != nil || callerID == uuid.Nil {
		log.Fatalf("invalid caller ID %q", *callerFlag)
	}

	ctx := context.Background()
	pool, err := db.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	if _, err := db.LockCaller(ctx, pool, callerID); err != nil {
		log.Fatal(err)
	}

	expiresAt := time.Now().Add(*expiresIn)
	key, hash := auth.GenerateAPIKey()
	if _, err := db.CreateAPIKey(ctx, pool, callerID, hash, expiresAt); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Caller ID: %s\nAPI key: %s\nExpires: %s\n", callerID, key, expiresAt.Format(time.RFC3339))
}
