// Create an admin account. The password is read from the first line of stdin.
// Run with: make admin ARGS="-username alice"
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
)

func main() {
	username := flag.String("username", "", "admin username")
	flag.Parse()
	if flag.NArg() != 0 || strings.TrimSpace(*username) == "" {
		log.Fatal("usage: make admin ARGS=\"-username NAME\"")
	}
	if os.Getenv("DATABASE_URL") == "" {
		log.Fatal("DATABASE_URL is required")
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		log.Fatalf("read password from stdin: %v", err)
	}
	hash, err := auth.HashPassword(strings.TrimRight(line, "\r\n"))
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	pool, err := db.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	id, err := db.CreateAdmin(ctx, pool, strings.TrimSpace(*username), hash)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Admin %s created: %s\n", strings.TrimSpace(*username), id)
}
