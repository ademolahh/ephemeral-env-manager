package env

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync"
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

const statusTimeout = 5 * time.Second

var envTypes = []db.EnvType{db.EnvRegularTesting, db.EnvPRReview, db.EnvProdBug}

type Manager struct {
	db        db.DBTX
	providers map[provider.Type]provider.Provider
	wg        sync.WaitGroup
}

func NewManager(q db.DBTX, providers map[provider.Type]provider.Provider) *Manager {
	return &Manager{db: q, providers: providers}
}

type CreateRequest struct {
	CallerID       uuid.UUID
	Type           db.EnvType
	Provider       provider.Type
	Workload       string
	Template       provider.Template
	IdempotencyKey string
}

func (m *Manager) Create(ctx context.Context, req CreateRequest) (db.Environment, error) {
	pt := req.Provider
	if pt == "" {
		pt = req.Template.DefaultProvider
	}

	if pt == "" {
		return db.Environment{}, fmt.Errorf("create environment: %w", ErrNoProvider)
	}

	if len(req.Template.Providers) > 0 && !slices.Contains(req.Template.Providers, pt) {
		return db.Environment{}, fmt.Errorf("create environment on %s: %w", pt, ErrProviderNotAllowed)
	}

	p, ok := m.providers[pt]
	if !ok {
		return db.Environment{}, fmt.Errorf("create environment on %s: %w", pt, ErrProviderUnavailable)
	}

	var r reservation
	err := pgx.BeginFunc(ctx, m.db, func(tx pgx.Tx) error {
		var err error
		r, err = reserve(ctx, tx, pt, req)
		return err
	})

	if err != nil {
		return db.Environment{}, fmt.Errorf("create environment: %w", err)
	}

	bg := context.WithoutCancel(ctx)
	for _, id := range r.preempted {
		m.wg.Go(func() { m.teardown(bg, p, id) })
	}

	if r.created {
		m.wg.Go(func() { m.provision(bg, p, r.env.ID, req) })
	}

	return r.env, nil
}

func (m *Manager) Get(ctx context.Context, caller db.AuthenticatedCaller, id uuid.UUID) (db.Environment, provider.Status, error) {
	e, err := m.owned(ctx, caller, id)
	if err != nil {
		return db.Environment{}, "", fmt.Errorf("get environment: %w", err)
	}

	if e.Status != db.EnvProvisioning && e.Status != db.EnvRunning {
		return e, "", nil
	}

	p, ok := m.providers[e.Provider]
	if !ok {
		return e, provider.StatusUnknown, nil
	}

	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()

	status, err := p.Status(ctx, e.ID.String())
	if err != nil {
		log.Printf("status of environment %s: %v", e.ID, err)
		return e, provider.StatusUnknown, nil
	}

	return e, status, nil
}

func (m *Manager) Delete(ctx context.Context, caller db.AuthenticatedCaller, id uuid.UUID) (db.Environment, error) {
	e, err := m.owned(ctx, caller, id)
	if err != nil {
		return db.Environment{}, fmt.Errorf("delete environment: %w", err)
	}

	return m.Remove(ctx, e)
}

func (m *Manager) owned(ctx context.Context, caller db.AuthenticatedCaller, id uuid.UUID) (db.Environment, error) {
	e, err := db.GetEnvironment(ctx, m.db, id)
	if err != nil {
		return db.Environment{}, err
	}

	if e.CallerID != caller.ID && caller.Role != db.RoleAdmin {
		return db.Environment{}, fmt.Errorf("environment %s: %w", id, db.ErrEnvNotFound)
	}

	return e, nil
}

func (m *Manager) Remove(ctx context.Context, e db.Environment) (db.Environment, error) {
	p, ok := m.providers[e.Provider]
	if !ok {
		return db.Environment{}, fmt.Errorf("delete environment on %s: %w", e.Provider, ErrProviderUnavailable)
	}

	if e.Status == db.EnvProvisioning || e.Status == db.EnvRunning {
		err := db.MarkEnvironmentDeleting(ctx, m.db, e.ID)
		if err != nil && !errors.Is(err, db.ErrInvalidTransition) {
			return db.Environment{}, fmt.Errorf("delete environment: %w", err)
		}

		if e, err = db.GetEnvironment(ctx, m.db, e.ID); err != nil {
			return db.Environment{}, fmt.Errorf("delete environment: %w", err)
		}
	}

	if e.Status == db.EnvDeleting {
		bg := context.WithoutCancel(ctx)
		m.wg.Go(func() { m.teardown(bg, p, e.ID) })
	}

	return e, nil
}

func (m *Manager) Wait() {
	m.wg.Wait()
}

func (m *Manager) provision(ctx context.Context, p provider.Provider, id uuid.UUID, req CreateRequest) {
	defer func() {
		if r := recover(); r != nil {
			m.fail(ctx, id, fmt.Sprintf("provider panic: %v", r))
		}
	}()

	if _, err := p.Create(ctx, id.String(), req.Workload, req.Template); err != nil {
		m.fail(ctx, id, err.Error())
		return
	}

	err := db.MarkEnvironmentRunning(ctx, m.db, id)
	if errors.Is(err, db.ErrInvalidTransition) {
		if err := p.Delete(ctx, id.String()); err != nil {
			log.Printf("remove environment %s deleted while provisioning: %v", id, err)
		}
		return
	}

	if err != nil {
		log.Printf("provision environment %s: %v", id, err)
	}
}

