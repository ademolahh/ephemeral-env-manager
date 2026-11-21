package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
)

func writeServiceError(w http.ResponseWriter, op string, err error) {
	status, code := classify(err)
	if status == http.StatusInternalServerError {
		log.Printf("%s: %v", op, err)
		writeErrorCode(w, status, code, "internal error")
		return
	}

	writeErrorCode(w, status, code, err.Error())
}

type badRequestError struct {
	msg string
}

func (e *badRequestError) Error() string {
	return e.msg
}

func badRequest(format string, args ...any) error {
	return &badRequestError{msg: fmt.Sprintf(format, args...)}
}

// apiErrors maps service errors to an HTTP status and a stable code that
// clients can branch on instead of parsing the message.
var apiErrors = []struct {
	err    error
	status int
	code   string
}{
	{env.ErrInvalidEnvType, http.StatusBadRequest, "invalid_env_type"},
	{env.ErrNoProvider, http.StatusBadRequest, "no_provider"},
	{env.ErrProviderNotAllowed, http.StatusBadRequest, "provider_not_allowed"},
	{env.ErrProviderUnavailable, http.StatusBadRequest, "provider_unavailable"},

	{env.ErrNotPermitted, http.StatusForbidden, "not_permitted"},
	{env.ErrQuotaExceeded, http.StatusForbidden, "quota_exceeded"},
	{env.ErrBudgetExceeded, http.StatusForbidden, "budget_exceeded"},
	{env.ErrBudgetExpired, http.StatusForbidden, "budget_expired"},
	{env.ErrProdBugCapExceeded, http.StatusForbidden, "prod_bug_cap_exceeded"},
	{env.ErrKeyCostCapExceeded, http.StatusForbidden, "key_cost_cap_exceeded"},
	{db.ErrAPIKeyInactive, http.StatusForbidden, "api_key_inactive"},

	{db.ErrEnvNotFound, http.StatusNotFound, "env_not_found"},
	{db.ErrCallerNotFound, http.StatusNotFound, "caller_not_found"},
	{db.ErrTemplateNotFound, http.StatusNotFound, "template_not_found"},
	{db.ErrAPIKeyNotFound, http.StatusNotFound, "api_key_not_found"},

	{db.ErrInvalidTransition, http.StatusConflict, "invalid_transition"},
	{env.ErrNotRunning, http.StatusConflict, "env_not_running"},
	{db.ErrCapacityBelowUsage, http.StatusConflict, "capacity_below_usage"},
	{db.ErrGithubIDTaken, http.StatusConflict, "github_id_taken"},
	{db.ErrTemplateExists, http.StatusConflict, "template_exists"},
	{env.ErrIdempotencyKeyInUse, http.StatusConflict, "idempotency_key_in_use"},

	{env.ErrCapacityExceeded, http.StatusServiceUnavailable, "capacity_exceeded"},
	{db.ErrPoolNotFound, http.StatusServiceUnavailable, "capacity_pool_not_found"},
	{db.ErrPricesNotFound, http.StatusServiceUnavailable, "prices_not_found"},
}

func classify(err error) (int, string) {
	if _, ok := errors.AsType[*badRequestError](err); ok {
		return http.StatusBadRequest, "bad_request"
	}

	for _, e := range apiErrors {
		if errors.Is(err, e.err) {
			return e.status, e.code
		}
	}

	return http.StatusInternalServerError, "internal"
}

func errorStatus(err error) int {
	status, _ := classify(err)
	return status
}

// statusCode is the code for errors that have no more specific one.
func statusCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusRequestEntityTooLarge:
		return "body_too_large"
	case http.StatusGatewayTimeout:
		return "timeout"
	case http.StatusServiceUnavailable:
		return "unavailable"
	default:
		return "internal"
	}
}
