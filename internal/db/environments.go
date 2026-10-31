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
	ID              uuid.UUID
	CreatedAt       time.Time
	ExpiresAt       time.Time
	Type            EnvType
	Status          EnvStatus
	DeletedAt       *time.Time
	Provider        provider.Type
	Endpoint        *string
	Resources       provider.Resources
	HourlyRate      decimal.Decimal
	CallerID        uuid.UUID
	FailureReason   *string
	TemplateName    string
	TemplateVersion string
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
	_, err := q.Exec(ctx, `
		INSERT INTO environments (
			id, expires_at, type, provider,
			cpu_cores, ram_gb, disk_gb, hourly_rate,
			caller_id, template_name, template_version
		)
		VALUES ($1, now() + make_interval(secs => $2), $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		id, e.TTL.Seconds(), e.Type, e.Provider,
		e.Resources.CPUCore, e.Resources.RAMGB, e.Resources.DiskGB, e.HourlyRate,
		e.CallerID, e.TemplateName, e.TemplateVersion,
	)

	if err != nil {
		return uuid.Nil, fmt.Errorf("create environment: %w", err)
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

func MarkEnvironmentRunning(ctx context.Context, q DBTX, id uuid.UUID) error {
	return setStatus(ctx, q, id, EnvRunning, nil, EnvProvisioning)
}

func MarkEnvironmentFailed(ctx context.Context, q DBTX, id uuid.UUID, reason string) error {
	return setStatus(ctx, q, id, EnvFailed, &reason, EnvProvisioning, EnvRunning)
}

func MarkEnvironmentDeleting(ctx context.Context, q DBTX, id uuid.UUID) error {
	return setStatus(ctx, q, id, EnvDeleting, nil, EnvProvisioning, EnvRunning)
}

func MarkEnvironmentDeleted(ctx context.Context, q DBTX, id uuid.UUID) error {
	return setStatus(ctx, q, id, EnvDeleted, nil, EnvDeleting)
}

func setStatus(ctx context.Context, q DBTX, id uuid.UUID, to EnvStatus, reason *string, from ...EnvStatus) error {
	allowed := make([]string, len(from))
	for i, s := range from {
		allowed[i] = string(s)
	}

	tag, err := q.Exec(ctx, `
	UPDATE environments SET
	status         = $2::env_status,
			failure_reason = COALESCE($3, failure_reason),
			deleted_at     = CASE WHEN $2::env_status = 'DELETED' THEN now() ELSE deleted_at END
		WHERE id = $1 AND status::text = ANY($4)`,
		id, to, reason, allowed,
	)

	if err != nil {
		return fmt.Errorf("set environment %s to %s: %w", id, to, err)
	}

	if tag.RowsAffected() > 0 {
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
