package provider

import "context"

type Type string

const (
	Docker Type = "docker"
	Cloud  Type = "cloud"
)

type Status string

const (
	StatusRunning   Status = "running"
	StatusStarting  Status = "starting"
	StatusUnhealthy Status = "unhealthy"
	StatusStopped   Status = "stopped"
	StatusMissing   Status = "missing"
	StatusUnknown   Status = "unknown"
)

type Provider interface {
	Create(ctx context.Context, id, workload string, temp Template) (string, error)
	Delete(ctx context.Context, id string) error
	Status(ctx context.Context, id string) (Status, error)
	Address(ctx context.Context, id string) (string, error)
	Exec(ctx context.Context, id string, cmd []string) (string, error)
	List(ctx context.Context) ([]string, error)
}
