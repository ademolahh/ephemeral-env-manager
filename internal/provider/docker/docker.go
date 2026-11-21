package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

const (
	envLabel  = "eem.env"
	portLabel = "eem.port"
	nodeLabel = "eem.node"
)

var ErrUnlimitedResources = errors.New("cpu and ram limits are below the minimum, and docker treats 0 as unlimited")

// DefaultPidsLimit caps processes per container so a fork bomb in one
// environment cannot exhaust the host.
const DefaultPidsLimit = 1024

// droppedCaps are removed from Docker's default capability set. Workloads
// rarely need raw sockets, device nodes, audit writes or file capabilities,
// and dropping more (CHOWN, SETUID, ...) breaks common images such as postgres.
var droppedCaps = []string{"NET_RAW", "MKNOD", "AUDIT_WRITE", "SETFCAP"}

type Docker struct {
	cli       *client.Client
	proxy     string
	pidsLimit int64
	diskQuota bool
}

type Option func(*Docker)

func WithProxy(container string) Option {
	return func(d *Docker) { d.proxy = container }
}

// WithPidsLimit sets the per-container process limit.
func WithPidsLimit(n int64) Option {
	return func(d *Docker) { d.pidsLimit = n }
}

// WithDiskQuota limits each container's writable layer to its disk_gb. It
// needs the overlay2 storage driver on xfs mounted with pquota, so it is off
// by default; without it disk_gb is reserved and priced but not enforced.
func WithDiskQuota() Option {
	return func(d *Docker) { d.diskQuota = true }
}

func New(opts ...Option) (*Docker, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	d := &Docker{cli: cli, pidsLimit: DefaultPidsLimit}
	for _, opt := range opts {
		opt(d)
	}

	return d, nil
}

func (d *Docker) Ping(ctx context.Context) error {
	if _, err := d.cli.Ping(ctx, client.PingOptions{}); err != nil {
		return fmt.Errorf("ping docker: %w", err)
	}
	return nil
}

func (d *Docker) Create(ctx context.Context, id, workload string, temp provider.Template) (string, error) {
	containerID, err := d.create(ctx, id, workload, temp)
	if err != nil {
		if cleanupErr := d.cleanup(context.WithoutCancel(ctx), id); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
		return "", fmt.Errorf("create env %s: %w", id, err)
	}

	return containerID, nil
}

func (d *Docker) Delete(ctx context.Context, id string) error {
	if err := d.cleanup(ctx, id); err != nil {
		return fmt.Errorf("delete env %s: %w", id, err)
	}
	return nil
}

func (d *Docker) Status(ctx context.Context, id string) (provider.Status, error) {
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", envLabel+"="+id),
	})
	if err != nil {
		return "", fmt.Errorf("status env %s: list containers: %w", id, err)
	}

	return aggregateStatus(id, list.Items), nil
}

func (d *Docker) Address(ctx context.Context, id string) (string, error) {
	name := workloadName(id)
	res, err := d.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("address env %s: %w", id, err)
	}

	labels := res.Container.Config.Labels
	port := labels[portLabel]
	if port == "" {
		return "", nil
	}

	if alias := labels[nodeLabel]; alias != "" {
		return scopedAlias(id, alias) + ":" + port, nil
	}

	return name + ":" + port, nil
}

func (d *Docker) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	if len(cmd) == 0 {
		return "", fmt.Errorf("exec env %s: %w", id, provider.ErrEmptyCommand)
	}

	created, err := d.cli.ExecCreate(ctx, workloadName(id), client.ExecCreateOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("exec env %s: create: %w", id, err)
	}

	attached, err := d.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("exec env %s: attach: %w", id, err)
	}
	defer attached.Close()
	defer context.AfterFunc(ctx, attached.Close)()

	out := &cappedBuffer{max: maxExecOutput}
	if _, err := stdcopy.StdCopy(out, out, attached.Reader); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return out.String(), fmt.Errorf("exec env %s: read output: %w", id, err)
	}

	res, err := d.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return out.String(), fmt.Errorf("exec env %s: inspect: %w", id, err)
	}

	if res.ExitCode != 0 {
		return out.String(), fmt.Errorf("exec env %s: %w", id, &provider.ExitError{Code: res.ExitCode})
	}

	return out.String(), nil
}

const maxExecOutput = 1 << 20

