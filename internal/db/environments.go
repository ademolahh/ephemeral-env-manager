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

var (
	ErrEnvNotFound       = errors.New("environment not found")
	ErrInvalidTransition = errors.New("invalid environment status transition")
)

type Environment struct {
	ID              uuid.UUID          `json:"id"`
	CreatedAt       time.Time          `json:"created_at"`
	ExpiresAt       time.Time          `json:"expires_at"`
	Type            EnvType            `json:"type"`
	Status          EnvStatus          `json:"status"`
	DeletedAt       *time.Time         `json:"-"`
	Provider        provider.Type      `json:"provider"`
	Endpoint        *string            `json:"endpoint"`
	Resources       provider.Resources `json:"resources"`
	HourlyRate      decimal.Decimal    `json:"hourly_rate"`
	CallerID        uuid.UUID          `json:"-"`
	FailureReason   *string            `json:"failure_reason,omitempty"`
	TemplateName    string             `json:"template_name"`
	TemplateVersion string             `json:"template_version"`
	InstanceStatus  provider.Status    `json:"instance_status,omitempty"`
}

type NewEnvironment struct {
	CallerID        uuid.UUID
	Type            EnvType
	Provider        provider.Type
	Resources       provider.Resources
	HourlyRate      decimal.Decimal
	TTL             time.Duration
	TemplateName    string
	TemplateVersion string
	IdempotencyKey  *string
	EstimatedCost   decimal.Decimal
}

const environmentColumns = `
	id, created_at, expires_at, type, status, deleted_at, provider, endpoint,
	cpu_cores, ram_gb, disk_gb, hourly_rate, caller_id, failure_reason,
	template_name, template_version`

func scanEnvironment(row pgx.Row) (Environment, error) {
	var e Environment
	err := row.Scan(
		&e.ID, &e.CreatedAt, &e.ExpiresAt, &e.Type, &e.Status, &e.DeletedAt, &e.Provider, &e.Endpoint,
		&e.Resources.CPUCore, &e.Resources.RAMGB, &e.Resources.DiskGB, &e.HourlyRate, &e.CallerID, &e.FailureReason,
		&e.TemplateName, &e.TemplateVersion,
	)
	return e, err
}

func CreateEnvironment(ctx context.Context, q DBTX, e NewEnvironment) (uuid.UUID, error) {
	id := uuid.New()
	tx, err := q.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create environment: begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO environments (
			id, expires_at, type, provider,
			cpu_cores, ram_gb, disk_gb, hourly_rate,
			caller_id, template_name, template_version, idempotency_key,
			estimated_cost, reserved_cost
		)
		VALUES ($1, now() + make_interval(secs => $2), $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			$13::numeric, CASE WHEN $3::env_type = 'PROD_BUG' THEN 0 ELSE $13::numeric END)`,
		id, e.TTL.Seconds(), e.Type, e.Provider,
		e.Resources.CPUCore, e.Resources.RAMGB, e.Resources.DiskGB, e.HourlyRate,
		e.CallerID, e.TemplateName, e.TemplateVersion, e.IdempotencyKey, e.EstimatedCost,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create environment: %w", err)
	}

	charge := `UPDATE callers SET cost_used = cost_used + $2 WHERE id = $1`
	if e.Type == EnvProdBug {
		charge = `UPDATE callers SET prod_bug_cost = prod_bug_cost + $2 WHERE id = $1`
	}

	if _, err := tx.Exec(ctx, charge, e.CallerID, e.EstimatedCost); err != nil {
		return uuid.Nil, fmt.Errorf("create environment: charge caller: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("create environment: commit transaction: %w", err)
	}

	return id, nil
}

func GetEnvironment(ctx context.Context, q DBTX, id uuid.UUID) (Environment, error) {
	e, err := scanEnvironment(q.QueryRow(ctx, `SELECT `+environmentColumns+` FROM environments WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, fmt.Errorf("get environment %s: %w", id, ErrEnvNotFound)
	}

	if err != nil {
		return Environment{}, fmt.Errorf("get environment %s: %w", id, err)
	}

	return e, nil
}

