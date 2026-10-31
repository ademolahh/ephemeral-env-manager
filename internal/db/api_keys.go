package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrAPIKeyNotFound = errors.New("api key not found")

type AuthenticatedCaller struct {
	ID           uuid.UUID
	Role         Role
	PermittedEnv EnvType
}

func CreateAPIKey(ctx context.Context, q DBTX, callerID uuid.UUID, hash []byte, expiresAt time.Time) (uuid.UUID, error) {
	id := uuid.New()
	_, err := q.Exec(ctx, `
		INSERT INTO api_keys (id, api_key_hash, caller_id, expires_at)
		VALUES ($1, $2, $3, $4)`,
		id, hash, callerID, expiresAt,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create api key: %w", err)
	}
	return id, nil
}

func CallerByAPIKey(ctx context.Context, q DBTX, hash []byte) (AuthenticatedCaller, error) {
	var c AuthenticatedCaller
	err := q.QueryRow(ctx, `
		SELECT c.id, c.role, c.permitted_env
		FROM api_keys k
		JOIN callers c ON c.id = k.caller_id
		WHERE k.api_key_hash = $1
			AND k.valid
			AND k.expires_at > now()`,
		hash,
	).Scan(&c.ID, &c.Role, &c.PermittedEnv)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthenticatedCaller{}, ErrAPIKeyNotFound
	}
	if err != nil {
		return AuthenticatedCaller{}, fmt.Errorf("caller by api key: %w", err)
	}
	return c, nil
}

func RevokeAPIKey(ctx context.Context, q DBTX, id uuid.UUID) error {
	tag, err := q.Exec(ctx, `UPDATE api_keys SET valid = false WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("revoke api key %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("revoke api key %s: %w", id, ErrAPIKeyNotFound)
	}
	return nil
}
