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
	ErrProdBugCapExceeded  = errors.New("PROD_BUG cost cap exceeded")
	ErrKeyCostCapExceeded  = errors.New("api key cost cap exceeded")
	ErrIdempotencyKeyInUse = errors.New("idempotency key already used by another api key")
	ErrQuotaExceeded       = errors.New("quota exceeded")
	ErrBudgetExpired       = errors.New("cost budget expired")
	ErrBudgetExceeded      = errors.New("cost budget exceeded")
	ErrCapacityExceeded    = errors.New("capacity pool exhausted")
	ErrNotRunning          = errors.New("environment is not running")
)

const (
	statusTimeout = 5 * time.Second
	execTimeout   = 5 * time.Minute
	readyInterval = 2 * time.Second
)

type Manager struct {
	db          db.DBTX
	providers   map[provider.Type]provider.Provider
	proxy       *proxy.Proxy
	wg          sync.WaitGroup
	provisioned chan uuid.UUID
	watching    sync.WaitGroup
}

type Option func(*Manager)

func WithProxy(p *proxy.Proxy) Option {
	return func(m *Manager) { m.proxy = p }
}

func NewManager(q db.DBTX, providers map[provider.Type]provider.Provider, opts ...Option) *Manager {
	m := &Manager{db: q, providers: providers, provisioned: make(chan uuid.UUID, 100)}
	for _, opt := range opts {
		opt(m)
	}

	go m.watch()
	return m
}

type CreateRequest struct {
	CallerID       uuid.UUID
	APIKeyID       uuid.UUID
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

	if key := caller.AgentKey(); key != nil && (e.APIKeyID == nil || *e.APIKeyID != *key) {
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
		err := db.MarkEnvironmentRunning(ctx, m.db, e.ID, m.backend(ctx, p, e.ID))
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

func (m *Manager) WaitReady() {
	m.wg.Wait()
	m.watching.Wait()
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

	m.watching.Add(1)
	m.provisioned <- id
}

func (m *Manager) watch() {
	for id := range m.provisioned {
		if m.checkProvisioned(id) {
			m.watching.Done()
		} else {
			time.AfterFunc(readyInterval, func() { m.provisioned <- id })
		}
	}
}

func (m *Manager) checkProvisioned(id uuid.UUID) bool {
	ctx := context.Background()
	e, err := db.GetEnvironment(ctx, m.db, id)
	if err != nil {
		log.Printf("check environment %s: %v", id, err)
		return false
	}

	p, ok := m.providers[e.Provider]
	if !ok || e.Status != db.EnvProvisioning {
		return true
	}

	statusCtx, cancel := context.WithTimeout(ctx, statusTimeout)
	status, err := p.Status(statusCtx, id.String())
	cancel()
	if err != nil || status == provider.StatusStarting {
		return false
	}

	if status != provider.StatusRunning {
		if err := p.Delete(ctx, id.String()); err != nil {
			log.Printf("remove environment %s: %v", id, err)
		}
		m.fail(ctx, id, fmt.Sprintf("environment did not become ready: instance %s", status))
		return true
	}

	if err := db.MarkEnvironmentRunning(ctx, m.db, id, m.backend(ctx, p, id)); err != nil {
		log.Printf("mark environment %s running: %v", id, err)
	}
	return true
}

func (m *Manager) backend(ctx context.Context, p provider.Provider, id uuid.UUID) string {
	if m.proxy == nil {
		return ""
	}

	addr, err := p.Address(ctx, id.String())
	if err != nil {
		log.Printf("route environment %s: %v", id, err)
	}

	return addr
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
	if priority(req.Type) == 0 {
		return reservation{}, fmt.Errorf("%w: %q", ErrInvalidEnvType, req.Type)
	}

	caller, err := db.LockCaller(ctx, tx, req.CallerID)
	if err != nil {
		return reservation{}, err
	}

	role, permitted := db.RoleUser, caller.PermittedEnv
	var limits db.KeyLimits
	var apiKeyID *uuid.UUID
	if req.APIKeyID != uuid.Nil {
		limits, err = db.LockAPIKey(ctx, tx, req.CallerID, req.APIKeyID)
		if err != nil {
			return reservation{}, err
		}

		apiKeyID = &req.APIKeyID
		role = limits.Role
		if limits.PermittedEnv != nil && priority(*limits.PermittedEnv) < priority(permitted) {
			permitted = *limits.PermittedEnv
		}
	}

	var key *string
	if req.IdempotencyKey != "" {
		key = &req.IdempotencyKey
		existing, err := db.EnvironmentByIdempotencyKey(ctx, tx, req.CallerID, req.IdempotencyKey)
		if err == nil {
			if role == db.RoleAgent && (existing.APIKeyID == nil || *existing.APIKeyID != req.APIKeyID) {
				return reservation{}, ErrIdempotencyKeyInUse
			}
			return reservation{env: existing}, nil
		}

		if !errors.Is(err, db.ErrEnvNotFound) {
			return reservation{}, err
		}
	}

	if err := authorize(role, permitted, req.Type); err != nil {
		return reservation{}, err
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
	if limits.CostCap != nil && limits.CostSpent+cost > *limits.CostCap {
		return reservation{}, fmt.Errorf("key spent %d, estimated %d, cap %d: %w", limits.CostSpent, cost, *limits.CostCap, ErrKeyCostCapExceeded)
	}

	if req.Type == db.EnvProdBug && role == db.RoleAgent {
		if caller.ProdBugCost+cost > caller.ProdBugCostCap {
			return reservation{}, fmt.Errorf("used %d, estimated %d, cap %d: %w", caller.ProdBugCost, cost, caller.ProdBugCostCap, ErrProdBugCapExceeded)
		}
	} else if req.Type != db.EnvProdBug {
		if caller.BudgetExpired {
			return reservation{}, ErrBudgetExpired
		}

		if caller.CostUsed+cost > caller.CostBudgetTotal {
			return reservation{}, fmt.Errorf("used %d, estimated %d, budget %d: %w", caller.CostUsed, cost, caller.CostBudgetTotal, ErrBudgetExceeded)
		}
	}

	preempted, err := makeRoom(ctx, tx, pt, req.Type, need)
	if err != nil {
		return reservation{}, err
	}

	id, err := db.CreateEnvironment(ctx, tx, db.NewEnvironment{
		CallerID:        req.CallerID,
		APIKeyID:        apiKeyID,
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

func authorize(role db.Role, permitted db.EnvType, t db.EnvType) error {
	if priority(t) > priority(permitted) {
		return fmt.Errorf("%s with permission %s: %w", t, permitted, ErrNotPermitted)
	}

	if role == db.RoleAgent && t != db.EnvPRReview && t != db.EnvProdBug {
		return fmt.Errorf("agents create PR_REVIEW or PROD_BUG, not %s: %w", t, ErrNotPermitted)
	}

	return nil
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
	return t.Rank()
}

func lowerPriority(t db.EnvType) []db.EnvType {
	var lower []db.EnvType
	for _, et := range db.EnvTypes {
		if priority(et) < priority(t) {
			lower = append(lower, et)
		}
	}
	return lower
}

func estimatedCost(rate int64, ttl time.Duration) int64 {
	const microsPerCentHour = 3600 * 10000
	return (rate*int64(ttl/time.Second) + microsPerCentHour - 1) / microsPerCentHour
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
