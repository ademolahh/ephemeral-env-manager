package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

var (
	ErrCallerNotFound = errors.New("caller not found")
	ErrGithubIDTaken  = errors.New("github id already linked to a caller")
)

type NewCaller struct {
	GithubLogin     *string
	GithubID        *string
	Role            Role
	PermittedEnv    EnvType
	Quota           provider.Resources
	CostBudgetTotal int64
	BudgetExpiresAt time.Time
}

type CallerLimits struct {
	Role            Role
	PermittedEnv    EnvType
	Quota           provider.Resources
	CostBudgetTotal int64
	CostUsed        int64
	ProdBugCost     int64
	ProdBugCostCap  int64
	BudgetSetAt     time.Time
	BudgetExpired   bool
}

type CallerBudget struct {
	Quota           provider.Resources
	CostBudgetTotal int64
	CostUsed        int64
	ProdBugCost     int64
	ProdBugCostCap  int64
	BudgetSetAt     time.Time
	BudgetExpiresAt time.Time
}

func GetCallerBudget(ctx context.Context, q DBTX, id uuid.UUID) (CallerBudget, error) {
	var b CallerBudget
	err := q.QueryRow(ctx, `
	SELECT quota_cpu_cores, quota_ram_gb, quota_disk_gb,
			cost_budget_total, cost_used, prod_bug_cost, prod_bug_cost_cap, budget_set_at, budget_expires_at
		FROM callers WHERE id = $1`, id,
	).Scan(&b.Quota.CPUCore, &b.Quota.RAMGB, &b.Quota.DiskGB,
		&b.CostBudgetTotal, &b.CostUsed, &b.ProdBugCost, &b.ProdBugCostCap, &b.BudgetSetAt, &b.BudgetExpiresAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return CallerBudget{}, fmt.Errorf("get caller budget %s: %w", id, ErrCallerNotFound)
	}

	if err != nil {
		return CallerBudget{}, fmt.Errorf("get caller budget %s: %w", id, err)
	}
	return b, nil
}

func CreateCaller(ctx context.Context, q DBTX, c NewCaller) (uuid.UUID, error) {
	id := uuid.New()
	tag, err := q.Exec(ctx, `
		INSERT INTO callers (
			id, github_login, github_id,
			quota_cpu_cores, quota_ram_gb, quota_disk_gb,
			cost_budget_total, budget_expires_at, role, permitted_env
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (github_id) DO NOTHING`,
		id, c.GithubLogin, c.GithubID,
		c.Quota.CPUCore, c.Quota.RAMGB, c.Quota.DiskGB,
		c.CostBudgetTotal, c.BudgetExpiresAt, c.Role, c.PermittedEnv,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create caller: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return uuid.Nil, fmt.Errorf("create caller: %w", ErrGithubIDTaken)
	}
	return id, nil
}

func SetCallerBudget(ctx context.Context, q DBTX, id uuid.UUID, quota provider.Resources, costBudget int64, expiresAt time.Time) error {
	tag, err := q.Exec(ctx, `
		UPDATE callers SET
			quota_cpu_cores   = $2,
			quota_ram_gb      = $3,
			quota_disk_gb     = $4,
			cost_budget_total = $5,
			budget_expires_at = $6,
			budget_set_at     = now()
		WHERE id = $1`,
		id, quota.CPUCore, quota.RAMGB, quota.DiskGB, costBudget, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("set caller budget %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set caller budget %s: %w", id, ErrCallerNotFound)
	}
	return nil
}

func SetCallerPermission(ctx context.Context, q DBTX, id uuid.UUID, permitted EnvType, prodBugCostCap int64) error {
	tag, err := q.Exec(ctx, `
		UPDATE callers SET
			permitted_env     = $2,
			prod_bug_cost_cap = $3
		WHERE id = $1`,
		id, permitted, prodBugCostCap,
	)
	if err != nil {
		return fmt.Errorf("set caller permission %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set caller permission %s: %w", id, ErrCallerNotFound)
	}
	return nil
}

func SetCallerGithub(ctx context.Context, q DBTX, id uuid.UUID, githubLogin, githubID string) error {
	tag, err := q.Exec(ctx, `
		UPDATE callers SET
			github_login = $2,
			github_id    = $3
		WHERE id = $1`,
		id, githubLogin, githubID,
	)
	if err != nil {
		return fmt.Errorf("set caller github %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set caller github %s: %w", id, ErrCallerNotFound)
	}
	return nil
}

func LockCaller(ctx context.Context, q DBTX, id uuid.UUID) (CallerLimits, error) {
	var c CallerLimits
	err := q.QueryRow(ctx, `
		SELECT role, permitted_env, quota_cpu_cores, quota_ram_gb, quota_disk_gb,
			cost_budget_total, cost_used, prod_bug_cost, prod_bug_cost_cap,
			budget_set_at, budget_expires_at <= now()
		FROM callers WHERE id = $1
		FOR UPDATE`, id,
	).Scan(
		&c.Role, &c.PermittedEnv, &c.Quota.CPUCore, &c.Quota.RAMGB, &c.Quota.DiskGB,
		&c.CostBudgetTotal, &c.CostUsed, &c.ProdBugCost, &c.ProdBugCostCap,
		&c.BudgetSetAt, &c.BudgetExpired,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return CallerLimits{}, fmt.Errorf("lock caller %s: %w", id, ErrCallerNotFound)
	}

	if err != nil {
		return CallerLimits{}, fmt.Errorf("lock caller %s: %w", id, err)
	}

	return c, nil
}
