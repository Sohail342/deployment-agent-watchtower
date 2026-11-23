package docker

import (
	"context"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type recreateAPI interface {
	inspect(ctx context.Context, id string) (container.InspectResponse, error)
	stop(ctx context.Context, id string, timeout time.Duration) error
	rename(ctx context.Context, id, name string) error
	remove(ctx context.Context, id string) error
	start(ctx context.Context, id string) error
	create(ctx context.Context, opts client.ContainerCreateOptions) (string, error)
	connect(ctx context.Context, networkName, containerID string, ep *network.EndpointSettings) error
}

type mobyAPI struct{ api *client.Client }

func (m mobyAPI) inspect(ctx context.Context, id string) (container.InspectResponse, error) {
	res, err := m.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, err
	}
	return res.Container, nil
}

func (m mobyAPI) stop(ctx context.Context, id string, timeout time.Duration) error {
	sec := seconds(timeout)
	_, err := m.api.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &sec})
	return err
}

func (m mobyAPI) rename(ctx context.Context, id, name string) error {
	_, err := m.api.ContainerRename(ctx, id, client.ContainerRenameOptions{NewName: name})
	return err
}

func (m mobyAPI) remove(ctx context.Context, id string) error {
	_, err := m.api.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	return err
}

func (m mobyAPI) start(ctx context.Context, id string) error {
	_, err := m.api.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

func (m mobyAPI) create(ctx context.Context, opts client.ContainerCreateOptions) (string, error) {
	res, err := m.api.ContainerCreate(ctx, opts)
	if err != nil {
		return "", err
	}
	return res.ID, nil
}

func (m mobyAPI) connect(ctx context.Context, networkName, containerID string, ep *network.EndpointSettings) error {
	_, err := m.api.NetworkConnect(ctx, networkName, client.NetworkConnectOptions{Container: containerID, EndpointConfig: ep})
	return err
}

// Recreate stops the container, renames it aside, and starts a replacement
// with the same spec. The image tag must already point at the desired digest.
func (c *Client) Recreate(ctx context.Context, id string, labels map[string]string, stopTimeout time.Duration) (RecreateResult, error) {
	return replace(ctx, mobyAPI{api: c.api}, id, labels, true, stopTimeout)
}

// Handoff renames the running container aside and starts the replacement
// without stopping the old process first. Used for the agent container.
func (c *Client) Handoff(ctx context.Context, id string, labels map[string]string) (RecreateResult, error) {
	return replace(ctx, mobyAPI{api: c.api}, id, labels, false, 0)
}

// Restore puts the previous container back and removes the replacement.
func (c *Client) Restore(ctx context.Context, res RecreateResult) error {
	return restore(ctx, mobyAPI{api: c.api}, res, true)
}

func replace(ctx context.Context, api recreateAPI, id string, labels map[string]string, stopFirst bool, stopTimeout time.Duration) (RecreateResult, error) {
	ins, err := api.inspect(ctx, id)
	if err != nil {
		return RecreateResult{}, err
	}
	original := strings.TrimPrefix(ins.Name, "/")
	if original == "" {
		return RecreateResult{}, fmt.Errorf("container %s has no name", id)
	}
	if strings.HasSuffix(original, PreviousSuffix) {
		return RecreateResult{}, fmt.Errorf("container %s is already a previous generation", original)
	}
	if ins.HostConfig != nil && networkKind(ins.HostConfig.NetworkMode) == "container" {
		return RecreateResult{}, fmt.Errorf("container %s uses another container's network", original)
	}
	previous := original + PreviousSuffix
	result := RecreateResult{PreviousID: ins.ID, OriginalName: original, PreviousName: previous}
	if stopFirst {
		if err := api.stop(ctx, ins.ID, stopTimeout); err != nil {
			return RecreateResult{}, fmt.Errorf("stop %s: %w", original, err)
		}
	}
	if err := api.rename(ctx, ins.ID, previous); err != nil {
		if stopFirst {
			_ = api.start(ctx, ins.ID)
		}
		return RecreateResult{}, fmt.Errorf("rename %s: %w", original, err)
	}
	created, err := api.create(ctx, createOptions(ins, original, labels))
	if err != nil {
		_ = restore(ctx, api, result, stopFirst)
		return RecreateResult{}, fmt.Errorf("create %s: %w", original, err)
	}
	result.NewID = created
	if err := connectExtra(ctx, api, ins, created); err != nil {
		_ = restore(ctx, api, result, stopFirst)
		return RecreateResult{}, err
	}
	if err := api.start(ctx, created); err != nil {
		_ = restore(ctx, api, result, stopFirst)
		return RecreateResult{}, fmt.Errorf("start %s: %w", original, err)
	}
	return result, nil
}

func restore(ctx context.Context, api recreateAPI, res RecreateResult, startPrevious bool) error {
	if res.NewID != "" {
		_ = api.remove(ctx, res.NewID)
	}
	if res.PreviousID == "" {
		return nil
	}
	ins, err := api.inspect(ctx, res.PreviousID)
	if err != nil {
		return err
	}
	name := strings.TrimPrefix(ins.Name, "/")
	if name != res.OriginalName {
		if err := api.rename(ctx, res.PreviousID, res.OriginalName); err != nil {
			return err
		}
	}
	if !startPrevious {
		if ins.State != nil && ins.State.Running {
			return nil
		}
	}
	return api.start(ctx, res.PreviousID)
}

func createOptions(ins container.InspectResponse, name string, extra map[string]string) client.ContainerCreateOptions {
	cfg := container.Config{}
	if ins.Config != nil {
		cfg = *ins.Config
	}
	labels := maps.Clone(cfg.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	for k, v := range extra {
		labels[k] = v
	}
	labels["deployment-agent.managed"] = "true"
	cfg.Labels = labels

	host := container.HostConfig{}
	if ins.HostConfig != nil {
		host = *ins.HostConfig
	}
	host.AutoRemove = false

	opts := client.ContainerCreateOptions{
		Name:       name,
		Config:     &cfg,
		HostConfig: &host,
	}
	primary, endpoints := primaryEndpoint(ins)
	if primary != "" {
		host.NetworkMode = container.NetworkMode(primary)
		opts.HostConfig = &host
		opts.NetworkingConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				primary: endpoints,
			},
		}
	}
	return opts
}

