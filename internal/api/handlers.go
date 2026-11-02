package api

import (
	"net/http"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
)

type Handler struct {
	db   db.DBTX
	envs *env.Manager
}

func NewHandler(q db.DBTX, envs *env.Manager) *Handler {
	return &Handler{db: q, envs: envs}
}

func (h *Handler) CreateCaller(w http.ResponseWriter, r *http.Request) {}

func (h *Handler) GetBudget(w http.ResponseWriter, r *http.Request) {}
