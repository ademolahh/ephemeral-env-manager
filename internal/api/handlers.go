package api

import (
	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/session"
)

type Handler struct {
	db       db.DBTX
	envs     *env.Manager
	sessions *session.Store
	github   *auth.GitHub
}

func NewHandler(q db.DBTX, envs *env.Manager, sessions *session.Store, github *auth.GitHub) *Handler {
	return &Handler{db: q, envs: envs, sessions: sessions, github: github}
}