func connectExtra(ctx context.Context, api recreateAPI, ins container.InspectResponse, id string) error {
	if ins.NetworkSettings == nil {
		return nil
	}
	primary, _ := primaryEndpoint(ins)
	names := slices.Sorted(maps.Keys(ins.NetworkSettings.Networks))
	for _, name := range names {
		if name == primary {
			continue
		}
		ep := cleanEndpoint(ins.NetworkSettings.Networks[name])
		if err := api.connect(ctx, name, id, ep); err != nil {
			return fmt.Errorf("connect %s to %s: %w", id, name, err)
		}
	}
	return nil
}

func primaryEndpoint(ins container.InspectResponse) (string, *network.EndpointSettings) {
	mode := container.NetworkMode("")
	if ins.HostConfig != nil {
		mode = ins.HostConfig.NetworkMode
	}
	switch networkKind(mode) {
	case "host", "none", "container":
		return "", nil
	}
	if ins.NetworkSettings == nil || len(ins.NetworkSettings.Networks) == 0 {
		return "", nil
	}
	names := slices.Sorted(maps.Keys(ins.NetworkSettings.Networks))
	primary := names[0]
	if networkKind(mode) == "user" {
		want := string(mode)
		for _, name := range names {
			if name == want {
				primary = name
				break
			}
		}
	}
	return primary, cleanEndpoint(ins.NetworkSettings.Networks[primary])
}

// networkKind classifies a network mode from the mode string. The engine API
// helpers differ by the client OS, and this agent is often built on Windows
// while the daemon is Linux.
func networkKind(mode container.NetworkMode) string {
	switch string(mode) {
	case "", "default":
		return "default"
	case "host":
		return "host"
	case "none":
		return "none"
	case "bridge", "nat":
		return "bridge"
	default:
		if strings.HasPrefix(string(mode), "container:") {
			return "container"
		}
		return "user"
	}
}

func cleanEndpoint(in *network.EndpointSettings) *network.EndpointSettings {
	if in == nil {
		return &network.EndpointSettings{}
	}
	ep := in.Copy()
	ep.EndpointID = ""
	ep.NetworkID = ""
	ep.Gateway = netip.Addr{}
	ep.IPAddress = netip.Addr{}
	ep.GlobalIPv6Address = netip.Addr{}
	ep.GlobalIPv6PrefixLen = 0
	ep.IPPrefixLen = 0
	ep.MacAddress = nil
	ep.DNSNames = nil
	ep.IPAMConfig = nil
	return ep
}
