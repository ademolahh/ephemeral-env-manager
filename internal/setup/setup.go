package setup

import (
	"context"
	"fmt"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider/docker"
	"github.com/ademolahh/ephemeral-env-manager/internal/proxy"
)

func Manager(ctx context.Context, q db.DBTX) (*env.Manager, *docker.Docker, error) {
	px, err := proxy.FromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("proxy: %w", err)
	}

	cfg, err := docker.ConfigFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("docker: %w", err)
	}
	if px != nil {
		cfg.Proxy = px.Container
	}

	d, err := docker.New(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("docker: %w", err)
	}
	if err := d.Ping(ctx); err != nil {
		return nil, nil, fmt.Errorf("docker: %w", err)
	}

	m := env.NewManager(q, map[provider.Type]provider.Provider{provider.Docker: d})
	m.Proxy = px
	return m, d, nil
}
