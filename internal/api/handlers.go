package api

import (
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
)

type Handler struct {
	db          db.DBTX
	envs        *env.Manager
	adminSecret []byte
}

func NewHandler(q db.DBTX, envs *env.Manager, adminSecret []byte) *Handler {
	return &Handler{db: q, envs: envs, adminSecret: adminSecret}
}
