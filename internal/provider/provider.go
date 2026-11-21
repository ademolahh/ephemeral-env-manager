package provider

import (
	"context"
	"errors"
	"strconv"
)

var ErrEmptyCommand = errors.New("command is empty")

type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return "command exited with code " + strconv.Itoa(e.Code)
}

type Type string

const Docker Type = "docker"

type Status string

const (
	StatusRunning   Status = "running"
	StatusStarting  Status = "starting"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
	StatusStopped   Status = "stopped"
	StatusMissing   Status = "missing"
	StatusUnknown   Status = "unknown"
)

// Output is stdout and stderr combined; Truncated means it hit the size limit.
type ExecResult struct {
	Output    string
	Truncated bool
}

type Provider interface {
	Create(ctx context.Context, id, workload string, temp Template) (string, error)
	Delete(ctx context.Context, id string) error
	Status(ctx context.Context, id string) (Status, error)
	Address(ctx context.Context, id string) (string, error)
	Exec(ctx context.Context, id string, cmd []string) (ExecResult, error)
	List(ctx context.Context) ([]string, error)
}
