package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
)

func TestErrorStatus(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{env.ErrInvalidEnvType, http.StatusBadRequest},
		{env.ErrNoProvider, http.StatusBadRequest},
		{env.ErrProviderNotAllowed, http.StatusBadRequest},
		{env.ErrProviderUnavailable, http.StatusBadRequest},
		{env.ErrNotPermitted, http.StatusForbidden},
		{env.ErrQuotaExceeded, http.StatusForbidden},
		{env.ErrBudgetExceeded, http.StatusForbidden},
		{env.ErrBudgetExpired, http.StatusForbidden},
		{env.ErrCapacityExceeded, http.StatusServiceUnavailable},
		{db.ErrPoolNotFound, http.StatusServiceUnavailable},
		{db.ErrPricesNotFound, http.StatusServiceUnavailable},
		{db.ErrCallerNotFound, http.StatusInternalServerError},
		{errors.New("connection reset"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		wrapped := errors.Join(errors.New("create environment"), tt.err)
		if got := errorStatus(wrapped); got != tt.want {
			t.Errorf("errorStatus(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}
