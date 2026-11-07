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
	"github.com/ademolahh/ephemeral-env-manager/internal/proxy"
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
	ErrNotRunning          = errors.New("environment is not running")
)

const (
	statusTimeout = 5 * time.Second
	execTimeout   = 5 * time.Minute
)

var envTypes = []db.EnvType{db.EnvRegularTesting, db.EnvPRReview, db.EnvProdBug}

type Manager struct {
	db        db.DBTX
	providers map[provider.Type]provider.Provider
	proxy     *proxy.Proxy
	wg        sync.WaitGroup
}

type Option func(*Manager)

func WithProxy(p *proxy.Proxy) Option {
	return func(m *Manager) { m.proxy = p }
}

func NewManager(q db.DBTX, providers map[provider.Type]provider.Provider, opts ...Option) *Manager {
	m := &Manager{db: q, providers: providers}
	for _, opt := range opts {
		opt(m)
	}

	return m
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
		r, err = reserve(ctx, tx, pt, m.proxy, req)
		return err
	})

	if err != nil {
		return db.Environment{}, fmt.Errorf("create environment: %w", err)
	}

	bg := context.WithoutCancel(ctx)
	for _, id := range r.preempted {
		m.wg.Go(func() {
			if err := m.Teardown(bg, db.Environment{ID: id, Provider: pt}); err != nil {
				log.Print(err)
			}
		})
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

	return m.withStatus(ctx, e)
}

func (m *Manager) GetAny(ctx context.Context, id uuid.UUID) (db.Environment, provider.Status, error) {
	e, err := db.GetEnvironment(ctx, m.db, id)
	if err != nil {
		return db.Environment{}, "", fmt.Errorf("get environment: %w", err)
	}

	return m.withStatus(ctx, e)
}

