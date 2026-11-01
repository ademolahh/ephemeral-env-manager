package docker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

const envLabel = "eem.env"

var ErrUnlimitedResources = errors.New("cpu and ram limits must be at least 1, docker treats 0 as unlimited")

type Docker struct {
	cli *client.Client
}

func New() (*Docker, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Docker{cli: cli}, nil
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

	var wg sync.WaitGroup
	errs := make([]error, len(temp.Services))
	for i, s := range temp.Services {
		wg.Go(func() {
			cfg := &container.Config{
				Image:  s.Image,
				Env:    envList(s.Env),
				Labels: labels,
			}
			if s.Healthcheck != "" {
				cfg.Healthcheck = &container.HealthConfig{
					Test:     []string{"CMD-SHELL", s.Healthcheck},
					Interval: 5 * time.Second,
					Timeout:  5 * time.Second,
					Retries:  5,
				}
			}

			if _, err := d.run(ctx, id+"-"+s.Name, net, s.Name, cfg, s.Resources); err != nil {
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

	containerID, err := d.run(ctx, id+"-workload", net, "workload", &container.Config{
		Image:        workload,
		Env:          envList(temp.Workload.Env),
		ExposedPorts: ports,
		Labels:       labels,
	}, temp.Workload.Resources)
	if err != nil {
		return "", fmt.Errorf("workload: %w", err)
	}

	return containerID, nil
}

func (d *Docker) run(ctx context.Context, name, net, alias string, cfg *container.Config, res provider.Resources) (string, error) {
	if res.CPUCore < 1 || res.RAMGB < 1 {
		return "", fmt.Errorf("%w: cpu_core %d, ram_gb %d", ErrUnlimitedResources, res.CPUCore, res.RAMGB)
	}

	if err := d.ensureImage(ctx, cfg.Image); err != nil {
		return "", err
	}

	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   "eem-" + name,
		Config: cfg,
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(net),
			NanoCPUs:    int64(res.CPUCore) * 1e9,
			Memory:      int64(res.RAMGB) << 30,
		},
		NetworkingConfig: &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				net: {Aliases: []string{alias}},
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

	var errs []error
	for _, c := range list.Items {
		if _, err := d.cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("remove container %s: %w", c.ID, err))
		}
	}

	if _, err := d.cli.NetworkRemove(ctx, networkName(id), client.NetworkRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
		errs = append(errs, fmt.Errorf("remove network: %w", err))
	}

	return errors.Join(errs...)
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
