package env

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

var (
	ErrNoProvider          = errors.New("no provider requested and template has no default")
	ErrProviderNotAllowed  = errors.New("provider not allowed by template")
	ErrProviderUnavailable = errors.New("provider not configured")
)

type Manager struct {
	db        db.DBTX
	providers map[provider.Type]provider.Provider
}

func NewManager(q db.DBTX, providers map[provider.Type]provider.Provider) *Manager {
	return &Manager{db: q, providers: providers}
}

type CreateRequest struct {
	CallerID uuid.UUID
	Type     db.EnvType
	Provider provider.Type
	Workload string
	Template provider.Template
}

func (m *Manager) Create(ctx context.Context, req CreateRequest) (uuid.UUID, error) {
	pt := req.Provider
	if pt == "" {
		pt = req.Template.DefaultProvider
	}

	if pt == "" {
		return uuid.Nil, fmt.Errorf("create environment: %w", ErrNoProvider)
	}

	if len(req.Template.Providers) > 0 && !slices.Contains(req.Template.Providers, pt) {
		return uuid.Nil, fmt.Errorf("create environment on %s: %w", pt, ErrProviderNotAllowed)
	}

	p, ok := m.providers[pt]
	if !ok {
		return uuid.Nil, fmt.Errorf("create environment on %s: %w", pt, ErrProviderUnavailable)
	}

	prices, err := db.GetHourlyPrices(ctx, m.db, pt)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create environment: %w", err)
	}

	id, err := db.CreateEnvironment(ctx, m.db, db.NewEnvironment{
		CallerID:        req.CallerID,
		Type:            req.Type,
		Provider:        pt,
		Resources:       req.Template.TotalResources(),
		HourlyRate:      req.Template.HourlyCost(prices),
		TTL:             req.Template.TTL,
		TemplateName:    req.Template.Name,
		TemplateVersion: req.Template.Version,
	})
	if err != nil {
		return uuid.Nil, err
	}

	if _, err := p.Create(ctx, id.String(), req.Workload, req.Template); err != nil {
		provisionErr := fmt.Errorf("provision environment %s: %w", id, err)
		if markErr := db.MarkEnvironmentFailed(context.WithoutCancel(ctx), m.db, id, err.Error()); markErr != nil {
			provisionErr = errors.Join(provisionErr, markErr)
		}
		return id, provisionErr
	}

	if err := db.MarkEnvironmentRunning(context.WithoutCancel(ctx), m.db, id); err != nil {
		return id, err
	}

	return id, nil
}
