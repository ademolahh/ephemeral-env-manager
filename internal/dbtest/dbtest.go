package dbtest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ademolahh/ephemeral-env-manager/internal/session"
)

var dotEnvErr error

func init() {
	dotEnvErr = loadDotEnv(filepath.Join(repoRoot(), ".env"))
}

func DatabaseURL(t *testing.T) string {
	t.Helper()
	checkDotEnv(t)
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return url
}

func RequireDocker(t *testing.T) {
	t.Helper()
	checkDotEnv(t)
	if os.Getenv("TEST_DOCKER") == "" {
		t.Skip("TEST_DOCKER not set")
	}
}

func Tx(t *testing.T) pgx.Tx {
	t.Helper()
	url := DatabaseURL(t)
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

	for _, f := range Migrations(t) {
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

func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := DatabaseURL(t)
	ctx := context.Background()
	schema := "eem_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s; SET search_path TO %s", schema, schema)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), url)
		if err != nil {
			t.Errorf("connect for cleanup: %v", err)
			return
		}
		defer c.Close(context.Background())
		if _, err := c.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	for _, f := range Migrations(t) {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func Migrations(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRoot(), "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	sort.Strings(files)
	return files
}

func checkDotEnv(t *testing.T) {
	t.Helper()
	if dotEnvErr != nil {
		t.Fatal(dotEnvErr)
	}
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("load %s: %w", path, err)
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, value)
		}
	}

	if err := s.Err(); err != nil {
		return fmt.Errorf("load %s: %w", path, err)
	}
	return nil
}

func Sessions(t *testing.T) *session.Store {
	t.Helper()
	checkDotEnv(t)
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set")
	}

	ctx := context.Background()
	rdb, err := session.Connect(ctx, url)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}

	prefix := "eem_test:" + uuid.NewString() + ":"
	t.Cleanup(func() {
		defer rdb.Close()
		keys, err := rdb.Keys(context.Background(), prefix+"*").Result()
		if err != nil {
			t.Errorf("list test keys: %v", err)
			return
		}
		if len(keys) > 0 {
			rdb.Del(context.Background(), keys...)
		}
	})
	return session.New(rdb, prefix)
}
