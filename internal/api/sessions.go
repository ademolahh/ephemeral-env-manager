package api

import (
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/session"
)

const (
	sessionCookie    = "eem_session"
	oauthCookie      = "eem_oauth"
	oauthCookiePath  = "/auth/github"
	oauthStateTTL    = 10 * time.Minute
	callerSessionTTL = 24 * time.Hour
)

type providersResponse struct {
	GitHub bool `json:"github"`
}

func (h *Handler) AuthProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, providersResponse{GitHub: h.github != nil})
}

func (h *Handler) GitHubLogin(w http.ResponseWriter, r *http.Request) {
	if h.github == nil {
		writeError(w, http.StatusNotFound, "github login is not configured")
		return
	}

	state, verifier := auth.RandomString(), auth.NewVerifier()
	http.SetCookie(w, &http.Cookie{
		Name:     oauthCookie,
		Value:    state + "." + verifier,
		Path:     oauthCookiePath,
		MaxAge:   int(oauthStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   h.github.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, h.github.AuthCodeURL(state, verifier), http.StatusFound)
}

func (h *Handler) GitHubCallback(w http.ResponseWriter, r *http.Request) {
	if h.github == nil {
		writeError(w, http.StatusNotFound, "github login is not configured")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     oauthCookie,
		Path:     oauthCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.github.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})

	q := r.URL.Query()
	cookie, err := r.Cookie(oauthCookie)
	if err != nil {
		loginFailed(w, r, "expired")
		return
	}

	state, verifier, ok := strings.Cut(cookie.Value, ".")
	if !ok || subtle.ConstantTimeCompare([]byte(state), []byte(q.Get("state"))) != 1 {
		loginFailed(w, r, "state")
		return
	}

	if q.Get("error") != "" || q.Get("code") == "" {
		loginFailed(w, r, "denied")
		return
	}

	user, err := h.github.User(r.Context(), q.Get("code"), verifier)
	if err != nil {
		log.Printf("github login: %v", err)
		loginFailed(w, r, "github")
		return
	}

	if !h.github.Allowed(user.Login) {
		exists, err := db.GithubCallerExists(r.Context(), h.db, user.ID)
		if err != nil {
			log.Printf("github login: %v", err)
			loginFailed(w, r, "internal")
			return
		}

		if !exists {
			loginFailed(w, r, "not_allowed")
			return
		}
	}

	callerID, err := db.UpsertGithubCaller(r.Context(), h.db, db.NewCaller{
		GithubLogin:     &user.Login,
		GithubID:        &user.ID,
		PermittedEnv:    db.EnvRegularTesting,
		Quota:           defaultQuota,
		CostBudgetTotal: defaultCostBudget,
		BudgetExpiresAt: time.Now().Add(defaultBudgetPeriod).UTC().Truncate(time.Second),
	})
	if err != nil {
		log.Printf("github login: %v", err)
		loginFailed(w, r, "internal")
		return
	}

	token, expiresAt, err := h.sessions.CreateCaller(r.Context(), callerID, callerSessionTTL)
	if err != nil {
		log.Printf("github login: %v", err)
		loginFailed(w, r, "internal")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   h.github.SecureCookies(),
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func loginFailed(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, "/?"+url.Values{"login_error": {reason}}.Encode(), http.StatusFound)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	token, ok := sessionFrom(r.Context())
	if !ok {
		writeError(w, http.StatusBadRequest, "not signed in with a session")
		return
	}

	if err := h.sessions.Delete(r.Context(), token); err != nil && !errors.Is(err, session.ErrNotFound) {
		writeServiceError(w, "logout", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.github != nil && h.github.SecureCookies(),
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

type meResponse struct {
	Kind         string     `json:"kind"`
	ID           uuid.UUID  `json:"id"`
	GithubLogin  *string    `json:"github_login,omitempty"`
	Role         db.Role    `json:"role,omitempty"`
	PermittedEnv db.EnvType `json:"permitted_env,omitempty"`
	APIKeyID     *uuid.UUID `json:"api_key_id,omitempty"`
	Session      bool       `json:"session"`
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	_, session := sessionFrom(r.Context())
	if admin, ok := adminFrom(r.Context()); ok {
		writeJSON(w, http.StatusOK, meResponse{Kind: "admin", ID: admin.ID, Session: session})
		return
	}

	caller := callerFrom(r.Context())
	c, err := db.GetCaller(r.Context(), h.db, caller.ID)
	if err != nil {
		writeServiceError(w, "me", err)
		return
	}

	resp := meResponse{
		Kind:         "caller",
		ID:           caller.ID,
		GithubLogin:  c.GithubLogin,
		Role:         caller.Role,
		PermittedEnv: caller.PermittedEnv,
		Session:      session,
	}
	if caller.KeyID != uuid.Nil {
		resp.APIKeyID = &caller.KeyID
	}
	writeJSON(w, http.StatusOK, resp)
}
