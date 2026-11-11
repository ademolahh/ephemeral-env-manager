package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/session"
)

type (
	callerKey  struct{}
	adminKey   struct{}
	sessionKey struct{}
)

func credentials(r *http.Request) string {
	if header := r.Header.Get("Authorization"); header != "" {
		token, _ := strings.CutPrefix(header, "Bearer ")
		if token == header {
			return ""
		}
		return token
	}

	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, msg)
}

func (h *Handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := credentials(r)
		if token == "" {
			unauthorized(w, "missing api key or session")
			return
		}

		sess, err := h.sessions.Get(r.Context(), token)
		if err == nil {
			h.authenticateSession(w, r, next, token, sess)
			return
		}

		if !errors.Is(err, session.ErrNotFound) {
			log.Printf("authenticate session: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		caller, err := db.CallerByAPIKey(r.Context(), h.db, auth.HashToken(token))
		if errors.Is(err, db.ErrAPIKeyNotFound) {
			unauthorized(w, "invalid api key or session")
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

func (h *Handler) authenticateSession(w http.ResponseWriter, r *http.Request, next http.Handler, token string, sess session.Session) {
	ctx := context.WithValue(r.Context(), sessionKey{}, token)
	if sess.AdminID != nil {
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, adminKey{}, db.Admin{ID: *sess.AdminID})))
		return
	}

	c, err := db.GetCaller(r.Context(), h.db, *sess.CallerID)
	if errors.Is(err, db.ErrCallerNotFound) {
		unauthorized(w, "invalid api key or session")
		return
	}

	if err != nil {
		log.Printf("authenticate session: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, callerKey{}, db.AuthenticatedCaller{
		ID:           c.ID,
		Role:         db.RoleUser,
		PermittedEnv: c.PermittedEnv,
		Session:      true,
	})))
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
			writeError(w, http.StatusForbidden, "requires a caller api key or session")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func requireCallerSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, ok := r.Context().Value(callerKey{}).(db.AuthenticatedCaller); !ok || !c.Session {
			writeError(w, http.StatusForbidden, "requires a signed-in caller session")
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

func sessionFrom(ctx context.Context) (string, bool) {
	token, ok := ctx.Value(sessionKey{}).(string)
	return token, ok
}
