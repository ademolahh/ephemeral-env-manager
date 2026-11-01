package env

import (
	"context"
	"os"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider/docker"
)

const testImage = "redis:7-alpine"

func dockerClient(t *testing.T) *client.Client {
	t.Helper()
	if os.Getenv("TEST_DOCKER") == "" {
		t.Skip("TEST_DOCKER not set")
	}
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { cli.Close() })
	return cli
}

func envContainers(t *testing.T, cli *client.Client, id string) client.ContainerListResult {
	t.Helper()
	list, err := cli.ContainerList(context.Background(), client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", "eem.env="+id),
	})
	if err != nil {
		t.Fatalf("list containers: %v", err)
	}
	return list
}

func removeEnv(t *testing.T, cli *client.Client, id string) {
	t.Helper()
	ctx := context.Background()
	for _, c := range envContainers(t, cli, id).Items {
		if _, err := cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !cerrdefs.IsNotFound(err) {
			t.Errorf("remove container %s: %v", c.ID, err)
		}
	}
	if _, err := cli.NetworkRemove(ctx, "eem-"+id, client.NetworkRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
		t.Errorf("remove network: %v", err)
	}
}

func TestCreateDocker(t *testing.T) {
	cli := dockerClient(t)
	tx, callerID := setup(t)
	ctx := context.Background()

	d, err := docker.New()
	if err != nil {
		t.Fatalf("docker.New() error = %v", err)
	}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: d})

	tpl := provider.Template{
		Name:            "cache-only",
		Version:         "v1",
		Providers:       []provider.Type{provider.Docker},
		DefaultProvider: provider.Docker,
		TTL:             time.Hour,
		Workload:        provider.Workload{Resources: provider.Resources{CPUCore: 1, RAMGB: 1, DiskGB: 1}},
		Services: []provider.Service{
			{Name: "cache", Image: testImage, Resources: provider.Resources{CPUCore: 1, RAMGB: 1, DiskGB: 1}},
		},
	}

	created, err := m.Create(ctx, CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: testImage,
		Template: tpl,
	})
	m.Wait()
	id := created.ID
	if id != uuid.Nil {
		t.Cleanup(func() { removeEnv(t, cli, id.String()) })
	}
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	env, err := db.GetEnvironment(ctx, tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if env.Status != db.EnvRunning {
		t.Errorf("status = %s, want %s", env.Status, db.EnvRunning)
	}

	list := envContainers(t, cli, id.String())
	if len(list.Items) != 2 {
		t.Fatalf("containers = %d, want 2", len(list.Items))
	}
	for _, c := range list.Items {
		if c.State != "running" {
			t.Errorf("container %v state = %q, want running", c.Names, c.State)
		}
	}
}

func TestCreateDockerFailure(t *testing.T) {
	cli := dockerClient(t)
	tx, callerID := setup(t)
	ctx := context.Background()

	d, err := docker.New()
	if err != nil {
		t.Fatalf("docker.New() error = %v", err)
	}
	m := NewManager(tx, map[provider.Type]provider.Provider{provider.Docker: d})

	created, err := m.Create(ctx, CreateRequest{
		CallerID: callerID,
		Type:     db.EnvRegularTesting,
		Workload: "eem.invalid/missing:never",
		Template: provider.Template{
			Name:            "broken",
			Version:         "v1",
			DefaultProvider: provider.Docker,
			TTL:             time.Hour,
			Workload:        provider.Workload{Resources: provider.Resources{CPUCore: 1, RAMGB: 1}},
		},
	})
	m.Wait()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	id := created.ID
	t.Cleanup(func() { removeEnv(t, cli, id.String()) })

	env, err := db.GetEnvironment(ctx, tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if env.Status != db.EnvFailed {
		t.Errorf("status = %s, want %s", env.Status, db.EnvFailed)
	}
	if env.FailureReason == nil || *env.FailureReason == "" {
		t.Error("failure_reason empty, want provider error")
	}
	if n := len(envContainers(t, cli, id.String()).Items); n != 0 {
		t.Errorf("containers left after failure = %d, want 0", n)
	}
}