type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (d *Docker) List(ctx context.Context) ([]string, error) {
	filters := client.Filters{}.Add("label", envLabel)
	containers, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("list envs: list containers: %w", err)
	}

	networks, err := d.cli.NetworkList(ctx, client.NetworkListOptions{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("list envs: list networks: %w", err)
	}

	seen := map[string]struct{}{}
	for _, c := range containers.Items {
		seen[c.Labels[envLabel]] = struct{}{}
	}

	for _, n := range networks.Items {
		seen[n.Labels[envLabel]] = struct{}{}
	}
	delete(seen, "")

	return slices.Sorted(maps.Keys(seen)), nil
}

func (d *Docker) create(ctx context.Context, id, workload string, temp provider.Template) (string, error) {
	labels := map[string]string{envLabel: id}
	net := networkName(id)

	if _, err := d.cli.NetworkCreate(ctx, net, client.NetworkCreateOptions{
		Driver:   "bridge",
		Internal: true,
		Labels:   labels,
	}); err != nil {
		return "", fmt.Errorf("create network: %w", err)
	}

	if d.proxy != "" {
		_, err := d.cli.NetworkConnect(ctx, net, client.NetworkConnectOptions{Container: d.proxy})
		if err != nil && !cerrdefs.IsPermissionDenied(err) {
			return "", fmt.Errorf("connect proxy %s: %w", d.proxy, err)
		}
	}

	var route string
	if len(temp.Workload.Aliases) > 0 {
		route = temp.Workload.Aliases[0]
	}

	var wg sync.WaitGroup
	errs := make([]error, len(temp.Services))
	for i, s := range temp.Services {
		wg.Go(func() {
			cfg := &container.Config{
				Image:       s.Image,
				Env:         envList(s.Env),
				Labels:      nodeLabels(labels, route, s.Aliases),
				Healthcheck: healthConfig(s.Healthcheck),
			}

			if _, err := d.run(ctx, id+"-"+s.Name, net, aliasList(id, s.Name, s.Aliases), cfg, s.Resources); err != nil {
				errs[i] = fmt.Errorf("service %s: %w", s.Name, err)
			}
		})
	}

	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return "", err
	}

	ports := network.PortSet{}
	for _, p := range temp.Workload.Ports {
		port, err := network.ParsePort(strconv.Itoa(p))
		if err != nil {
			return "", fmt.Errorf("workload port %d: %w", p, err)
		}
		ports[port] = struct{}{}
	}

	workloadLabels := nodeLabels(labels, route, temp.Workload.Aliases)
	if len(temp.Workload.Ports) > 0 {
		workloadLabels[portLabel] = strconv.Itoa(temp.Workload.Ports[0])
	}

	containerID, err := d.run(ctx, id+"-workload", net, aliasList(id, "workload", temp.Workload.Aliases), &container.Config{
		Image:        workload,
		Env:          envList(temp.Workload.Env),
		ExposedPorts: ports,
		Labels:       workloadLabels,
		Healthcheck:  healthConfig(temp.Workload.Healthcheck),
	}, temp.Workload.Resources)
	if err != nil {
		return "", fmt.Errorf("workload: %w", err)
	}

	return containerID, nil
}

func (d *Docker) run(ctx context.Context, name, net string, aliases []string, cfg *container.Config, res provider.Resources) (string, error) {
	if res.CPUMillis < provider.MinCPUMillis || res.RAMMB < provider.MinRAMMB {
		return "", fmt.Errorf("%w: cpu_millis %d (min %d), ram_mb %d (min %d)", ErrUnlimitedResources, res.CPUMillis, provider.MinCPUMillis, res.RAMMB, provider.MinRAMMB)
	}

	if err := d.ensureImage(ctx, cfg.Image); err != nil {
		return "", err
	}

	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:       "eem-" + name,
		Config:     cfg,
		HostConfig: d.hostConfig(net, res),
		NetworkingConfig: &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				net: {Aliases: aliases},
			},
		},
	})

	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}

	if _, err := d.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("start container: %w", err)
	}

	return created.ID, nil
}

func (d *Docker) hostConfig(net string, res provider.Resources) *container.HostConfig {
	hc := &container.HostConfig{
		NetworkMode: container.NetworkMode(net),
		CapDrop:     droppedCaps,
		SecurityOpt: []string{"no-new-privileges:true"},
		LogConfig: container.LogConfig{
			Type:   "json-file",
			Config: map[string]string{"max-size": "10m", "max-file": "3"},
		},
		Resources: container.Resources{
			NanoCPUs:   int64(res.CPUMillis) * 1e6,
			Memory:     int64(res.RAMMB) << 20,
			MemorySwap: int64(res.RAMMB) << 20,
			PidsLimit:  &d.pidsLimit,
		},
	}

	if d.diskQuota {
		hc.StorageOpt = map[string]string{"size": strconv.Itoa(res.DiskGB) + "G"}
	}
	return hc
}

