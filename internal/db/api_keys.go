package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrAPIKeyNotFound = errors.New("api key not found")
	ErrAPIKeyInactive = errors.New("api key revoked or expired")
)

type AuthenticatedCaller struct {
	ID           uuid.UUID
	Role         Role
	PermittedEnv EnvType
	KeyID        uuid.UUID
	Session      bool
}

func (c AuthenticatedCaller) AgentKey() *uuid.UUID {
	if c.Role != RoleAgent || c.KeyID == uuid.Nil {
		return nil
	}
	id := c.KeyID
	return &id
}

type NewAPIKey struct {
	CallerID     uuid.UUID
	Name         string
	Hash         []byte
	Role         Role
	PermittedEnv *EnvType
	CostCap      *int64
	ExpiresAt    time.Time
}

type APIKey struct {
	ID           uuid.UUID  `json:"id"`
	CallerID     uuid.UUID  `json:"caller_id"`
	Name         string     `json:"name"`
	Role         Role       `json:"role"`
	PermittedEnv *EnvType   `json:"permitted_env"`
	CostCap      *int64     `json:"cost_cap"`
	CostSpent    int64      `json:"cost_spent"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	RevokedAt    *time.Time `json:"revoked_at"`
}

const keySpent = `COALESCE((SELECT sum(COALESCE(e.charged_cost, e.estimated_cost)) FROM environments e WHERE e.api_key_id = k.id), 0)`

const apiKeyColumns = `k.id, k.caller_id, k.name, k.role, k.permitted_env, k.cost_cap, ` + keySpent + `,
	k.created_at, k.expires_at, k.last_used_at, k.revoked_at`

func scanAPIKey(row pgx.Row) (APIKey, error) {
	var k APIKey
	err := row.Scan(&k.ID, &k.CallerID, &k.Name, &k.Role, &k.PermittedEnv, &k.CostCap, &k.CostSpent,
		&k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt, &k.RevokedAt)
	return k, err
}

func CreateAPIKey(ctx context.Context, q DBTX, k NewAPIKey) (APIKey, error) {
	if k.Role == "" {
		k.Role = RoleUser
	}

	created, err := scanAPIKey(q.QueryRow(ctx, `
		WITH k AS (
			INSERT INTO api_keys (id, caller_id, name, api_key_hash, role, permitted_env, cost_cap, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING *
		)
		SELECT `+apiKeyColumns+` FROM k`,
		uuid.New(), k.CallerID, k.Name, k.Hash, k.Role, k.PermittedEnv, k.CostCap, k.ExpiresAt,
	))
	if err != nil {
		return APIKey{}, fmt.Errorf("create api key: %w", err)
	}
	return created, nil
}

func ListAPIKeys(ctx context.Context, q DBTX, callerID uuid.UUID) ([]APIKey, error) {
	rows, err := q.Query(ctx, `
		SELECT `+apiKeyColumns+` FROM api_keys k
		WHERE k.caller_id = $1
		ORDER BY k.created_at DESC, k.id DESC`, callerID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}

	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (APIKey, error) {
		return scanAPIKey(row)
	})
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	return keys, nil
}

func CallerByAPIKey(ctx context.Context, q DBTX, hash []byte) (AuthenticatedCaller, error) {
	var c AuthenticatedCaller
	err := q.QueryRow(ctx, `
		WITH k AS (
			SELECT k.id, k.caller_id, k.role, k.permitted_env, k.last_used_at
			FROM api_keys k
			WHERE k.api_key_hash = $1
				AND k.revoked_at IS NULL
				AND k.expires_at > now()
		), touched AS (
			UPDATE api_keys SET last_used_at = now()
			FROM k
			WHERE api_keys.id = k.id
				AND (k.last_used_at IS NULL OR k.last_used_at < now() - interval '1 minute')
		)
		SELECT c.id, k.role, LEAST(c.permitted_env, COALESCE(k.permitted_env, c.permitted_env)), k.id
		FROM k
		JOIN callers c ON c.id = k.caller_id`,
		hash,
	).Scan(&c.ID, &c.Role, &c.PermittedEnv, &c.KeyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthenticatedCaller{}, ErrAPIKeyNotFound
	}
	if err != nil {
		return AuthenticatedCaller{}, fmt.Errorf("caller by api key: %w", err)
	}
	return c, nil
}

type KeyLimits struct {
	Role         Role
	PermittedEnv *EnvType
	CostCap      *int64
	CostSpent    int64
}

func LockAPIKey(ctx context.Context, q DBTX, callerID, id uuid.UUID) (KeyLimits, error) {
	var l KeyLimits
	var active bool
	err := q.QueryRow(ctx, `
		SELECT k.role, k.permitted_env, k.cost_cap, `+keySpent+`,
			k.revoked_at IS NULL AND k.expires_at > now()
		FROM api_keys k
		WHERE k.id = $1 AND k.caller_id = $2
		FOR UPDATE`, id, callerID,
	).Scan(&l.Role, &l.PermittedEnv, &l.CostCap, &l.CostSpent, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return KeyLimits{}, fmt.Errorf("lock api key %s: %w", id, ErrAPIKeyNotFound)
	}
	if err != nil {
		return KeyLimits{}, fmt.Errorf("lock api key %s: %w", id, err)
	}
	if !active {
		return KeyLimits{}, fmt.Errorf("lock api key %s: %w", id, ErrAPIKeyInactive)
	}
	return l, nil
}

func RevokeAPIKey(ctx context.Context, q DBTX, id, callerID uuid.UUID) error {
	tag, err := q.Exec(ctx, `
		UPDATE api_keys SET revoked_at = COALESCE(revoked_at, now())
		WHERE id = $1 AND ($2::uuid IS NULL OR caller_id = $2)`,
		id, owner(callerID),
	)
	if err != nil {
		return fmt.Errorf("revoke api key %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("revoke api key %s: %w", id, ErrAPIKeyNotFound)
	}
	return nil
}

func owner(callerID uuid.UUID) *uuid.UUID {
	if callerID == uuid.Nil {
		return nil
	}
	return &callerID
}
