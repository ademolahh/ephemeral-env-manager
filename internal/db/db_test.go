package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ademolahh/ephemeral-env-manager/internal/dbtest"
)

func testTx(t *testing.T) pgx.Tx {
	t.Helper()
	return dbtest.Tx(t)
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
