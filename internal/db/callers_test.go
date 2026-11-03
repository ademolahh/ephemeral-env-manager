package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type callerRow struct {
	GithubLogin     *string
	GithubID        *string
	Role            string
	PermittedEnv    string
	Quota           provider.Resources
	CostBudgetTotal string
	BudgetExpiresAt time.Time
	BudgetSetAt     time.Time
}

func getCaller(t *testing.T, tx pgx.Tx, id uuid.UUID) callerRow {
	t.Helper()
	var r callerRow
	err := tx.QueryRow(context.Background(), `
		SELECT github_login, github_id, role::text, permitted_env::text,
			quota_cpu_cores, quota_ram_gb, quota_disk_gb,
			cost_budget_total::text, budget_expires_at, budget_set_at
		FROM callers WHERE id = $1`, id,
	).Scan(
		&r.GithubLogin, &r.GithubID, &r.Role, &r.PermittedEnv,
		&r.Quota.CPUCore, &r.Quota.RAMGB, &r.Quota.DiskGB,
		&r.CostBudgetTotal, &r.BudgetExpiresAt, &r.BudgetSetAt,
	)
	if err != nil {
		t.Fatalf("get caller %s: %v", id, err)
	}
	return r
}

func newCaller() NewCaller {
	return NewCaller{
		GithubLogin:     new("octocat"),
		GithubID:        new("583231"),
		Role:            RoleUser,
		PermittedEnv:    EnvPRReview,
		Quota:           provider.Resources{CPUCore: 4, RAMGB: 16, DiskGB: 50},
		CostBudgetTotal: decimal.RequireFromString("12.345"),
		BudgetExpiresAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

func TestCreateCaller(t *testing.T) {
	tx := testTx(t)
	c := newCaller()

	id, err := CreateCaller(context.Background(), tx, c)
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	if id == uuid.Nil {
		t.Fatal("CreateCaller() returned nil id")
	}

	got := getCaller(t, tx, id)
	if got.GithubLogin == nil || *got.GithubLogin != *c.GithubLogin {
		t.Errorf("github_login = %v, want %q", got.GithubLogin, *c.GithubLogin)
	}
	if got.GithubID == nil || *got.GithubID != *c.GithubID {
		t.Errorf("github_id = %v, want %q", got.GithubID, *c.GithubID)
	}
	if got.Role != string(c.Role) {
		t.Errorf("role = %q, want %q", got.Role, c.Role)
	}
	if got.PermittedEnv != string(c.PermittedEnv) {
		t.Errorf("permitted_env = %q, want %q", got.PermittedEnv, c.PermittedEnv)
	}
	if got.Quota != c.Quota {
		t.Errorf("quota = %+v, want %+v", got.Quota, c.Quota)
	}
	if got.CostBudgetTotal != c.CostBudgetTotal.String() {
		t.Errorf("cost_budget_total = %s, want %s", got.CostBudgetTotal, c.CostBudgetTotal)
	}
	if !got.BudgetExpiresAt.Equal(c.BudgetExpiresAt) {
		t.Errorf("budget_expires_at = %v, want %v", got.BudgetExpiresAt, c.BudgetExpiresAt)
	}
}

func TestCreateCallerNullGithub(t *testing.T) {
	tx := testTx(t)
	c := newCaller()
	c.GithubLogin = nil
	c.GithubID = nil
	c.Role = RoleAgent

	first, err := CreateCaller(context.Background(), tx, c)
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	second, err := CreateCaller(context.Background(), tx, c)
	if err != nil {
		t.Fatalf("CreateCaller() second error = %v", err)
	}
	if first == second {
		t.Errorf("CreateCaller() returned duplicate id %s", first)
	}

	got := getCaller(t, tx, first)
	if got.GithubLogin != nil || got.GithubID != nil {
		t.Errorf("github_login, github_id = %v, %v, want nil, nil", got.GithubLogin, got.GithubID)
	}
}

func TestCreateCallerErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NewCaller)
		code   string
	}{
		{
			name:   "negative cpu quota",
			mutate: func(c *NewCaller) { c.Quota.CPUCore = -1 },
			code:   "23514",
		},
		{
			name:   "negative ram quota",
			mutate: func(c *NewCaller) { c.Quota.RAMGB = -1 },
			code:   "23514",
		},
		{
			name:   "negative disk quota",
			mutate: func(c *NewCaller) { c.Quota.DiskGB = -1 },
			code:   "23514",
		},
		{
			name:   "negative cost budget",
			mutate: func(c *NewCaller) { c.CostBudgetTotal = decimal.RequireFromString("-0.01") },
			code:   "23514",
		},
		{
			name:   "invalid role",
			mutate: func(c *NewCaller) { c.Role = "superuser" },
			code:   "22P02",
		},
		{
			name:   "invalid env",
			mutate: func(c *NewCaller) { c.PermittedEnv = "STAGING" },
			code:   "22P02",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			c := newCaller()
			tt.mutate(&c)

			id, err := CreateCaller(context.Background(), tx, c)
			if err == nil {
				t.Fatal("CreateCaller() error = nil, want error")
			}
			if id != uuid.Nil {
				t.Errorf("CreateCaller() id = %s, want nil", id)
			}
			if !strings.HasPrefix(err.Error(), "create caller:") {
				t.Errorf("CreateCaller() error = %v, want create caller prefix", err)
			}
			if got := pgCode(err); got != tt.code {
				t.Errorf("CreateCaller() error code = %q, want %q", got, tt.code)
			}
		})
	}
}

