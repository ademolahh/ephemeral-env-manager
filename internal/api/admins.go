package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
)

const adminSessionTTL = 12 * time.Hour

type adminLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type adminLoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (h *Handler) AdminLogin(w http.ResponseWriter, r *http.Request) {
	var req adminLoginRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	admin, err := db.AdminByUsername(r.Context(), h.db, req.Username)
	if err != nil && !errors.Is(err, db.ErrAdminNotFound) {
		writeServiceError(w, "admin login", err)
		return
	}

	if !auth.CheckPassword(admin.PasswordHash, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	token, expiresAt, err := h.sessions.CreateAdmin(r.Context(), admin.ID, adminSessionTTL)
	if err != nil {
		writeServiceError(w, "admin login", err)
		return
	}

	writeJSON(w, http.StatusOK, adminLoginResponse{Token: token, ExpiresAt: expiresAt})
}
