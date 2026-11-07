package docker

import (
	"context"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func startProxy(t *testing.T, d *Docker) string {
	t.Helper()
	ctx := context.Background()
	if err := d.ensureImage(ctx, testImage); err != nil {
		t.Fatalf("ensure image: %v", err)
	}

	name := "eem-test-proxy-" + uuid.NewString()
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   name,
		Config: &container.Config{Image: testImage},
	})
	if err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	t.Cleanup(func() {
		d.cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})

	if _, err := d.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start proxy: %v", err)
	}

	return name
}

func proxyAttached(t *testing.T, d *Docker, id, proxy string) bool {
	t.Helper()
	n, err := d.cli.NetworkInspect(context.Background(), networkName(id), client.NetworkInspectOptions{})
	if err != nil {
		t.Fatalf("inspect network: %v", err)
	}
	for _, c := range n.Network.Containers {
		if c.Name == proxy {
			return true
		}
	}
	return false
}

func routedTemplate() provider.Template {
	return provider.Template{
		Workload: provider.Workload{
			Ports:     []int{6379, 6380},
			Resources: provider.Resources{CPUCore: 1, RAMGB: 1},
		},
	}
}

func TestCreateConnectsProxy(t *testing.T) {
	d := testDocker(t)
	d.proxy = startProxy(t, d)
	ctx := context.Background()
	id := newEnvID(t, d)

	if _, err := d.Create(ctx, id, testImage, routedTemplate()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if !proxyAttached(t, d, id, d.proxy) {
		t.Fatalf("proxy %s not attached to %s", d.proxy, networkName(id))
	}

	addr, err := d.Address(ctx, id)
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	if want := workloadName(id) + ":6379"; addr != want {
		t.Errorf("Address() = %q, want %q", addr, want)
	}

	reached := false
	for range 20 {
		if execExitCode(t, d, d.proxy, "redis-cli", "-h", workloadName(id), "-p", "6379", "ping") == 0 {
			reached = true
			break
		}
	}
	if !reached {
		t.Errorf("proxy cannot reach workload at %s", addr)
	}
}

func TestDeleteDisconnectsProxy(t *testing.T) {
	d := testDocker(t)
	d.proxy = startProxy(t, d)
	ctx := context.Background()
	id := newEnvID(t, d)

	if _, err := d.Create(ctx, id, testImage, routedTemplate()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := d.Delete(ctx, id); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := d.cli.NetworkInspect(ctx, networkName(id), client.NetworkInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("network inspect error = %v, want not found", err)
	}

	p, err := d.cli.ContainerInspect(ctx, d.proxy, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect proxy: %v", err)
	}
	if !p.Container.State.Running {
		t.Error("proxy stopped after Delete(), want running")
	}
}

func TestDeleteWithProxyNotAttached(t *testing.T) {
	d := testDocker(t)
	ctx := context.Background()
	id := newEnvID(t, d)

	if _, err := d.Create(ctx, id, testImage, routedTemplate()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	d.proxy = startProxy(t, d)
	if err := d.Delete(ctx, id); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := d.cli.NetworkInspect(ctx, networkName(id), client.NetworkInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("network inspect error = %v, want not found", err)
	}
}

func TestCreateMissingProxyRollsBack(t *testing.T) {
	d := testDocker(t)
	d.proxy = "eem-test-proxy-missing-" + uuid.NewString()
	ctx := context.Background()
	id := newEnvID(t, d)

	if _, err := d.Create(ctx, id, testImage, routedTemplate()); err == nil {
		t.Fatal("Create() error = nil, want error")
	}

	if _, err := d.cli.NetworkInspect(ctx, networkName(id), client.NetworkInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("network inspect error = %v, want not found", err)
	}

	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", envLabel+"="+id),
	})
	if err != nil {
		t.Fatalf("list containers: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("containers left after failure = %d, want 0", len(list.Items))
	}
}

func TestAddressNoPorts(t *testing.T) {
	d := testDocker(t)
	ctx := context.Background()
	id := newEnvID(t, d)

	tpl := routedTemplate()
	tpl.Workload.Ports = nil
	if _, err := d.Create(ctx, id, testImage, tpl); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	addr, err := d.Address(ctx, id)
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	if addr != "" {
		t.Errorf("Address() = %q, want empty", addr)
	}
}

func TestAddressMissing(t *testing.T) {
	d := testDocker(t)
	if _, err := d.Address(context.Background(), uuid.NewString()); !cerrdefs.IsNotFound(err) {
		t.Errorf("Address() error = %v, want not found", err)
	}
}
