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

	"github.com/jackc/pgx/v5"
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
