package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrTemplateNotFound = errors.New("template not found")
	ErrTemplateExists   = errors.New("template version already exists")
)

func CreateTemplate(ctx context.Context, q DBTX, name, version string, spec json.RawMessage) error {
	tag, err := q.Exec(ctx, `
		INSERT INTO templates (id, name, version, spec)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (name, version) DO NOTHING`,
		uuid.New(), name, version, spec,
	)
	if err != nil {
		return fmt.Errorf("create template %s@%s: %w", name, version, err)
	}

	if tag.RowsAffected() == 0 {
		return fmt.Errorf("create template %s@%s: %w", name, version, ErrTemplateExists)
	}

	return nil
}

func GetTemplate(ctx context.Context, q DBTX, name, version string) (json.RawMessage, error) {
	var spec json.RawMessage
	err := q.QueryRow(ctx, `SELECT spec FROM templates WHERE name = $1 AND version = $2`, name, version).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get template %s@%s: %w", name, version, ErrTemplateNotFound)
	}

	if err != nil {
		return nil, fmt.Errorf("get template %s@%s: %w", name, version, err)
	}

	return spec, nil
}

func ListTemplates(ctx context.Context, q DBTX) ([]json.RawMessage, error) {
	rows, err := q.Query(ctx, `SELECT spec FROM templates ORDER BY name, version`)
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}

	specs, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}

	return specs, nil
}