func EnvironmentByIdempotencyKey(ctx context.Context, q DBTX, callerID uuid.UUID, key string) (Environment, error) {
	e, err := scanEnvironment(q.QueryRow(ctx, `
		SELECT `+environmentColumns+` FROM environments
		WHERE caller_id = $1 AND idempotency_key = $2`, callerID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, fmt.Errorf("environment by idempotency key: %w", ErrEnvNotFound)
	}

	if err != nil {
		return Environment{}, fmt.Errorf("environment by idempotency key: %w", err)
	}

	return e, nil
}

func MarkEnvironmentRunning(ctx context.Context, q DBTX, id uuid.UUID) error {
	return setStatus(ctx, q, id, EnvRunning, nil, false, false, EnvProvisioning)
}

func MarkEnvironmentFailed(ctx context.Context, q DBTX, id uuid.UUID, reason string) error {
	return setStatus(ctx, q, id, EnvFailed, &reason, true, true, EnvProvisioning, EnvRunning)
}

func MarkEnvironmentDeleting(ctx context.Context, q DBTX, id uuid.UUID) error {
	return setStatus(ctx, q, id, EnvDeleting, nil, true, false, EnvProvisioning, EnvRunning)
}

func MarkEnvironmentDeleted(ctx context.Context, q DBTX, id uuid.UUID) error {
	return setStatus(ctx, q, id, EnvDeleted, nil, false, true, EnvDeleting)
}

func setStatus(ctx context.Context, q DBTX, id uuid.UUID, to EnvStatus, reason *string, release, refund bool, from ...EnvStatus) error {
	if !release && !refund {
		return updateStatus(ctx, q, id, to, reason, false, false, from)
	}

	return pgx.BeginFunc(ctx, q, func(tx pgx.Tx) error {
		if refund {
			if _, err := tx.Exec(ctx, `
				SELECT 1 FROM callers
				WHERE id = (SELECT caller_id FROM environments WHERE id = $1)
				FOR UPDATE`, id); err != nil {
				return fmt.Errorf("set environment %s to %s: lock caller: %w", id, to, err)
			}
		}

		if release {
			if _, err := tx.Exec(ctx, `
				SELECT 1 FROM capacity_pools
				WHERE provider = (SELECT provider FROM environments WHERE id = $1)
				FOR UPDATE`, id); err != nil {
				return fmt.Errorf("set environment %s to %s: lock capacity pool: %w", id, to, err)
			}
		}

		return updateStatus(ctx, tx, id, to, reason, release, refund, from)
	})
}

func updateStatus(ctx context.Context, q DBTX, id uuid.UUID, to EnvStatus, reason *string, release, refund bool, from []EnvStatus) error {
	allowed := make([]string, len(from))
	for i, s := range from {
		allowed[i] = string(s)
	}

	var updated int
	err := q.QueryRow(ctx, `
		WITH env AS (
			UPDATE environments SET
				status         = $2::env_status,
				failure_reason = COALESCE($3, failure_reason),
				deleted_at     = CASE WHEN $2::env_status = 'DELETED' THEN now() ELSE deleted_at END,
				charged_cost   = CASE WHEN $6 THEN LEAST(estimated_cost,
					hourly_rate * GREATEST(0, extract(epoch FROM LEAST(now(), expires_at) - created_at)) / 3600)
					ELSE charged_cost END
			WHERE id = $1 AND status::text = ANY($4)
			RETURNING provider, cpu_cores, ram_gb, disk_gb, caller_id, type, estimated_cost, reserved_cost, charged_cost
		), settled AS (
			UPDATE callers c SET
				cost_used     = GREATEST(0, c.cost_used - GREATEST(0, env.reserved_cost - env.charged_cost)),
				prod_bug_cost = CASE WHEN env.type = 'PROD_BUG'
					THEN GREATEST(0, c.prod_bug_cost - (env.estimated_cost - env.charged_cost))
					ELSE c.prod_bug_cost END
			FROM env
			WHERE $6 AND c.id = env.caller_id
		), released AS (
			UPDATE capacity_pools p SET
				available_cpu_cores = LEAST(p.default_cpu_cores, p.available_cpu_cores + env.cpu_cores),
				available_ram_gb    = LEAST(p.default_ram_gb, p.available_ram_gb + env.ram_gb),
				available_disk_gb   = LEAST(p.default_disk_gb, p.available_disk_gb + env.disk_gb)
			FROM env
			WHERE $5 AND p.provider = env.provider
		)
		SELECT count(*) FROM env`,
		id, to, reason, allowed, release, refund,
	).Scan(&updated)

	if err != nil {
		return fmt.Errorf("set environment %s to %s: %w", id, to, err)
	}

	if updated > 0 {
		return nil
	}

	var current EnvStatus
	err = q.QueryRow(ctx, `SELECT status FROM environments WHERE id = $1`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("set environment %s to %s: %w", id, to, ErrEnvNotFound)
	}

	if err != nil {
		return fmt.Errorf("set environment %s to %s: %w", id, to, err)
	}

	return fmt.Errorf("set environment %s from %s to %s: %w", id, current, to, ErrInvalidTransition)
}

func ListExpiredEnvironments(ctx context.Context, q DBTX) ([]Environment, error) {
	return listEnvironments(ctx, q, `
		WHERE status IN ('PROVISIONING', 'RUNNING') AND expires_at <= now()
		ORDER BY expires_at`)
}

func ListStuckProvisioning(ctx context.Context, q DBTX, olderThan time.Duration) ([]Environment, error) {
	return listEnvironments(ctx, q, `
		WHERE status = 'PROVISIONING' AND created_at <= now() - make_interval(secs => $1)
		ORDER BY created_at`, olderThan.Seconds())
}

func ListDeletingEnvironments(ctx context.Context, q DBTX) ([]Environment, error) {
	return listEnvironments(ctx, q, `WHERE status = 'DELETING' ORDER BY created_at`)
}

func ListCallerEnvironments(ctx context.Context, q DBTX, callerID uuid.UUID) ([]Environment, error) {
	return listEnvironments(ctx, q, `WHERE caller_id = $1 ORDER BY created_at DESC`, callerID)
}

func ListActiveEnvironments(ctx context.Context, q DBTX) ([]Environment, error) {
	return listEnvironments(ctx, q, `WHERE status IN ('PROVISIONING', 'RUNNING')`)
}

func listEnvironments(ctx context.Context, q DBTX, where string, args ...any) ([]Environment, error) {
	rows, err := q.Query(ctx, `SELECT `+environmentColumns+` FROM environments `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}

	envs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Environment, error) {
		return scanEnvironment(row)
	})

	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}

	return envs, nil
}

func ListPreemptible(ctx context.Context, q DBTX, p provider.Type, types []EnvType) ([]Environment, error) {
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = string(t)
	}

	return listEnvironments(ctx, q, `
		WHERE provider = $1 AND status IN ('PROVISIONING', 'RUNNING') AND type::text = ANY($2)
		ORDER BY created_at DESC
		FOR UPDATE`, p, names)
}

func CallerUsage(ctx context.Context, q DBTX, callerID uuid.UUID) (provider.Resources, error) {
	var r provider.Resources
	err := q.QueryRow(ctx, `
		SELECT COALESCE(sum(cpu_cores), 0), COALESCE(sum(ram_gb), 0), COALESCE(sum(disk_gb), 0)
		FROM environments
		WHERE caller_id = $1 AND status IN ('PROVISIONING', 'RUNNING')`, callerID,
	).Scan(&r.CPUCore, &r.RAMGB, &r.DiskGB)

	if err != nil {
		return provider.Resources{}, fmt.Errorf("caller usage %s: %w", callerID, err)
	}

	return r, nil
}
