package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func createExecEnv(t *testing.T, d *Docker) string {
	t.Helper()
	id := newEnvID(t, d)
	if _, err := d.Create(context.Background(), id, testImage, provider.Template{
		Workload: provider.Workload{Resources: provider.Resources{CPUCore: 1, RAMGB: 1}},
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return id
}

func TestExec(t *testing.T) {
	d := testDocker(t)
	id := createExecEnv(t, d)
	ctx := context.Background()

	t.Run("stdout", func(t *testing.T) {
		out, err := d.Exec(ctx, id, []string{"echo", "hello"})
		if err != nil || out != "hello\n" {
			t.Errorf("Exec() = %q, %v, want hello", out, err)
		}
	})

	t.Run("stderr and exit code", func(t *testing.T) {
		out, err := d.Exec(ctx, id, []string{"sh", "-c", "echo out; echo err >&2; exit 3"})
		var exit *provider.ExitError
		if !errors.As(err, &exit) || exit.Code != 3 {
			t.Fatalf("Exec() error = %v, want exit code 3", err)
		}
		if out != "out\nerr\n" && out != "err\nout\n" {
			t.Errorf("Exec() output = %q, want out and err", out)
		}
	})

	t.Run("command not found", func(t *testing.T) {
		_, err := d.Exec(ctx, id, []string{"no-such-command"})
		var exit *provider.ExitError
		if !errors.As(err, &exit) || exit.Code == 0 {
			t.Errorf("Exec() error = %v, want non-zero exit", err)
		}
	})

	t.Run("output capped", func(t *testing.T) {
		out, err := d.Exec(ctx, id, []string{"sh", "-c", "head -c 3000000 /dev/zero"})
		if err != nil {
			t.Fatalf("Exec() error = %v", err)
		}
		if len(out) != maxExecOutput {
			t.Errorf("len(output) = %d, want %d", len(out), maxExecOutput)
		}
	})

	t.Run("context cancelled", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := d.Exec(ctx, id, []string{"sleep", "30"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Exec() error = %v, want deadline exceeded", err)
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Errorf("Exec() took %s after cancel", elapsed)
		}
	})
}

func TestExecEmptyCommand(t *testing.T) {
	d := testDocker(t)
	if _, err := d.Exec(context.Background(), "x", nil); !errors.Is(err, provider.ErrEmptyCommand) {
		t.Errorf("Exec() error = %v, want ErrEmptyCommand", err)
	}
}

func TestExecMissing(t *testing.T) {
	d := testDocker(t)
	_, err := d.Exec(context.Background(), newEnvID(t, d), []string{"true"})
	if err == nil || !strings.Contains(err.Error(), "create") {
		t.Errorf("Exec() error = %v, want create error", err)
	}
}
