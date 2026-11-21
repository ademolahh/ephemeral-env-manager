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
	noInline bool
}

type Option func(*Handler)

// WithoutInlineTemplates makes callers create environments from stored
// templates only, so admins control every service image and setting.
func WithoutInlineTemplates() Option {
	return func(h *Handler) { h.noInline = true }
}

func NewHandler(q db.DBTX, envs *env.Manager, sessions *session.Store, github *auth.GitHub, opts ...Option) *Handler {
	h := &Handler{db: q, envs: envs, sessions: sessions, github: github}
	for _, opt := range opts {
		opt(h)
	}
	return h
}