func TestCreateCallerDuplicateGithubID(t *testing.T) {
	tx := testTx(t)
	c := newCaller()

	if _, err := CreateCaller(context.Background(), tx, c); err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	_, err := CreateCaller(context.Background(), tx, c)
	if !errors.Is(err, ErrGithubIDTaken) {
		t.Errorf("CreateCaller() duplicate error = %v, want %v", err, ErrGithubIDTaken)
	}

	var n int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM callers WHERE github_id = $1`, *c.GithubID).Scan(&n); err != nil {
		t.Fatalf("count callers: %v", err)
	}
	if n != 1 {
		t.Errorf("callers with github_id = %d, want 1", n)
	}
}

func TestSetCallerBudget(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	c := newCaller()

	id, err := CreateCaller(ctx, tx, c)
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE callers SET budget_set_at = now() - interval '1 day' WHERE id = $1`, id); err != nil {
		t.Fatalf("backdate budget_set_at: %v", err)
	}

	quota := provider.Resources{CPUCore: 8, RAMGB: 32, DiskGB: 100}
	budget := decimal.RequireFromString("99.99")
	expires := time.Date(2031, 6, 7, 8, 9, 10, 0, time.UTC)
	if err := SetCallerBudget(ctx, tx, id, quota, budget, expires); err != nil {
		t.Fatalf("SetCallerBudget() error = %v", err)
	}

	got := getCaller(t, tx, id)
	if got.Quota != quota {
		t.Errorf("quota = %+v, want %+v", got.Quota, quota)
	}
	if got.CostBudgetTotal != budget.String() {
		t.Errorf("cost_budget_total = %s, want %s", got.CostBudgetTotal, budget)
	}
	if !got.BudgetExpiresAt.Equal(expires) {
		t.Errorf("budget_expires_at = %v, want %v", got.BudgetExpiresAt, expires)
	}

	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		t.Fatalf("select now: %v", err)
	}
	if !got.BudgetSetAt.Equal(now) {
		t.Errorf("budget_set_at = %v, want %v", got.BudgetSetAt, now)
	}
	if got.Role != string(c.Role) || got.PermittedEnv != string(c.PermittedEnv) {
		t.Errorf("role, permitted_env = %q, %q, want unchanged %q, %q", got.Role, got.PermittedEnv, c.Role, c.PermittedEnv)
	}
}

func TestSetCallerBudgetNotFound(t *testing.T) {
	tx := testTx(t)
	err := SetCallerBudget(context.Background(), tx, uuid.New(), provider.Resources{}, decimal.Zero, time.Now())
	if !errors.Is(err, ErrCallerNotFound) {
		t.Errorf("SetCallerBudget() error = %v, want ErrCallerNotFound", err)
	}
}

func TestSetCallerBudgetInvalid(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()

	id, err := CreateCaller(ctx, tx, newCaller())
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	err = SetCallerBudget(ctx, tx, id, provider.Resources{CPUCore: -1}, decimal.Zero, time.Now())
	if errors.Is(err, ErrCallerNotFound) {
		t.Fatalf("SetCallerBudget() error = %v, want check violation", err)
	}
	if got := pgCode(err); got != "23514" {
		t.Errorf("SetCallerBudget() error = %v, want check violation", err)
	}
}

func TestSetCallerGithub(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	c := newCaller()
	c.GithubLogin = nil
	c.GithubID = nil

	id, err := CreateCaller(ctx, tx, c)
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	if err := SetCallerGithub(ctx, tx, id, "octocat", "583231"); err != nil {
		t.Fatalf("SetCallerGithub() error = %v", err)
	}

	got := getCaller(t, tx, id)
	if got.GithubLogin == nil || *got.GithubLogin != "octocat" {
		t.Errorf("github_login = %v, want %q", got.GithubLogin, "octocat")
	}
	if got.GithubID == nil || *got.GithubID != "583231" {
		t.Errorf("github_id = %v, want %q", got.GithubID, "583231")
	}
	if got.Quota != c.Quota || got.Role != string(c.Role) {
		t.Errorf("quota, role = %+v, %q, want unchanged %+v, %q", got.Quota, got.Role, c.Quota, c.Role)
	}
}

func TestSetCallerGithubOverwrite(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()

	id, err := CreateCaller(ctx, tx, newCaller())
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	if err := SetCallerGithub(ctx, tx, id, "hubot", "999"); err != nil {
		t.Fatalf("SetCallerGithub() error = %v", err)
	}

	got := getCaller(t, tx, id)
	if got.GithubLogin == nil || *got.GithubLogin != "hubot" || got.GithubID == nil || *got.GithubID != "999" {
		t.Errorf("github_login, github_id = %v, %v, want %q, %q", got.GithubLogin, got.GithubID, "hubot", "999")
	}
}

func TestSetCallerGithubNotFound(t *testing.T) {
	tx := testTx(t)
	err := SetCallerGithub(context.Background(), tx, uuid.New(), "octocat", "583231")
	if !errors.Is(err, ErrCallerNotFound) {
		t.Errorf("SetCallerGithub() error = %v, want ErrCallerNotFound", err)
	}
}

func TestSetCallerGithubDuplicateID(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	c := newCaller()

	if _, err := CreateCaller(ctx, tx, c); err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	c.GithubLogin = nil
	c.GithubID = nil
	id, err := CreateCaller(ctx, tx, c)
	if err != nil {
		t.Fatalf("CreateCaller() second error = %v", err)
	}

	err = SetCallerGithub(ctx, tx, id, "octocat", "583231")
	if errors.Is(err, ErrCallerNotFound) {
		t.Fatalf("SetCallerGithub() error = %v, want unique violation", err)
	}
	if got := pgCode(err); got != "23505" {
		t.Errorf("SetCallerGithub() error = %v, want unique violation", err)
	}
	if want := "set caller github " + id.String() + ":"; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("SetCallerGithub() error = %v, want %q prefix", err, want)
	}
}
