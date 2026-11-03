package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

var ErrCallerNotFound = errors.New("caller not found")

type NewCaller struct {
	GithubLogin     *string
	GithubID        *string
	Role            Role
	PermittedEnv    EnvType
	Quota           provider.Resources
	CostBudget      decimal.Decimal
	BudgetExpiresAt time.Time
}

type CallerLimits struct {
	PermittedEnv  EnvType
	Quota         provider.Resources
	CostBudget    decimal.Decimal
	BudgetSetAt   time.Time
	BudgetExpired bool
}

type CallerBudget struct {
	Quota           provider.Resources
	CostBudget      decimal.Decimal
	BudgetSetAt     time.Time
	BudgetExpiresAt time.Time
}

func GetCallerBudget(ctx context.Context, q DBTX, id uuid.UUID) (CallerBudget, error) {
	var b CallerBudget
	err := q.QueryRow(ctx, `
	SELECT quota_cpu_cores, quota_ram_gb, quota_disk_gb,
			cost_budget, budget_set_at, budget_expires_at
		FROM callers WHERE id = $1`, id,
	).Scan(&b.Quota.CPUCore, &b.Quota.RAMGB, &b.Quota.DiskGB,
		&b.CostBudget, &b.BudgetSetAt, &b.BudgetExpiresAt)

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
	_, err := q.Exec(ctx, `
		INSERT INTO callers (
			id, github_login, github_id,
			quota_cpu_cores, quota_ram_gb, quota_disk_gb,
			cost_budget, budget_expires_at, role, permitted_env
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		id, c.GithubLogin, c.GithubID,
		c.Quota.CPUCore, c.Quota.RAMGB, c.Quota.DiskGB,
		c.CostBudget, c.BudgetExpiresAt, c.Role, c.PermittedEnv,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create caller: %w", err)
	}
	return id, nil
}

func SetCallerBudget(ctx context.Context, q DBTX, id uuid.UUID, quota provider.Resources, costBudget decimal.Decimal, expiresAt time.Time) error {
	tag, err := q.Exec(ctx, `
		UPDATE callers SET
			quota_cpu_cores   = $2,
			quota_ram_gb      = $3,
			quota_disk_gb     = $4,
			cost_budget       = $5,
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
		SELECT permitted_env, quota_cpu_cores, quota_ram_gb, quota_disk_gb,
			cost_budget, budget_set_at, budget_expires_at <= now()
		FROM callers WHERE id = $1
		FOR UPDATE`, id,
	).Scan(
		&c.PermittedEnv, &c.Quota.CPUCore, &c.Quota.RAMGB, &c.Quota.DiskGB,
		&c.CostBudget, &c.BudgetSetAt, &c.BudgetExpired,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return CallerLimits{}, fmt.Errorf("lock caller %s: %w", id, ErrCallerNotFound)
	}

	if err != nil {
		return CallerLimits{}, fmt.Errorf("lock caller %s: %w", id, err)
	}

	return c, nil
}
