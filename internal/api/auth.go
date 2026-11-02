package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
)

type callerKey struct{}

func (h *Handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || key == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "missing api key")
			return
		}

		caller, err := db.CallerByAPIKey(r.Context(), h.db, auth.HashAPIKey(key))
		if errors.Is(err, db.ErrAPIKeyNotFound) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "invalid api key")
			return
		}

		if err != nil {
			log.Printf("authenticate: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, caller)))
	})
}

func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if callerFrom(r.Context()).Role != db.RoleAdmin {
			writeError(w, http.StatusForbidden, "admin only")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func callerFrom(ctx context.Context) db.AuthenticatedCaller {
	c, _ := ctx.Value(callerKey{}).(db.AuthenticatedCaller)
	return c
}