func (m *Manager) withStatus(ctx context.Context, e db.Environment) (db.Environment, provider.Status, error) {
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

func (m *Manager) DeleteAny(ctx context.Context, id uuid.UUID) (db.Environment, error) {
	e, err := db.GetEnvironment(ctx, m.db, id)
	if err != nil {
		return db.Environment{}, fmt.Errorf("delete environment: %w", err)
	}

	return m.Remove(ctx, e)
}

func (m *Manager) Exec(ctx context.Context, caller db.AuthenticatedCaller, id uuid.UUID, cmd []string) (string, error) {
	e, err := m.owned(ctx, caller, id)
	if err != nil {
		return "", fmt.Errorf("exec environment: %w", err)
	}

	return m.exec(ctx, e, cmd)
}

func (m *Manager) ExecAny(ctx context.Context, id uuid.UUID, cmd []string) (string, error) {
	e, err := db.GetEnvironment(ctx, m.db, id)
	if err != nil {
		return "", fmt.Errorf("exec environment: %w", err)
	}

	return m.exec(ctx, e, cmd)
}

func (m *Manager) exec(ctx context.Context, e db.Environment, cmd []string) (string, error) {
	if e.Status != db.EnvRunning {
		return "", fmt.Errorf("exec environment %s: %w", e.ID, ErrNotRunning)
	}

	p, ok := m.providers[e.Provider]
	if !ok {
		return "", fmt.Errorf("exec environment %s on %s: %w", e.ID, e.Provider, ErrProviderUnavailable)
	}

	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	return p.Exec(ctx, e.ID.String(), cmd)
}

func (m *Manager) owned(ctx context.Context, caller db.AuthenticatedCaller, id uuid.UUID) (db.Environment, error) {
	e, err := db.GetEnvironment(ctx, m.db, id)
	if err != nil {
		return db.Environment{}, err
	}

	if e.CallerID != caller.ID {
		return db.Environment{}, fmt.Errorf("environment %s: %w", id, db.ErrEnvNotFound)
	}

	return e, nil
}

func (m *Manager) HasProvider(t provider.Type) bool {
	_, ok := m.providers[t]
	return ok
}

func (m *Manager) Status(ctx context.Context, e db.Environment) (provider.Status, error) {
	p, ok := m.providers[e.Provider]
	if !ok {
		return "", fmt.Errorf("delete environment on %s: %w", e.Provider, ErrProviderUnavailable)
	}

	return p.Status(ctx, e.ID.String())
}

func (m *Manager) Remove(ctx context.Context, e db.Environment) (db.Environment, error) {
	e, err := m.MarkDeleting(ctx, e)
	if err != nil {
		return db.Environment{}, err
	}

	if e.Status == db.EnvDeleting {
		bg := context.WithoutCancel(ctx)
		m.wg.Go(func() {
			if err := m.Teardown(bg, e); err != nil {
				log.Print(err)
			}
		})
	}

	return e, nil
}

func (m *Manager) MarkDeleting(ctx context.Context, e db.Environment) (db.Environment, error) {
	if _, ok := m.providers[e.Provider]; !ok {
		return db.Environment{}, fmt.Errorf("delete environment on %s: %w", e.Provider, ErrProviderUnavailable)
	}

	if e.Status != db.EnvProvisioning && e.Status != db.EnvRunning {
		return e, nil
	}

	err := db.MarkEnvironmentDeleting(ctx, m.db, e.ID)
	if err != nil && !errors.Is(err, db.ErrInvalidTransition) {
		return db.Environment{}, fmt.Errorf("delete environment: %w", err)
	}

	e, err = db.GetEnvironment(ctx, m.db, e.ID)
	if err != nil {
		return db.Environment{}, fmt.Errorf("delete environment: %w", err)
	}

	return e, nil
}

func (m *Manager) Teardown(ctx context.Context, e db.Environment) (err error) {
	p, ok := m.providers[e.Provider]
	if !ok {
		return fmt.Errorf("delete environment %s on %s: %w", e.ID, e.Provider, ErrProviderUnavailable)
	}

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("delete environment %s: provider panic: %v", e.ID, r)
		}
	}()

	if err := p.Delete(ctx, e.ID.String()); err != nil {
		return fmt.Errorf("delete environment %s: %w", e.ID, err)
	}

	err = db.MarkEnvironmentDeleted(ctx, m.db, e.ID)
	if err != nil && !errors.Is(err, db.ErrInvalidTransition) {
		return fmt.Errorf("mark environment %s deleted: %w", e.ID, err)
	}

	return nil
}

func (m *Manager) ResolveStuck(ctx context.Context, e db.Environment) (db.EnvStatus, error) {
	p, ok := m.providers[e.Provider]
	if !ok {
		return "", fmt.Errorf("resolve environment %s on %s: %w", e.ID, e.Provider, ErrProviderUnavailable)
	}

	statusCtx, cancel := context.WithTimeout(ctx, statusTimeout)
	status, err := p.Status(statusCtx, e.ID.String())
	cancel()
	if err != nil {
		return "", fmt.Errorf("resolve environment %s: provider unavailable: %w", e.ID, err)
	}

	if status == provider.StatusRunning {
		m.route(ctx, p, e.ID)
		err := db.MarkEnvironmentRunning(ctx, m.db, e.ID)
		if errors.Is(err, db.ErrInvalidTransition) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("resolve environment %s: %w", e.ID, err)
		}
		return db.EnvRunning, nil
	}

	if err := p.Delete(ctx, e.ID.String()); err != nil {
		return "", fmt.Errorf("resolve environment %s: remove leftovers: %w", e.ID, err)
	}

	err = db.MarkEnvironmentFailed(ctx, m.db, e.ID, fmt.Sprintf("provisioning did not finish: instance %s", status))
	if errors.Is(err, db.ErrInvalidTransition) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve environment %s: %w", e.ID, err)
	}
	return db.EnvFailed, nil
}

