package supervisor

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"deployment-agent/internal/docker"
	"deployment-agent/internal/registry"
)

// Handoff updates the agent container without stopping the old process until
// the replacement is up. exit is called only when this process is that container.
type Handoff struct {
	Eng           docker.Engine
	Resolver      registry.Resolver
	Log           *slog.Logger
	SelfName      string
	Hostname      string
	StopTimeout   time.Duration
	HealthTimeout time.Duration
	Exit          func(int)
	Wait          func(ctx context.Context, id string) error
}

// Container is the agent container, copied from the live host model.
type Container struct {
	ID            string
	Name          string
	Image         string
	CurrentDigest string
}

// Run pulls a newer digest of the agent image and starts the replacement.
func (h Handoff) Run(ctx context.Context, containers []Container) error {
	self, ok := findSelf(containers, h.SelfName, h.Hostname)
	if !ok {
		h.log().Info("agent is not a container; self-update skipped")
		return nil
	}
	ref, err := registry.Parse(self.Image)
	if err != nil || ref.Pinned {
		h.log().Info("agent image is not a tracked tag", "image", self.Image)
		return nil
	}
	digest, err := h.Resolver.Digest(ctx, self.Image)
	if err != nil {
		return err
	}
	if digest == self.CurrentDigest {
		return nil
	}
	auth, err := h.Resolver.PullAuth(ctx, self.Image)
	if err != nil {
		return err
	}
	if err := h.Eng.PullAndTag(ctx, self.Image, digest, auth); err != nil {
		return err
	}
	res, err := h.Eng.Handoff(ctx, self.ID, map[string]string{
		"deployment-agent.previous-digest": self.CurrentDigest,
		"deployment-agent.service":         self.Name,
	})
	if err != nil {
		return err
	}
	if err := h.wait(ctx, res.NewID); err != nil {
		if rerr := h.Eng.Restore(ctx, res); rerr != nil {
			h.log().Error("self-update rollback failed", "err", rerr)
		}
		return err
	}
	inside := h.Hostname != "" && strings.HasPrefix(self.ID, h.Hostname)
	if inside {
		h.log().Info("replacement agent is up; exiting")
		exit := h.Exit
		if exit == nil {
			exit = os.Exit
		}
		exit(0)
		return nil
	}
	return h.Eng.Remove(ctx, res.PreviousID)
}

func (h Handoff) wait(ctx context.Context, id string) error {
	if h.Wait != nil {
		return h.Wait(ctx, id)
	}
	deadline := time.Now().Add(h.HealthTimeout)
	if h.HealthTimeout <= 0 {
		deadline = time.Now().Add(60 * time.Second)
	}
	for time.Now().Before(deadline) {
		ctr, err := h.Eng.Inspect(ctx, id)
		if err == nil && ctr.Running && ctr.Health != "unhealthy" && ctr.Health != "starting" {
			if ctr.Health == "healthy" || ctr.Health == "" || ctr.Health == "none" {
				return nil
			}
		}
		if err == nil && ctr.Health == "unhealthy" {
			return errUnhealthy
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return errUnhealthy
}

func (h Handoff) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

func findSelf(containers []Container, selfName, hostname string) (Container, bool) {
	if hostname != "" {
		for _, ctr := range containers {
			if strings.HasPrefix(ctr.ID, hostname) {
				return ctr, true
			}
		}
	}
	for _, ctr := range containers {
		if selfName != "" && ctr.Name == selfName {
			return ctr, true
		}
	}
	return Container{}, false
}

type unhealthyError struct{}

func (unhealthyError) Error() string { return "replacement agent is unhealthy" }

var errUnhealthy = unhealthyError{}