func (d *Docker) ensureImage(ctx context.Context, image string) error {
	_, err := d.cli.ImageInspect(ctx, image)
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect image %s: %w", image, err)
	}
	resp, err := d.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", image, err)
	}
	if err := resp.Wait(ctx); err != nil {
		return fmt.Errorf("pull image %s: %w", image, err)
	}
	return nil
}

func (d *Docker) cleanup(ctx context.Context, id string) error {
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", envLabel+"="+id),
	})

	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, len(list.Items))
	for i, c := range list.Items {
		wg.Go(func() {
			if _, err := d.cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !cerrdefs.IsNotFound(err) {
				errs[i] = fmt.Errorf("remove container %s: %w", c.ID, err)
			}
		})
	}
	wg.Wait()

	if err := d.disconnectProxy(ctx, id); err != nil {
		errs = append(errs, err)
	}

	if _, err := d.cli.NetworkRemove(ctx, networkName(id), client.NetworkRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
		errs = append(errs, fmt.Errorf("remove network: %w", err))
	}

	return errors.Join(errs...)
}

func (d *Docker) disconnectProxy(ctx context.Context, id string) error {
	if d.proxy == "" {
		return nil
	}

	n, err := d.cli.NetworkInspect(ctx, networkName(id), client.NetworkInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("inspect network: %w", err)
	}

	for _, c := range n.Network.Containers {
		if c.Name != d.proxy {
			continue
		}

		_, err := d.cli.NetworkDisconnect(ctx, networkName(id), client.NetworkDisconnectOptions{Container: d.proxy, Force: true})
		if err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("disconnect proxy %s: %w", d.proxy, err)
		}
	}

	return nil
}

func aggregateStatus(id string, containers []container.Summary) provider.Status {
	workload := slices.ContainsFunc(containers, func(c container.Summary) bool {
		return slices.Contains(c.Names, "/"+workloadName(id))
	})
	if !workload {
		return provider.StatusMissing
	}

	status := provider.StatusRunning
	nodes, down := 0, 0
	for _, c := range containers {
		node := c.Labels[nodeLabel] != ""
		if node {
			nodes++
		}

		if c.State != container.StateRunning {
			if !node {
				return provider.StatusStopped
			}
			down++
			continue
		}

		if c.Health == nil {
			continue
		}

		switch c.Health.Status {
		case container.Unhealthy:
			if node {
				down++
				continue
			}
			status = provider.StatusUnhealthy
		case container.Starting:
			if status == provider.StatusRunning {
				status = provider.StatusStarting
			}
		}
	}

	switch {
	case down == 0:
		return status
	case down*2 >= nodes:
		return provider.StatusStopped
	case status == provider.StatusUnhealthy:
		return status
	default:
		return provider.StatusDegraded
	}
}

func healthConfig(cmd string) *container.HealthConfig {
	if cmd == "" {
		return nil
	}

	return &container.HealthConfig{
		Test:          []string{"CMD-SHELL", cmd},
		Interval:      5 * time.Second,
		Timeout:       5 * time.Second,
		Retries:       5,
		StartPeriod:   time.Minute,
		StartInterval: time.Second,
	}
}

func nodeLabels(base map[string]string, route string, aliases []string) map[string]string {
	labels := maps.Clone(base)
	if route != "" && slices.Contains(aliases, route) {
		labels[nodeLabel] = route
	}
	return labels
}

func aliasList(id, name string, aliases []string) []string {
	list := []string{name}
	for _, a := range aliases {
		list = append(list, a, scopedAlias(id, a))
	}
	return list
}

func scopedAlias(id, alias string) string {
	return "eem-" + id + "-" + alias
}

func workloadName(id string) string {
	return "eem-" + id + "-workload"
}

func networkName(id string) string {
	return "eem-" + id
}

func envList(env map[string]string) []string {
	list := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		list = append(list, k+"="+env[k])
	}
	return list
}

// OptionsFromEnv reads DOCKER_PIDS_LIMIT (a positive process count) and
// DOCKER_DISK_QUOTA (true to enforce disk_gb, see WithDiskQuota).
func OptionsFromEnv() ([]Option, error) {
	var opts []Option
	if v := os.Getenv("DOCKER_PIDS_LIMIT"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("DOCKER_PIDS_LIMIT %q must be a positive number", v)
		}
		opts = append(opts, WithPidsLimit(n))
	}

	if v := os.Getenv("DOCKER_DISK_QUOTA"); v != "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("DOCKER_DISK_QUOTA %q must be true or false", v)
		}
		if on {
			opts = append(opts, WithDiskQuota())
		}
	}
	return opts, nil
}
