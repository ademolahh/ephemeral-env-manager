package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type setCapacityRequest struct {
	Total *provider.Resources `json:"total"`
}

type poolResponse struct {
	Provider    provider.Type      `json:"provider"`
	Total       provider.Resources `json:"total"`
	Used        provider.Resources `json:"used"`
	NormalLimit provider.Resources `json:"normal_limit"`
	NormalFree  provider.Resources `json:"normal_free"`
	ProdBugFree provider.Resources `json:"prod_bug_free"`
}

type capacityResponse struct {
	Pools []poolResponse `json:"pools"`
}

func (h *Handler) GetCapacity(w http.ResponseWriter, r *http.Request) {
	pools, err := db.ListCapacityPools(r.Context(), h.db)
	if err != nil {
		writeServiceError(w, "get capacity", err)
		return
	}

	resp := capacityResponse{Pools: make([]poolResponse, 0, len(pools))}
	for _, p := range pools {
		c := env.Capacity(p.CapacityPool)
		resp.Pools = append(resp.Pools, poolResponse{
			Provider:    p.Provider,
			Total:       c.Total,
			Used:        c.Used,
			NormalLimit: c.NormalLimit,
			NormalFree:  c.NormalFree,
			ProdBugFree: c.ProdBugFree,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) SetCapacity(w http.ResponseWriter, r *http.Request) {
	p, ok := h.providerFrom(w, r, "set capacity")
	if !ok {
		return
	}

	var req setCapacityRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := db.SetCapacityPool(r.Context(), h.db, p, *req.Total); err != nil {
		writeServiceError(w, "set capacity", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (r setCapacityRequest) validate() error {
	if r.Total == nil {
		return errors.New("total is required")
	}

	if r.Total.CPUCore < 1 || r.Total.RAMGB < 1 || r.Total.DiskGB < 1 {
		return errors.New("total cpu_core, ram_gb and disk_gb must be positive")
	}

	return nil
}

func (h *Handler) providerFrom(w http.ResponseWriter, r *http.Request, op string) (provider.Type, bool) {
	p := provider.Type(r.PathValue("provider"))
	if !h.envs.HasProvider(p) {
		writeServiceError(w, op, fmt.Errorf("%s: %w", p, env.ErrProviderUnavailable))
		return "", false
	}
	return p, true
}