func (m *Manager) RemoveOrphans(ctx context.Context) ([]string, error) {
	var removed []string
	var errs []error
	for pt, p := range m.providers {
		ids, err := p.List(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("list %s environments: %w", pt, err))
			continue
		}

		for _, raw := range ids {
			id, err := uuid.Parse(raw)
			if err != nil {
				continue
			}

			e, err := db.GetEnvironment(ctx, m.db, id)
			if err != nil && !errors.Is(err, db.ErrEnvNotFound) {
				errs = append(errs, fmt.Errorf("orphan %s: %w", raw, err))
				continue
			}

			if err == nil && e.Provider == pt && e.Status != db.EnvFailed && e.Status != db.EnvDeleted {
				continue
			}

			if err := p.Delete(ctx, raw); err != nil {
				errs = append(errs, fmt.Errorf("remove orphan %s from %s: %w", raw, pt, err))
				continue
			}
			removed = append(removed, raw)
		}
	}

	return removed, errors.Join(errs...)
}

func (m *Manager) Wait() {
	m.wg.Wait()
}

func (m *Manager) List(ctx context.Context, e db.Environment) (any, error) {
	return m.providers[e.Provider].List(ctx)
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

	m.route(ctx, p, id)

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

func (m *Manager) route(ctx context.Context, p provider.Provider, id uuid.UUID) {
	if m.proxy == nil {
		return
	}

	addr, err := p.Address(ctx, id.String())
	if err != nil {
		log.Printf("route environment %s: %v", id, err)
		return
	}

	if addr == "" {
		return
	}

	if err := db.SetEnvironmentBackend(ctx, m.db, id, addr); err != nil {
		log.Printf("route environment %s: %v", id, err)
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

func reserve(ctx context.Context, tx pgx.Tx, pt provider.Type, px *proxy.Proxy, req CreateRequest) (reservation, error) {
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
	cost := estimatedCost(rate, req.Template.TTL)
	if req.Type != db.EnvProdBug {
		if caller.BudgetExpired {
			return reservation{}, ErrBudgetExpired
		}

		if caller.CostUsed.Add(cost).GreaterThan(caller.CostBudgetTotal) {
			return reservation{}, fmt.Errorf("used %s, estimated %s, budget %s: %w", caller.CostUsed, cost, caller.CostBudgetTotal, ErrBudgetExceeded)
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
		EstimatedCost:   cost,
	})
	if err != nil {
		return reservation{}, err
	}

	if err := db.ReserveCapacity(ctx, tx, pt, need); err != nil {
		return reservation{}, err
	}

	if px != nil && len(req.Template.Workload.Ports) > 0 {
		if err := db.SetEnvironmentEndpoint(ctx, tx, id, px.URL(id)); err != nil {
			return reservation{}, err
		}
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
		limit = normalLimit(pool.Default)
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

type PoolCapacity struct {
	Total       provider.Resources
	Used        provider.Resources
	NormalLimit provider.Resources
	NormalFree  provider.Resources
	ProdBugFree provider.Resources
}

func Capacity(pool db.CapacityPool) PoolCapacity {
	used := sub(pool.Default, pool.Available)
	limit := normalLimit(pool.Default)
	return PoolCapacity{
		Total:       pool.Default,
		Used:        used,
		NormalLimit: limit,
		NormalFree:  atLeastZero(sub(limit, used)),
		ProdBugFree: atLeastZero(pool.Available),
	}
}

func normalLimit(total provider.Resources) provider.Resources {
	return provider.Resources{
		CPUCore: total.CPUCore * 9 / 10,
		RAMGB:   total.RAMGB * 9 / 10,
		DiskGB:  total.DiskGB * 9 / 10,
	}
}

func atLeastZero(r provider.Resources) provider.Resources {
	return provider.Resources{CPUCore: max(0, r.CPUCore), RAMGB: max(0, r.RAMGB), DiskGB: max(0, r.DiskGB)}
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

// estimatedCost = hourly_rate × ttl_seconds / 3600
// where hourly_rate = cpu_core × cpu_price + ram_gb × ram_price + disk_gb × disk_price
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
