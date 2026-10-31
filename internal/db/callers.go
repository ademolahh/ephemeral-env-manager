package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
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
