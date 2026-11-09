package docker

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/ademolahh/ephemeral-env-manager/internal/dbtest"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

const testImage = "redis:7-alpine"

func testDocker(t *testing.T) *Docker {
	t.Helper()
	dbtest.RequireDocker(t)
	d, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { d.cli.Close() })
	if _, err := d.cli.Ping(context.Background(), client.PingOptions{}); err != nil {
		t.Fatalf("ping docker: %v", err)
	}
	return d
}

func newEnvID(t *testing.T, d *Docker) string {
	t.Helper()
	id := uuid.New().String()
	t.Cleanup(func() {
		if err := d.cleanup(context.Background(), id); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return id
}

func execExitCode(t *testing.T, d *Docker, containerID string, cmd ...string) int {
	t.Helper()
	ctx := context.Background()
	exec, err := d.cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{Cmd: cmd})
	if err != nil {
		t.Fatalf("exec create %v: %v", cmd, err)
	}
	if _, err := d.cli.ExecStart(ctx, exec.ID, client.ExecStartOptions{Detach: true}); err != nil {
		t.Fatalf("exec start %v: %v", cmd, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := d.cli.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
		if err != nil {
			t.Fatalf("exec inspect %v: %v", cmd, err)
		}
		if !res.Running {
			return res.ExitCode
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("exec %v did not finish", cmd)
	return -1
}

func TestCreate(t *testing.T) {
	d := testDocker(t)
	ctx := context.Background()
	id := newEnvID(t, d)

	tpl := provider.Template{
		Workload: provider.Workload{
			Ports:       []int{6379},
			Resources:   provider.Resources{CPUCore: 1, RAMGB: 1, DiskGB: 1},
			Env:         map[string]string{"APP_ENV": "test", "CACHE_URL": "redis://cache:6379"},
			Healthcheck: "redis-cli ping",
		},
		Services: []provider.Service{
			{
				Name:        "cache",
				Image:       testImage,
				Resources:   provider.Resources{CPUCore: 1, RAMGB: 2, DiskGB: 1},
				Env:         map[string]string{"CACHE_MODE": "test"},
				Healthcheck: "redis-cli ping",
			},
		},
	}

	got, err := d.Create(ctx, id, testImage, tpl)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	w, err := d.cli.ContainerInspect(ctx, got, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect workload: %v", err)
	}
	wc := w.Container
	if wc.Name != "/eem-"+id+"-workload" {
		t.Errorf("workload name = %q, want %q", wc.Name, "/eem-"+id+"-workload")
	}
	if !wc.State.Running {
		t.Errorf("workload state = %q, want running", wc.State.Status)
	}
	if wc.Config.Labels[envLabel] != id {
		t.Errorf("workload label %s = %q, want %q", envLabel, wc.Config.Labels[envLabel], id)
	}
	if wc.HostConfig.NanoCPUs != 1e9 {
		t.Errorf("workload NanoCPUs = %d, want %d", wc.HostConfig.NanoCPUs, int64(1e9))
	}
	if wc.HostConfig.Memory != 1<<30 {
		t.Errorf("workload Memory = %d, want %d", wc.HostConfig.Memory, 1<<30)
	}
	for _, e := range []string{"APP_ENV=test", "CACHE_URL=redis://cache:6379"} {
		if !slices.Contains(wc.Config.Env, e) {
			t.Errorf("workload env missing %q", e)
		}
	}
	if wc.Config.Healthcheck == nil || !slices.Equal(wc.Config.Healthcheck.Test, []string{"CMD-SHELL", "redis-cli ping"}) {
		t.Errorf("workload healthcheck = %+v, want CMD-SHELL redis-cli ping", wc.Config.Healthcheck)
	}
	if _, ok := wc.Config.ExposedPorts[network.MustParsePort("6379/tcp")]; !ok {
		t.Errorf("workload exposed ports = %v, want 6379/tcp", wc.Config.ExposedPorts)
	}

	s, err := d.cli.ContainerInspect(ctx, "eem-"+id+"-cache", client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect service: %v", err)
	}
	sc := s.Container
	if !sc.State.Running {
		t.Errorf("service state = %q, want running", sc.State.Status)
	}
	if sc.Config.Labels[envLabel] != id {
		t.Errorf("service label %s = %q, want %q", envLabel, sc.Config.Labels[envLabel], id)
	}
	if sc.HostConfig.Memory != 2<<30 {
		t.Errorf("service Memory = %d, want %d", sc.HostConfig.Memory, 2<<30)
	}
	if !slices.Contains(sc.Config.Env, "CACHE_MODE=test") {
		t.Errorf("service env missing CACHE_MODE=test")
	}
	if sc.Config.Healthcheck == nil || !slices.Equal(sc.Config.Healthcheck.Test, []string{"CMD-SHELL", "redis-cli ping"}) {
		t.Errorf("service healthcheck = %+v, want CMD-SHELL redis-cli ping", sc.Config.Healthcheck)
	}
	ep := sc.NetworkSettings.Networks[networkName(id)]
	if ep == nil || !slices.Contains(ep.Aliases, "cache") {
		t.Errorf("service endpoint on %s = %+v, want alias cache", networkName(id), ep)
	}

	n, err := d.cli.NetworkInspect(ctx, networkName(id), client.NetworkInspectOptions{})
	if err != nil {
		t.Fatalf("inspect network: %v", err)
	}
	if !n.Network.Internal {
		t.Error("network is not internal")
	}
	if n.Network.Labels[envLabel] != id {
		t.Errorf("network label %s = %q, want %q", envLabel, n.Network.Labels[envLabel], id)
	}

	reached := false
	for range 20 {
		if execExitCode(t, d, got, "redis-cli", "-h", "cache", "ping") == 0 {
			reached = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !reached {
		t.Error("workload cannot reach service by name cache")
	}

	if code := execExitCode(t, d, got, "wget", "-q", "-T", "3", "-O", "/dev/null", "http://1.1.1.1"); code == 0 {
		t.Error("workload reached the internet, want isolated network")
	}
}

func TestCreateNoServices(t *testing.T) {
	d := testDocker(t)
	ctx := context.Background()
	id := newEnvID(t, d)

	got, err := d.Create(ctx, id, testImage, provider.Template{
		Workload: provider.Workload{Resources: provider.Resources{CPUCore: 1, RAMGB: 1}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", envLabel+"="+id),
	})
	if err != nil {
		t.Fatalf("list containers: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != got {
		t.Errorf("containers = %d, want only workload %s", len(list.Items), got)
	}
}

func TestCreateRollsBackOnFailure(t *testing.T) {
	d := testDocker(t)
	ctx := context.Background()
	id := newEnvID(t, d)

	got, err := d.Create(ctx, id, testImage, provider.Template{
		Workload: provider.Workload{Resources: provider.Resources{CPUCore: 1, RAMGB: 1}},
		Services: []provider.Service{
			{Name: "cache", Image: testImage, Resources: provider.Resources{CPUCore: 1, RAMGB: 1}},
			{Name: "broken", Image: "eem.invalid/missing:never", Resources: provider.Resources{CPUCore: 1, RAMGB: 1}},
		},
	})
	if err == nil {
		t.Fatal("Create() error = nil, want error")
	}
	if got != "" {
		t.Errorf("Create() id = %q, want empty", got)
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
	if _, err := d.cli.NetworkInspect(ctx, networkName(id), client.NetworkInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("network inspect error = %v, want not found", err)
	}
}

func TestEnvList(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{name: "nil", env: nil, want: []string{}},
		{name: "sorted", env: map[string]string{"B": "2", "A": "1", "C": "x=y"}, want: []string{"A=1", "B=2", "C=x=y"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := envList(tt.env); !slices.Equal(got, tt.want) {
				t.Errorf("envList() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreateRejectsUnlimitedResources(t *testing.T) {
	d := testDocker(t)
	ctx := context.Background()

	tests := []struct {
		name string
		tpl  provider.Template
	}{
		{
			name: "zero workload cpu",
			tpl:  provider.Template{Workload: provider.Workload{Resources: provider.Resources{RAMGB: 1}}},
		},
		{
			name: "zero service ram",
			tpl: provider.Template{
				Workload: provider.Workload{Resources: provider.Resources{CPUCore: 1, RAMGB: 1}},
				Services: []provider.Service{{Name: "cache", Image: testImage, Resources: provider.Resources{CPUCore: 1}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := newEnvID(t, d)

			_, err := d.Create(ctx, id, testImage, tt.tpl)
			if !errors.Is(err, ErrUnlimitedResources) {
				t.Fatalf("Create() error = %v, want ErrUnlimitedResources", err)
			}

			list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
				All:     true,
				Filters: client.Filters{}.Add("label", envLabel+"="+id),
			})
			if err != nil {
				t.Fatalf("list containers: %v", err)
			}
			if len(list.Items) != 0 {
				t.Errorf("containers left = %d, want 0", len(list.Items))
			}
		})
	}
}

func TestDelete(t *testing.T) {
	d := testDocker(t)
	ctx := context.Background()
	id := newEnvID(t, d)

	if _, err := d.Create(ctx, id, testImage, provider.Template{
		Workload: provider.Workload{Resources: provider.Resources{CPUCore: 1, RAMGB: 1}},
		Services: []provider.Service{{Name: "cache", Image: testImage, Resources: provider.Resources{CPUCore: 1, RAMGB: 1}}},
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := d.Delete(ctx, id); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", envLabel+"="+id),
	})
	if err != nil {
		t.Fatalf("list containers: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("containers left = %d, want 0", len(list.Items))
	}
	if _, err := d.cli.NetworkInspect(ctx, networkName(id), client.NetworkInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("network inspect error = %v, want not found", err)
	}

	if err := d.Delete(ctx, id); err != nil {
		t.Errorf("Delete() again error = %v, want nil", err)
	}
	if err := d.Delete(ctx, uuid.NewString()); err != nil {
		t.Errorf("Delete() unknown env error = %v, want nil", err)
	}
}

func TestList(t *testing.T) {
	d := testDocker(t)
	ctx := context.Background()
	full := newEnvID(t, d)
	networkOnly := newEnvID(t, d)

	if _, err := d.Create(ctx, full, testImage, provider.Template{
		Workload: provider.Workload{Resources: provider.Resources{CPUCore: 1, RAMGB: 1}},
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := d.cli.NetworkCreate(ctx, networkName(networkOnly), client.NetworkCreateOptions{
		Driver:   "bridge",
		Internal: true,
		Labels:   map[string]string{envLabel: networkOnly},
	}); err != nil {
		t.Fatalf("create network: %v", err)
	}

	got, err := d.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, id := range []string{full, networkOnly} {
		if !slices.Contains(got, id) {
			t.Errorf("List() = %v, want it to contain %s", got, id)
		}
	}
	if len(slices.Compact(slices.Clone(got))) != len(got) {
		t.Errorf("List() = %v, want each env once", got)
	}

	for _, id := range []string{full, networkOnly} {
		if err := d.Delete(ctx, id); err != nil {
			t.Fatalf("Delete(%s) error = %v", id, err)
		}
	}
	got, err = d.List(ctx)
	if err != nil {
		t.Fatalf("List() after delete error = %v", err)
	}
	for _, id := range []string{full, networkOnly} {
		if slices.Contains(got, id) {
			t.Errorf("List() after delete = %v, still contains %s", got, id)
		}
	}
}

func TestAggregateStatus(t *testing.T) {
	const id = "env-1"
	workload := func(state container.ContainerState, health container.HealthStatus) container.Summary {
		c := container.Summary{Names: []string{"/eem-" + id + "-workload"}, State: state}
		if health != "" {
			c.Health = &container.HealthSummary{Status: health}
		}
		return c
	}
	service := func(state container.ContainerState, health container.HealthStatus) container.Summary {
		c := workload(state, health)
		c.Names = []string{"/eem-" + id + "-cache"}
		return c
	}
	node := func(c container.Summary) container.Summary {
		c.Labels = map[string]string{nodeLabel: "keel"}
		return c
	}
	running := node(service(container.StateRunning, ""))
	exited := node(service(container.StateExited, ""))

	tests := []struct {
		name       string
		containers []container.Summary
		want       provider.Status
	}{
		{name: "no containers", want: provider.StatusMissing},
		{name: "only a service", containers: []container.Summary{service(container.StateRunning, "")}, want: provider.StatusMissing},
		{name: "running without healthchecks", containers: []container.Summary{workload(container.StateRunning, "")}, want: provider.StatusRunning},
		{name: "all healthy", containers: []container.Summary{workload(container.StateRunning, container.NoHealthcheck), service(container.StateRunning, container.Healthy)}, want: provider.StatusRunning},
		{name: "service starting", containers: []container.Summary{workload(container.StateRunning, ""), service(container.StateRunning, container.Starting)}, want: provider.StatusStarting},
		{name: "unhealthy beats starting", containers: []container.Summary{workload(container.StateRunning, container.Starting), service(container.StateRunning, container.Unhealthy)}, want: provider.StatusUnhealthy},
		{name: "service exited", containers: []container.Summary{workload(container.StateRunning, ""), service(container.StateExited, "")}, want: provider.StatusStopped},
		{name: "workload created not started", containers: []container.Summary{workload(container.StateCreated, "")}, want: provider.StatusStopped},
		{name: "all nodes up", containers: []container.Summary{node(workload(container.StateRunning, "")), running, running}, want: provider.StatusRunning},
		{name: "workload node down, majority up", containers: []container.Summary{node(workload(container.StateExited, "")), running, running}, want: provider.StatusDegraded},
		{name: "unhealthy node, majority up", containers: []container.Summary{node(workload(container.StateRunning, container.Unhealthy)), running, running}, want: provider.StatusDegraded},
		{name: "node down beats starting", containers: []container.Summary{node(workload(container.StateRunning, container.Starting)), running, exited}, want: provider.StatusDegraded},
		{name: "half the nodes down", containers: []container.Summary{node(workload(container.StateRunning, "")), exited}, want: provider.StatusStopped},
		{name: "majority down", containers: []container.Summary{node(workload(container.StateExited, "")), running, exited}, want: provider.StatusStopped},
		{name: "non-node service down with nodes up", containers: []container.Summary{node(workload(container.StateRunning, "")), running, running, service(container.StateExited, "")}, want: provider.StatusStopped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aggregateStatus(id, tt.containers); got != tt.want {
				t.Errorf("aggregateStatus() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestStatus(t *testing.T) {
	d := testDocker(t)
	ctx := context.Background()
	id := newEnvID(t, d)

	if got, err := d.Status(ctx, id); err != nil || got != provider.StatusMissing {
		t.Fatalf("Status() before create = %s, %v, want missing", got, err)
	}

	if _, err := d.Create(ctx, id, testImage, provider.Template{
		Workload: provider.Workload{Resources: provider.Resources{CPUCore: 1, RAMGB: 1}},
		Services: []provider.Service{{Name: "cache", Image: testImage, Resources: provider.Resources{CPUCore: 1, RAMGB: 1}, Healthcheck: "redis-cli ping"}},
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	var got provider.Status
	for time.Now().Before(deadline) {
		var err error
		if got, err = d.Status(ctx, id); err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if got == provider.StatusRunning {
			break
		}
		if got != provider.StatusStarting {
			t.Fatalf("Status() while starting = %s, want starting or running", got)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if got != provider.StatusRunning {
		t.Fatalf("Status() = %s, want running once healthy", got)
	}

	if _, err := d.cli.ContainerStop(ctx, "eem-"+id+"-cache", client.ContainerStopOptions{}); err != nil {
		t.Fatalf("stop service: %v", err)
	}
	if got, err := d.Status(ctx, id); err != nil || got != provider.StatusStopped {
		t.Errorf("Status() after service stopped = %s, %v, want stopped", got, err)
	}

	if err := d.Delete(ctx, id); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got, err := d.Status(ctx, id); err != nil || got != provider.StatusMissing {
		t.Errorf("Status() after delete = %s, %v, want missing", got, err)
	}
}
