package api

import (
	"net/http"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

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
