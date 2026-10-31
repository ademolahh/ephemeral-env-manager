package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func testTx(t *testing.T) pgx.Tx {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { tx.Rollback(context.Background()) })

	if _, err := tx.Exec(ctx, "CREATE SCHEMA eem_test; SET LOCAL search_path TO eem_test"); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	sort.Strings(files)
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	return tx
}

func pgCode(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code
	}
	return ""
}

func TestNewInvalidURL(t *testing.T) {
	_, err := New(context.Background(), "not a url ::")
	if err == nil || !strings.HasPrefix(err.Error(), "create pool:") {
		t.Errorf("New() error = %v, want create pool error", err)
	}
}

func TestNewUnreachable(t *testing.T) {
	_, err := New(context.Background(), "postgres://user:pass@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	if err == nil || !strings.HasPrefix(err.Error(), "ping database:") {
		t.Errorf("New() error = %v, want ping database error", err)
	}
}

func TestNew(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := New(context.Background(), url)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	pool.Close()
}
