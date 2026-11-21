package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		{db.ErrEnvNotFound, http.StatusNotFound},
		{db.ErrInvalidTransition, http.StatusConflict},
		{db.ErrCapacityBelowUsage, http.StatusConflict},
		{db.ErrGithubIDTaken, http.StatusConflict},
		{db.ErrCallerNotFound, http.StatusNotFound},
		{errors.New("connection reset"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		wrapped := errors.Join(errors.New("create environment"), tt.err)
		if got := errorStatus(wrapped); got != tt.want {
			t.Errorf("errorStatus(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}

func TestServiceErrorBodyHasCode(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("create environment: using 1 cpu: %w", env.ErrQuotaExceeded), http.StatusForbidden, "quota_exceeded"},
		{fmt.Errorf("create environment: %w", env.ErrCapacityExceeded), http.StatusServiceUnavailable, "capacity_exceeded"},
		{badRequest("workload is required"), http.StatusBadRequest, "bad_request"},
		{errors.New("connection reset"), http.StatusInternalServerError, "internal"},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		writeServiceError(rec, "test", tt.err)

		var body errorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}
		if rec.Code != tt.status || body.Code != tt.code || body.Error == "" {
			t.Errorf("writeServiceError(%v) = %d %+v, want %d with code %s", tt.err, rec.Code, body, tt.status, tt.code)
		}
	}
}

func TestEveryMappedErrorHasUniqueCode(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range apiErrors {
		if e.code == "" || seen[e.code] {
			t.Errorf("error %v has empty or duplicate code %q", e.err, e.code)
		}
		seen[e.code] = true
	}
}
