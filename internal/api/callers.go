package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

var (
	defaultQuota        = provider.Resources{CPUCore: 4, RAMGB: 16, DiskGB: 50}
	defaultCostBudget   = decimal.NewFromInt(100)
	defaultBudgetPeriod = 30 * 24 * time.Hour
)

type createCallerRequest struct {
	GithubLogin string `json:"github_login"`
	GithubID    string `json:"github_id"`
}

type callerResponse struct {
	ID              uuid.UUID          `json:"id"`
	Role            db.Role            `json:"role"`
	PermittedEnv    db.EnvType         `json:"permitted_env"`
	GithubLogin     *string            `json:"github_login"`
	GithubID        *string            `json:"github_id"`
	Quota           provider.Resources `json:"quota"`
	CostBudgetTotal decimal.Decimal    `json:"cost_budget_total"`
	BudgetExpiresAt time.Time          `json:"budget_expires_at"`
}

func (h *Handler) CreateCaller(w http.ResponseWriter, r *http.Request) {
	var req createCallerRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	c := db.NewCaller{
		GithubLogin:     optional(req.GithubLogin),
		GithubID:        optional(req.GithubID),
		Role:            db.RoleUser,
		PermittedEnv:    db.EnvRegularTesting,
		Quota:           defaultQuota,
		CostBudgetTotal: defaultCostBudget,
		BudgetExpiresAt: time.Now().Add(defaultBudgetPeriod).UTC().Truncate(time.Second),
	}

	id, err := db.CreateCaller(r.Context(), h.db, c)
	if err != nil {
		writeServiceError(w, "create caller", err)
		return
	}

	w.Header().Set("Location", "/callers/"+id.String()+"/budget")
	writeJSON(w, http.StatusCreated, callerResponse{
		ID:              id,
		Role:            c.Role,
		PermittedEnv:    c.PermittedEnv,
		GithubLogin:     c.GithubLogin,
		GithubID:        c.GithubID,
		Quota:           c.Quota,
		CostBudgetTotal: c.CostBudgetTotal,
		BudgetExpiresAt: c.BudgetExpiresAt,
	})
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type setBudgetRequest struct {
	Quota           *provider.Resources `json:"quota"`
	CostBudgetTotal *decimal.Decimal    `json:"cost_budget_total"`
	BudgetExpiresAt *time.Time          `json:"budget_expires_at"`
}

type budgetResponse struct {
	CallerID        uuid.UUID          `json:"caller_id"`
	Currency        string             `json:"currency"`
	CostBudget      decimal.Decimal    `json:"cost_budget"`
	CostUsed        decimal.Decimal    `json:"cost_used"`
	CostRemaining   decimal.Decimal    `json:"cost_remaining"`
	ProdBugCostUsed decimal.Decimal    `json:"prod_bug_cost_used"`
	BudgetExpiresAt time.Time          `json:"budget_expires_at"`
	Quota           provider.Resources `json:"quota"`
	ResourcesUsed   provider.Resources `json:"resources_used"`
}

func (h *Handler) GetBudget(w http.ResponseWriter, r *http.Request) {
	_, admin := adminFrom(r.Context())
	caller := callerFrom(r.Context())
	id := caller.ID

	if r.PathValue("id") == "me" {
		if admin {
			writeError(w, http.StatusBadRequest, "admins have no budget; use a caller id")
			return
		}
	} else {
		var err error
		id, err = uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid caller id")
			return
		}
		if id != caller.ID && !admin {
			writeError(w, http.StatusNotFound, "caller not found")
			return
		}
	}

	resp, err := h.callerBudget(r.Context(), id)
	if err != nil {
		writeServiceError(w, "get budget", err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) callerBudget(ctx context.Context, id uuid.UUID) (budgetResponse, error) {
	budget, err := db.GetCallerBudget(ctx, h.db, id)
	if err != nil {
		return budgetResponse{}, err
	}

	resourcesUsed, err := db.CallerUsage(ctx, h.db, id)
	if err != nil {
		return budgetResponse{}, err
	}

	return budgetResponse{
		CallerID:        id,
		Currency:        "USD",
		CostBudget:      budget.CostBudgetTotal,
		CostUsed:        budget.CostUsed,
		CostRemaining:   decimal.Max(decimal.Zero, budget.CostBudgetTotal.Sub(budget.CostUsed)),
		ProdBugCostUsed: budget.ProdBugCost,
		BudgetExpiresAt: budget.BudgetExpiresAt,
		Quota:           budget.Quota,
		ResourcesUsed:   resourcesUsed,
	}, nil
}

func (h *Handler) SetBudget(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid caller id")
		return
	}

	var req setBudgetRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if err := req.validate(time.Now()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := db.SetCallerBudget(r.Context(), h.db, id, *req.Quota, *req.CostBudgetTotal, *req.BudgetExpiresAt); err != nil {
		writeServiceError(w, "set budget", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (r setBudgetRequest) validate(now time.Time) error {
	if r.Quota == nil || r.CostBudgetTotal == nil || r.BudgetExpiresAt == nil {
		return errors.New("quota, cost_budget_total and budget_expires_at are required")
	}

	if r.Quota.CPUCore < 0 || r.Quota.RAMGB < 0 || r.Quota.DiskGB < 0 {
		return errors.New("quota must not be negative")
	}

	if r.CostBudgetTotal.IsNegative() {
		return errors.New("cost_budget_total must not be negative")
	}

	if !r.BudgetExpiresAt.After(now) {
		return errors.New("budget_expires_at must be in the future")
	}

	return nil
}
