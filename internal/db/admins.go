package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrAdminNotFound = errors.New("admin not found")
	ErrAdminExists   = errors.New("admin username already taken")
)

type Admin struct {
	ID           uuid.UUID
	Username     string
	PasswordHash []byte
}

func CreateAdmin(ctx context.Context, q DBTX, username string, passwordHash []byte) (uuid.UUID, error) {
	id := uuid.New()
	tag, err := q.Exec(ctx, `
		INSERT INTO admins (id, username, password_hash)
		VALUES ($1, $2, $3)
		ON CONFLICT (username) DO NOTHING`,
		id, username, passwordHash,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create admin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return uuid.Nil, fmt.Errorf("create admin %s: %w", username, ErrAdminExists)
	}
	return id, nil
}

func AdminByUsername(ctx context.Context, q DBTX, username string) (Admin, error) {
	var a Admin
	err := q.QueryRow(ctx, `
		SELECT id, username, password_hash FROM admins WHERE username = $1`, username,
	).Scan(&a.ID, &a.Username, &a.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Admin{}, ErrAdminNotFound
	}
	if err != nil {
		return Admin{}, fmt.Errorf("admin by username: %w", err)
	}
	return a, nil
}
