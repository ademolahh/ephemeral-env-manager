package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
)

const (
	defaultKeyTTL = 30 * 24 * time.Hour
	maxKeyTTL     = 365 * 24 * time.Hour
	maxKeyNameLen = 64
)

type createKeyRequest struct {
	Name         string      `json:"name"`
	Role         db.Role     `json:"role"`
	ExpiresIn    string      `json:"expires_in"`
	PermittedEnv *db.EnvType `json:"permitted_env"`
	CostCap      *int64      `json:"cost_cap"`
}

type createKeyResponse struct {
	db.APIKey
	Key string `json:"key"`
}

type listKeysResponse struct {
	Keys []db.APIKey `json:"keys"`
}

func (h *Handler) CreateKey(w http.ResponseWriter, r *http.Request) {
	h.createKey(w, r, callerFrom(r.Context()).ID)
}

func (h *Handler) CreateCallerKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid caller id")
		return
	}
	h.createKey(w, r, id)
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request, callerID uuid.UUID) {
	var req createKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	nk, err := h.newAPIKey(r.Context(), callerID, req)
	if err != nil {
		writeServiceError(w, "create api key", err)
		return
	}

	key, hash := auth.GenerateAPIKey()
	nk.Hash = hash
	created, err := db.CreateAPIKey(r.Context(), h.db, nk)
	if err != nil {
		writeServiceError(w, "create api key", err)
		return
	}

	w.Header().Set("Location", "/keys/"+created.ID.String())
	writeJSON(w, http.StatusCreated, createKeyResponse{APIKey: created, Key: key})
}

func (h *Handler) newAPIKey(ctx context.Context, callerID uuid.UUID, req createKeyRequest) (db.NewAPIKey, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > maxKeyNameLen {
		return db.NewAPIKey{}, badRequest("name is required and at most %d characters", maxKeyNameLen)
	}

	role := req.Role
	if role == "" {
		role = db.RoleUser
	}
	if role != db.RoleUser && role != db.RoleAgent {
		return db.NewAPIKey{}, badRequest("role must be user or agent")
	}

	ttl := defaultKeyTTL
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil || d <= 0 || d > maxKeyTTL {
			return db.NewAPIKey{}, badRequest("expires_in must be a duration between 1s and %s", maxKeyTTL)
		}
		ttl = d
	}

	if req.CostCap != nil && *req.CostCap < 0 {
		return db.NewAPIKey{}, badRequest("cost_cap must not be negative")
	}

	owner, err := db.GetCaller(ctx, h.db, callerID)
	if err != nil {
		return db.NewAPIKey{}, err
	}

	if req.PermittedEnv != nil {
		if req.PermittedEnv.Rank() == 0 {
			return db.NewAPIKey{}, badRequest("permitted_env must be REGULAR_TESTING, PR_REVIEW or PROD_BUG")
		}
		if req.PermittedEnv.Rank() > owner.PermittedEnv.Rank() {
			return db.NewAPIKey{}, fmt.Errorf("key permission %s above caller permission %s: %w", *req.PermittedEnv, owner.PermittedEnv, env.ErrNotPermitted)
		}
	}

	return db.NewAPIKey{
		CallerID:     callerID,
		Name:         name,
		Role:         role,
		PermittedEnv: req.PermittedEnv,
		CostCap:      req.CostCap,
		ExpiresAt:    time.Now().Add(ttl).UTC().Truncate(time.Second),
	}, nil
}

func (h *Handler) ListKeys(w http.ResponseWriter, r *http.Request) {
	h.listKeys(w, r, callerFrom(r.Context()).ID)
}

func (h *Handler) ListCallerKeys(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid caller id")
		return
	}

	if _, err := db.GetCaller(r.Context(), h.db, id); err != nil {
		writeServiceError(w, "list api keys", err)
		return
	}
	h.listKeys(w, r, id)
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request, callerID uuid.UUID) {
	keys, err := db.ListAPIKeys(r.Context(), h.db, callerID)
	if err != nil {
		writeServiceError(w, "list api keys", err)
		return
	}

	if keys == nil {
		keys = []db.APIKey{}
	}
	writeJSON(w, http.StatusOK, listKeysResponse{Keys: keys})
}

func (h *Handler) RevokeKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid api key id")
		return
	}

	owner := callerFrom(r.Context()).ID
	if _, admin := adminFrom(r.Context()); admin {
		owner = uuid.Nil
	}

	if err := db.RevokeAPIKey(r.Context(), h.db, id, owner); err != nil {
		writeServiceError(w, "revoke api key", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
