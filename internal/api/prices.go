package api

import (
	"errors"
	"net/http"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type setPricesRequest struct {
	CPUCore *int64 `json:"cpu_core"`
	RAMGB   *int64 `json:"ram_gb"`
	DiskGB  *int64 `json:"disk_gb"`
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

	prices := provider.Prices{CPUCore: *req.CPUCore, RAMGB: *req.RAMGB, DiskGB: *req.DiskGB}
	if err := db.SetHourlyPrices(r.Context(), h.db, p, prices); err != nil {
		writeServiceError(w, "set prices", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (r setPricesRequest) validate() error {
	if r.CPUCore == nil || r.RAMGB == nil || r.DiskGB == nil {
		return errors.New("cpu_core, ram_gb and disk_gb are required")
	}

	if *r.CPUCore < 0 || *r.RAMGB < 0 || *r.DiskGB < 0 {
		return errors.New("prices must not be negative")
	}

	return nil
}
