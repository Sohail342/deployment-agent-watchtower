package docker

import (
	"context"
	"time"
)

const PreviousSuffix = ".deployment-agent-previous"

// Container is the engine view of one container, including stopped ones.
type Container struct {
	ID           string
	Name         string
	Image        string
	ImageID      string
	State        string
	Running      bool
	Restarting   bool
	RestartCount int
	Health       string
	NetworkMode  string
	Labels       map[string]string
	Mounts       []string
	Networks     []string
}

// Image is one image record from the daemon.
type Image struct {
	ID          string
	RepoTags    []string
	RepoDigests []string
}

// Event is one Docker event the inventory resyncs on.
type Event struct {
	Type   string
	Action string
	ID     string
}

// RecreateResult identifies the containers involved in a replacement.
type RecreateResult struct {
	NewID        string
	PreviousID   string
	OriginalName string
	PreviousName string
}

// ComposeSpec is a Compose CLI invocation. Files are passed in order.
type ComposeSpec struct {
	Files   []string
	Project string
	WorkDir string
	Wait    time.Duration
}

// Engine is the local Docker daemon. The agent never opens a deploy port.
type Engine interface {
	Ping(ctx context.Context) error
	ListContainers(ctx context.Context) ([]Container, error)
	ListImages(ctx context.Context) ([]Image, error)
	Events(ctx context.Context) (<-chan Event, <-chan error)
	Inspect(ctx context.Context, id string) (Container, error)
	PullAndTag(ctx context.Context, tagRef, digest, authHeader string) error
	Recreate(ctx context.Context, id string, labels map[string]string, stopTimeout time.Duration) (RecreateResult, error)
	Restore(ctx context.Context, res RecreateResult) error
	Handoff(ctx context.Context, id string, labels map[string]string) (RecreateResult, error)
	Remove(ctx context.Context, id string) error
	Recover(ctx context.Context) error
	Login(ctx context.Context, host, username, password string) error
	ComposeUp(ctx context.Context, bin string, spec ComposeSpec) error
}
