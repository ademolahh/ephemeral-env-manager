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

type (
	callerKey struct{}
	adminKey  struct{}
)

func (h *Handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "missing api key")
			return
		}

		if auth.IsAdminToken(token) {
			h.authenticateAdmin(w, r, next, token)
			return
		}

		caller, err := db.CallerByAPIKey(r.Context(), h.db, auth.HashAPIKey(token))
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

func (h *Handler) authenticateAdmin(w http.ResponseWriter, r *http.Request, next http.Handler, token string) {
	id, err := auth.ParseAdminToken(h.adminSecret, token)
	if errors.Is(err, auth.ErrInvalidAdminToken) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "invalid or expired admin token")
		return
	}

	if err != nil {
		log.Printf("authenticate admin: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminKey{}, db.Admin{ID: id})))
}

func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := adminFrom(r.Context()); !ok {
			writeError(w, http.StatusForbidden, "admin only")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func requireCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Value(callerKey{}).(db.AuthenticatedCaller); !ok {
			writeError(w, http.StatusForbidden, "requires a caller api key")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func callerFrom(ctx context.Context) db.AuthenticatedCaller {
	c, _ := ctx.Value(callerKey{}).(db.AuthenticatedCaller)
	return c
}

func adminFrom(ctx context.Context) (db.Admin, bool) {
	a, ok := ctx.Value(adminKey{}).(db.Admin)
	return a, ok
}
