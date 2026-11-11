package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

var (
	defaultQuota        = provider.Resources{CPUCore: 4, RAMGB: 16, DiskGB: 50}
	defaultCostBudget   = int64(10000)
	defaultBudgetPeriod = 30 * 24 * time.Hour
)

type createCallerRequest struct {
	GithubLogin string `json:"github_login"`
	GithubID    string `json:"github_id"`
}

type callerResponse struct {
	ID              uuid.UUID          `json:"id"`
	PermittedEnv    db.EnvType         `json:"permitted_env"`
	GithubLogin     *string            `json:"github_login"`
	GithubID        *string            `json:"github_id"`
	Quota           provider.Resources `json:"quota"`
	CostBudgetTotal int64              `json:"cost_budget_total"`
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
	CostBudgetTotal *int64              `json:"cost_budget_total"`
	BudgetExpiresAt *time.Time          `json:"budget_expires_at"`
}

type budgetResponse struct {
	CallerID        uuid.UUID          `json:"caller_id"`
	Currency        string             `json:"currency"`
	CostBudget      int64              `json:"cost_budget"`
	CostUsed        int64              `json:"cost_used"`
	CostRemaining   int64              `json:"cost_remaining"`
	ProdBugCostUsed int64              `json:"prod_bug_cost_used"`
	ProdBugCostCap  int64              `json:"prod_bug_cost_cap"`
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
		Currency:        "USD cents",
		CostBudget:      budget.CostBudgetTotal,
		CostUsed:        budget.CostUsed,
		CostRemaining:   max(0, budget.CostBudgetTotal-budget.CostUsed),
		ProdBugCostUsed: budget.ProdBugCost,
		ProdBugCostCap:  budget.ProdBugCostCap,
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

	if *r.CostBudgetTotal < 0 {
		return errors.New("cost_budget_total must not be negative")
	}

	if !r.BudgetExpiresAt.After(now) {
		return errors.New("budget_expires_at must be in the future")
	}

	return nil
}

type setPermissionRequest struct {
	PermittedEnv   db.EnvType `json:"permitted_env"`
	ProdBugCostCap *int64     `json:"prod_bug_cost_cap"`
}

func (h *Handler) SetPermission(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid caller id")
		return
	}

	var req setPermissionRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.PermittedEnv != db.EnvRegularTesting && req.PermittedEnv != db.EnvPRReview && req.PermittedEnv != db.EnvProdBug {
		writeError(w, http.StatusBadRequest, "permitted_env must be REGULAR_TESTING, PR_REVIEW or PROD_BUG")
		return
	}

	if req.ProdBugCostCap == nil || *req.ProdBugCostCap < 0 {
		writeError(w, http.StatusBadRequest, "prod_bug_cost_cap is required and must not be negative")
		return
	}

	if err := db.SetCallerPermission(r.Context(), h.db, id, req.PermittedEnv, *req.ProdBugCostCap); err != nil {
		writeServiceError(w, "set permission", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
