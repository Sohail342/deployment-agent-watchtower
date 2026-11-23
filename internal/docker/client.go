package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"

	"deployment-agent/internal/registry"
)

// Client talks to the local Docker engine.
type Client struct {
	api *client.Client
	log *slog.Logger
}

var _ Engine = (*Client)(nil)

// Dial connects to the daemon. An empty host uses DOCKER_HOST or the local socket.
func Dial(host string, log *slog.Logger) (*Client, error) {
	opts := []client.Opt{
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
		client.WithUserAgent("deployment-agent"),
	}
	if strings.TrimSpace(host) != "" {
		opts = append(opts, client.WithHost(host))
	}
	api, err := client.New(opts...)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Client{api: api, log: log}, nil
}

func (c *Client) Close() error { return c.api.Close() }

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.api.Ping(ctx, client.PingOptions{})
	return err
}

func (c *Client) ListContainers(ctx context.Context) ([]Container, error) {
	res, err := c.api.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(res.Items))
	for _, item := range res.Items {
		out = append(out, fromSummary(item))
	}
	return out, nil
}

func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	res, err := c.api.ImageList(ctx, client.ImageListOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make([]Image, 0, len(res.Items))
	for _, item := range res.Items {
		out = append(out, fromImage(item))
	}
	return out, nil
}

func (c *Client) Events(ctx context.Context) (<-chan Event, <-chan error) {
	stream := c.api.Events(ctx, client.EventsListOptions{})
	out := make(chan Event)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-stream.Messages:
				if !ok {
					return
				}
				ev := Event{Type: string(msg.Type), Action: string(msg.Action), ID: msg.Actor.ID}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, stream.Err
}

func (c *Client) Inspect(ctx context.Context, id string) (Container, error) {
	res, err := c.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return Container{}, err
	}
	return fromInspect(res.Container), nil
}

func (c *Client) Login(ctx context.Context, host, username, password string) error {
	server := host
	if registry.NormalizeHost(host) == "docker.io" {
		server = "https://index.docker.io/v1/"
	}
	if _, err := c.api.RegistryLogin(ctx, client.RegistryLoginOptions{
		Username:      username,
		Password:      password,
		ServerAddress: server,
	}); err != nil {
		return err
	}
	return registry.SaveDockerConfig(host, username, password)
}

func (c *Client) Remove(ctx context.Context, id string) error {
	_, err := c.api.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	return err
}

func (c *Client) Recover(ctx context.Context) error {
	list, err := c.ListContainers(ctx)
	if err != nil {
		return err
	}
	byName := map[string]Container{}
	for _, ctr := range list {
		byName[ctr.Name] = ctr
	}
	var first error
	for _, ctr := range list {
		original, ok := strings.CutSuffix(ctr.Name, PreviousSuffix)
		if !ok || original == "" {
			continue
		}
		current, hasCurrent := byName[original]
		if !hasCurrent {
			if err := c.renameAndStart(ctx, ctr.ID, original); err != nil && first == nil {
				first = err
			}
			continue
		}
		if ctr.Running {
			continue
		}
		unhealthy := current.Health == string(container.Unhealthy) || (!current.Running && current.State != "created")
		if unhealthy {
			if err := c.Restore(ctx, RecreateResult{
				NewID:        current.ID,
				PreviousID:   ctr.ID,
				OriginalName: original,
				PreviousName: ctr.Name,
			}); err != nil && first == nil {
				first = err
			}
			continue
		}
		if current.Running && !ctr.Running {
			if err := c.Remove(ctx, ctr.ID); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (c *Client) renameAndStart(ctx context.Context, id, name string) error {
	if _, err := c.api.ContainerRename(ctx, id, client.ContainerRenameOptions{NewName: name}); err != nil {
		return err
	}
	_, err := c.api.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

func fromSummary(item container.Summary) Container {
	name := ""
	if len(item.Names) > 0 {
		name = strings.TrimPrefix(item.Names[0], "/")
	}
	health := ""
	if item.Health != nil {
		health = string(item.Health.Status)
	}
	mounts := make([]string, 0, len(item.Mounts))
	for _, m := range item.Mounts {
		mounts = append(mounts, m.Source+":"+m.Destination)
	}
	nets := []string{}
	if item.NetworkSettings != nil {
		nets = slices.Sorted(maps.Keys(item.NetworkSettings.Networks))
	}
	return Container{
		ID:          item.ID,
		Name:        name,
		Image:       item.Image,
		ImageID:     item.ImageID,
		State:       string(item.State),
		Running:     item.State == container.StateRunning,
		Health:      health,
		NetworkMode: item.HostConfig.NetworkMode,
		Labels:      cloneLabels(item.Labels),
		Mounts:      mounts,
		Networks:    nets,
	}
}

func fromInspect(in container.InspectResponse) Container {
	name := strings.TrimPrefix(in.Name, "/")
	health := ""
	running := false
	restarting := false
	state := ""
	if in.State != nil {
		state = string(in.State.Status)
		running = in.State.Running
		restarting = in.State.Restarting
		if in.State.Health != nil {
			health = string(in.State.Health.Status)
		}
	}
	mode := ""
	if in.HostConfig != nil {
		mode = string(in.HostConfig.NetworkMode)
	}
	imageName := ""
	imageID := ""
	if in.Config != nil {
		imageName = in.Config.Image
	}
	if imageName == "" {
		imageName = in.Image
	}
	mounts := make([]string, 0, len(in.Mounts))
	for _, m := range in.Mounts {
		mounts = append(mounts, m.Source+":"+m.Destination)
	}
	nets := []string{}
	if in.NetworkSettings != nil {
		nets = slices.Sorted(maps.Keys(in.NetworkSettings.Networks))
	}
	restart := 0
	if in.RestartCount != 0 {
		restart = in.RestartCount
	}
	return Container{
		ID:           in.ID,
		Name:         name,
		Image:        imageName,
		ImageID:      imageID,
		State:        state,
		Running:      running,
		Restarting:   restarting,
		RestartCount: restart,
		Health:       health,
		NetworkMode:  mode,
		Labels:       labelsFrom(in.Config),
		Mounts:       mounts,
		Networks:     nets,
	}
}

func fromImage(item image.Summary) Image {
	return Image{ID: item.ID, RepoTags: slices.Clone(item.RepoTags), RepoDigests: slices.Clone(item.RepoDigests)}
}

func labelsFrom(cfg *container.Config) map[string]string {
	if cfg == nil {
		return map[string]string{}
	}
	return cloneLabels(cfg.Labels)
}

func cloneLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	return maps.Clone(in)
}

// ListenLocal binds the health address and refuses a wildcard host.
func ListenLocal(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return nil, fmt.Errorf("listen address %s is not local", addr)
	}
	return net.Listen("tcp", addr)
}

func seconds(d time.Duration) int {
	sec := int(d.Seconds())
	if sec < 1 {
		return 1
	}
	return sec
}
