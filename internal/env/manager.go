package env

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

var (
	ErrNoProvider          = errors.New("no provider requested and template has no default")
	ErrProviderNotAllowed  = errors.New("provider not allowed by template")
	ErrProviderUnavailable = errors.New("provider not configured")
	ErrInvalidEnvType      = errors.New("invalid environment type")
	ErrNotPermitted        = errors.New("environment type not permitted for caller")
	ErrQuotaExceeded       = errors.New("quota exceeded")
	ErrBudgetExpired       = errors.New("cost budget expired")
	ErrBudgetExceeded      = errors.New("cost budget exceeded")
	ErrCapacityExceeded    = errors.New("capacity pool exhausted")
)

var envTypes = []db.EnvType{db.EnvRegularTesting, db.EnvPRReview, db.EnvProdBug}

type DB interface {
	db.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

type Manager struct {
	db        DB
	providers map[provider.Type]provider.Provider
}

func NewManager(q DB, providers map[provider.Type]provider.Provider) *Manager {
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

	var id uuid.UUID
	err := pgx.BeginFunc(ctx, m.db, func(tx pgx.Tx) error {
		var err error
		id, err = reserve(ctx, tx, pt, req)
		return err
	})

	if err != nil {
		return uuid.Nil, fmt.Errorf("create environment: %w", err)
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

func reserve(ctx context.Context, tx pgx.Tx, pt provider.Type, req CreateRequest) (uuid.UUID, error) {
	rank := priority(req.Type)
	if rank == 0 {
		return uuid.Nil, fmt.Errorf("%w: %q", ErrInvalidEnvType, req.Type)
	}

	caller, err := db.LockCaller(ctx, tx, req.CallerID)
	if err != nil {
		return uuid.Nil, err
	}

	if rank > priority(caller.PermittedEnv) {
		return uuid.Nil, fmt.Errorf("%s with permission %s: %w", req.Type, caller.PermittedEnv, ErrNotPermitted)
	}

	need := req.Template.TotalResources()
	usage, err := db.CallerUsage(ctx, tx, req.CallerID)
	if err != nil {
		return uuid.Nil, err
	}

	if !fits(add(usage, need), caller.Quota) {
		return uuid.Nil, fmt.Errorf("using %+v, requesting %+v, quota %+v: %w", usage, need, caller.Quota, ErrQuotaExceeded)
	}

	prices, err := db.GetHourlyPrices(ctx, tx, pt)
	if err != nil {
		return uuid.Nil, err
	}

	rate := req.Template.HourlyCost(prices)
	if req.Type != db.EnvProdBug {
		if caller.BudgetExpired {
			return uuid.Nil, ErrBudgetExpired
		}

		spent, err := db.CallerSpend(ctx, tx, req.CallerID, caller.BudgetSetAt)
		if err != nil {
			return uuid.Nil, err
		}

		cost := estimatedCost(rate, req.Template.TTL)
		if spent.Add(cost).GreaterThan(caller.CostBudget) {
			return uuid.Nil, fmt.Errorf("spent %s, estimated %s, budget %s: %w", spent, cost, caller.CostBudget, ErrBudgetExceeded)
		}
	}

	if err := makeRoom(ctx, tx, pt, req.Type, need); err != nil {
		return uuid.Nil, err
	}

	id, err := db.CreateEnvironment(ctx, tx, db.NewEnvironment{
		CallerID:        req.CallerID,
		Type:            req.Type,
		Provider:        pt,
		Resources:       need,
		HourlyRate:      rate,
		TTL:             req.Template.TTL,
		TemplateName:    req.Template.Name,
		TemplateVersion: req.Template.Version,
	})
	if err != nil {
		return uuid.Nil, err
	}

	if err := db.ReserveCapacity(ctx, tx, pt, need); err != nil {
		return uuid.Nil, err
	}

	return id, nil
}

func makeRoom(ctx context.Context, tx pgx.Tx, pt provider.Type, t db.EnvType, need provider.Resources) error {
	pool, err := db.LockCapacityPool(ctx, tx, pt)
	if err != nil {
		return err
	}

	limit := pool.Default
	if t != db.EnvProdBug {
		limit = provider.Resources{
			CPUCore: pool.Default.CPUCore * 9 / 10,
			RAMGB:   pool.Default.RAMGB * 9 / 10,
			DiskGB:  pool.Default.DiskGB * 9 / 10,
		}
	}

	free := sub(limit, sub(pool.Default, pool.Available))
	if fits(need, free) {
		return nil
	}

	var lower []db.EnvType
	for _, et := range envTypes {
		if priority(et) < priority(t) {
			lower = append(lower, et)
		}
	}

	if len(lower) == 0 {
		return fmt.Errorf("%s needs %+v, %+v free: %w", pt, need, free, ErrCapacityExceeded)
	}

	candidates, err := db.ListPreemptible(ctx, tx, pt, lower)
	if err != nil {
		return err
	}

	slices.SortStableFunc(candidates, func(a, b db.Environment) int {
		return priority(a.Type) - priority(b.Type)
	})

	var victims []uuid.UUID
	for _, c := range candidates {
		if fits(need, free) {
			break
		}

		if !relieves(c.Resources, sub(need, free)) {
			continue
		}

		victims = append(victims, c.ID)
		free = add(free, c.Resources)
	}

	if !fits(need, free) {
		return fmt.Errorf("%s needs %+v, %+v free after preemption: %w", pt, need, free, ErrCapacityExceeded)
	}

	for _, id := range victims {
		if err := db.MarkEnvironmentDeleting(ctx, tx, id); err != nil {
			return fmt.Errorf("preempt %s: %w", id, err)
		}
	}

	return nil
}

func priority(t db.EnvType) int {
	return slices.Index(envTypes, t) + 1
}

func estimatedCost(rate decimal.Decimal, ttl time.Duration) decimal.Decimal {
	return rate.Mul(decimal.NewFromInt(int64(ttl / time.Second))).Div(decimal.NewFromInt(3600))
}

func fits(need, free provider.Resources) bool {
	return need.CPUCore <= free.CPUCore && need.RAMGB <= free.RAMGB && need.DiskGB <= free.DiskGB
}

func relieves(r, deficit provider.Resources) bool {
	return (deficit.CPUCore > 0 && r.CPUCore > 0) ||
		(deficit.RAMGB > 0 && r.RAMGB > 0) ||
		(deficit.DiskGB > 0 && r.DiskGB > 0)
}

func add(a, b provider.Resources) provider.Resources {
	return provider.Resources{CPUCore: a.CPUCore + b.CPUCore, RAMGB: a.RAMGB + b.RAMGB, DiskGB: a.DiskGB + b.DiskGB}
}

func sub(a, b provider.Resources) provider.Resources {
	return provider.Resources{CPUCore: a.CPUCore - b.CPUCore, RAMGB: a.RAMGB - b.RAMGB, DiskGB: a.DiskGB - b.DiskGB}
}
