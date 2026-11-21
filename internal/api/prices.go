package api

import (
	"errors"
	"net/http"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type setPricesRequest struct {
	PerCPUCore *int64 `json:"per_cpu_core"`
	PerRAMGB   *int64 `json:"per_ram_gb"`
	PerDiskGB  *int64 `json:"per_disk_gb"`
}

func (h *Handler) SetPrices(w http.ResponseWriter, r *http.Request) {
	p, ok := h.providerFrom(w, r, "set prices")
	if !ok {
		return
	}

	var req setPricesRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	prices := provider.Prices{PerCPUCore: *req.PerCPUCore, PerRAMGB: *req.PerRAMGB, PerDiskGB: *req.PerDiskGB}
	if err := db.SetHourlyPrices(r.Context(), h.db, p, prices); err != nil {
		writeServiceError(w, "set prices", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (r setPricesRequest) validate() error {
	if r.PerCPUCore == nil || r.PerRAMGB == nil || r.PerDiskGB == nil {
		return errors.New("per_cpu_core, per_ram_gb and per_disk_gb are required")
	}

	if *r.PerCPUCore < 0 || *r.PerRAMGB < 0 || *r.PerDiskGB < 0 {
		return errors.New("prices must not be negative")
	}

	return nil
}
