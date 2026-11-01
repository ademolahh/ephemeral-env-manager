package api

import (
	"errors"
	"log"
	"net/http"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
)

func writeServiceError(w http.ResponseWriter, op string, err error) {
	status := errorStatus(err)
	if status == http.StatusInternalServerError {
		log.Printf("%s: %v", op, err)
		writeError(w, status, "internal error")
		return
	}

	writeError(w, status, err.Error())
}

func errorStatus(err error) int {
	switch {
	case errors.Is(err, env.ErrInvalidEnvType),
		errors.Is(err, env.ErrNoProvider),
		errors.Is(err, env.ErrProviderNotAllowed),
		errors.Is(err, env.ErrProviderUnavailable):
		return http.StatusBadRequest

	case errors.Is(err, env.ErrNotPermitted),
		errors.Is(err, env.ErrQuotaExceeded),
		errors.Is(err, env.ErrBudgetExceeded),
		errors.Is(err, env.ErrBudgetExpired):
		return http.StatusForbidden

	case errors.Is(err, env.ErrCapacityExceeded),
		errors.Is(err, db.ErrPoolNotFound),
		errors.Is(err, db.ErrPricesNotFound):
		return http.StatusServiceUnavailable

	default:
		return http.StatusInternalServerError
	}
}
