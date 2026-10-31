package docker

import (
	"context"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type Docker struct{}

func New() *Docker {
	return &Docker{}
}

func (d *Docker) Create(ctx context.Context, id string, temp provider.Template) (string, error) {
	return "", nil
}

func (d *Docker) Delete(ctx context.Context, id string) error {
	return nil
}

func (d *Docker) Status(ctx context.Context, id string) (provider.Status, error) {
	return "", nil
}

func (d *Docker) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	return "", nil
}

func (d *Docker) List(ctx context.Context) ([]string, error) {
	return nil, nil
}
