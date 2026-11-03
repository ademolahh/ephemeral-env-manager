package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type setBudgetRequest struct {
	Quota           *provider.Resources `json:"quota"`
	CostBudget      *decimal.Decimal    `json:"cost_budget"`
	BudgetExpiresAt *time.Time          `json:"budget_expires_at"`
}

type budgetResponse struct {
	CallerID        uuid.UUID          `json:"caller_id"`
	Currency        string             `json:"currency"`
	CostBudget      decimal.Decimal    `json:"cost_budget"`
	CostUsed        decimal.Decimal    `json:"cost_used"`
	CostRemaining   decimal.Decimal    `json:"cost_remaining"`
	BudgetExpiresAt time.Time          `json:"budget_expires_at"`
	Quota           provider.Resources `json:"quota"`
	ResourcesUsed   provider.Resources `json:"resources_used"`
}

func (h *Handler) GetBudget(w http.ResponseWriter, r *http.Request) {
	caller := callerFrom(r.Context())
	id := caller.ID
	if r.PathValue("id") != "me" {
		var err error
		id, err = uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid caller id")
			return
		}
		if id != caller.ID && caller.Role != db.RoleAdmin {
			writeError(w, http.StatusNotFound, "caller not found")
			return
		}
	}

	budget, err := db.GetCallerBudget(r.Context(), h.db, id)
	if err != nil {
		writeServiceError(w, "get budget", err)
		return
	}
	used, err := db.CallerSpend(r.Context(), h.db, id, budget.BudgetSetAt)
	if err != nil {
		writeServiceError(w, "get cost usage", err)
		return
	}

	resourcesUsed, err := db.CallerUsage(r.Context(), h.db, id)
	if err != nil {
		writeServiceError(w, "get resource usage", err)
		return
	}

	writeJSON(w, http.StatusOK, budgetResponse{
		CallerID:        id,
		Currency:        "USD",
		CostBudget:      budget.CostBudget.Add(used),
		CostUsed:        used,
		CostRemaining:   budget.CostBudget,
		BudgetExpiresAt: budget.BudgetExpiresAt,
		Quota:           budget.Quota,
		ResourcesUsed:   resourcesUsed,
	})
}

func (h *Handler) SetBudget(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid caller id")
		return
	}

	var req setBudgetRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}

		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := req.validate(time.Now()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := db.SetCallerBudget(r.Context(), h.db, id, *req.Quota, *req.CostBudget, *req.BudgetExpiresAt); err != nil {
		writeServiceError(w, "set budget", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (r setBudgetRequest) validate(now time.Time) error {
	if r.Quota == nil || r.CostBudget == nil || r.BudgetExpiresAt == nil {
		return errors.New("quota, cost_budget and budget_expires_at are required")
	}

	if r.Quota.CPUCore < 0 || r.Quota.RAMGB < 0 || r.Quota.DiskGB < 0 {
		return errors.New("quota must not be negative")
	}

	if r.CostBudget.IsNegative() {
		return errors.New("cost_budget must not be negative")
	}

	if !r.BudgetExpiresAt.After(now) {
		return errors.New("budget_expires_at must be in the future")
	}

	return nil
}