func (m *Manager) teardown(ctx context.Context, p provider.Provider, id uuid.UUID) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("delete environment %s: provider panic: %v", id, r)
		}
	}()

	if err := p.Delete(ctx, id.String()); err != nil {
		log.Printf("delete environment %s: %v", id, err)
		return
	}

	err := db.MarkEnvironmentDeleted(ctx, m.db, id)
	if err != nil && !errors.Is(err, db.ErrInvalidTransition) {
		log.Printf("mark environment %s deleted: %v", id, err)
	}
}

func (m *Manager) fail(ctx context.Context, id uuid.UUID, reason string) {
	log.Printf("provision environment %s: %s", id, reason)
	if err := db.MarkEnvironmentFailed(ctx, m.db, id, reason); err != nil {
		log.Printf("mark environment %s failed: %v", id, err)
	}
}

type reservation struct {
	env       db.Environment
	created   bool
	preempted []uuid.UUID
}

func reserve(ctx context.Context, tx pgx.Tx, pt provider.Type, req CreateRequest) (reservation, error) {
	rank := priority(req.Type)
	if rank == 0 {
		return reservation{}, fmt.Errorf("%w: %q", ErrInvalidEnvType, req.Type)
	}

	caller, err := db.LockCaller(ctx, tx, req.CallerID)
	if err != nil {
		return reservation{}, err
	}

	var key *string
	if req.IdempotencyKey != "" {
		key = &req.IdempotencyKey
		existing, err := db.EnvironmentByIdempotencyKey(ctx, tx, req.CallerID, req.IdempotencyKey)
		if err == nil {
			return reservation{env: existing}, nil
		}

		if !errors.Is(err, db.ErrEnvNotFound) {
			return reservation{}, err
		}
	}

	if rank > priority(caller.PermittedEnv) {
		return reservation{}, fmt.Errorf("%s with permission %s: %w", req.Type, caller.PermittedEnv, ErrNotPermitted)
	}

	need := req.Template.TotalResources()
	usage, err := db.CallerUsage(ctx, tx, req.CallerID)
	if err != nil {
		return reservation{}, err
	}

	if !fits(add(usage, need), caller.Quota) {
		return reservation{}, fmt.Errorf("using %+v, requesting %+v, quota %+v: %w", usage, need, caller.Quota, ErrQuotaExceeded)
	}

	prices, err := db.GetHourlyPrices(ctx, tx, pt)
	if err != nil {
		return reservation{}, err
	}

	rate := req.Template.HourlyCost(prices)
	if req.Type != db.EnvProdBug {
		if caller.BudgetExpired {
			return reservation{}, ErrBudgetExpired
		}

		spent, err := db.CallerSpend(ctx, tx, req.CallerID, caller.BudgetSetAt)
		if err != nil {
			return reservation{}, err
		}

		cost := estimatedCost(rate, req.Template.TTL)
		if spent.Add(cost).GreaterThan(caller.CostBudget) {
			return reservation{}, fmt.Errorf("spent %s, estimated %s, budget %s: %w", spent, cost, caller.CostBudget, ErrBudgetExceeded)
		}
	}

	preempted, err := makeRoom(ctx, tx, pt, req.Type, need)
	if err != nil {
		return reservation{}, err
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
		IdempotencyKey:  key,
	})
	if err != nil {
		return reservation{}, err
	}

	if err := db.ReserveCapacity(ctx, tx, pt, need); err != nil {
		return reservation{}, err
	}

	e, err := db.GetEnvironment(ctx, tx, id)
	if err != nil {
		return reservation{}, err
	}

	return reservation{env: e, created: true, preempted: preempted}, nil
}

func makeRoom(ctx context.Context, tx pgx.Tx, pt provider.Type, t db.EnvType, need provider.Resources) ([]uuid.UUID, error) {
	pool, err := db.LockCapacityPool(ctx, tx, pt)
	if err != nil {
		return nil, err
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
		return nil, nil
	}

	lower := lowerPriority(t)

	if len(lower) == 0 {
		return nil, fmt.Errorf("%s needs %+v, %+v free: %w", pt, need, free, ErrCapacityExceeded)
	}

	candidates, err := db.ListPreemptible(ctx, tx, pt, lower)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("%s needs %+v, %+v free after preemption: %w", pt, need, free, ErrCapacityExceeded)
	}

	for _, id := range victims {
		if err := db.MarkEnvironmentDeleting(ctx, tx, id); err != nil {
			return nil, fmt.Errorf("preempt %s: %w", id, err)
		}
	}

	return victims, nil
}

func priority(t db.EnvType) int {
	return slices.Index(envTypes, t) + 1
}

func lowerPriority(t db.EnvType) []db.EnvType {
	var lower []db.EnvType
	for _, et := range envTypes {
		if priority(et) < priority(t) {
			lower = append(lower, et)
		}
	}
	return lower
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
