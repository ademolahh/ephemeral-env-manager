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
	status := errorStatus(err)
	if status == http.StatusInternalServerError {
		log.Printf("%s: %v", op, err)
		writeError(w, status, "internal error")
		return
	}

	writeError(w, status, err.Error())
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

func errorStatus(err error) int {
	if _, ok := errors.AsType[*badRequestError](err); ok {
		return http.StatusBadRequest
	}

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

	case errors.Is(err, db.ErrEnvNotFound),
		errors.Is(err, db.ErrCallerNotFound),
		errors.Is(err, db.ErrTemplateNotFound):
		return http.StatusNotFound

	case errors.Is(err, db.ErrInvalidTransition),
		errors.Is(err, env.ErrNotRunning),
		errors.Is(err, db.ErrCapacityBelowUsage),
		errors.Is(err, db.ErrGithubIDTaken),
		errors.Is(err, db.ErrTemplateExists):
		return http.StatusConflict

	case errors.Is(err, env.ErrCapacityExceeded),
		errors.Is(err, db.ErrPoolNotFound),
		errors.Is(err, db.ErrPricesNotFound):
		return http.StatusServiceUnavailable

	default:
		return http.StatusInternalServerError
	}
}
