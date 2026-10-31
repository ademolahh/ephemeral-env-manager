package provider

import "context"

type Type string

const (
	Docker Type = "docker"
	Cloud  Type = "cloud"
)

type Status string

type Provider interface {
	Create(ctx context.Context, id string, temp Template) (string, error)
	Delete(ctx context.Context, id string) error
	Status(ctx context.Context, id string) (Status, error)
	Exec(ctx context.Context, id string, cmd []string) (string, error)
	List(ctx context.Context) ([]string, error)
}
