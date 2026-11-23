package deployment

import (
	"context"
	"fmt"
	"time"

	"deployment-agent/internal/docker"
)

const stabilityWindow = 5 * time.Second

// WaitHealthy waits until the container is healthy, or until it has stayed
// running without a restart when it has no healthcheck.
func WaitHealthy(ctx context.Context, inspect func(context.Context) (docker.Container, error), timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stableSince time.Time
	var stableRestarts int
	for {
		ctr, err := inspect(deadline)
		if err != nil {
			return err
		}
		switch ctr.Health {
		case "healthy":
			return nil
		case "unhealthy":
			return fmt.Errorf("container %s is unhealthy", ctr.Name)
		}
		if ctr.Health == "" || ctr.Health == "none" {
			if ctr.Running && !ctr.Restarting {
				if stableSince.IsZero() || ctr.RestartCount != stableRestarts {
					stableSince = time.Now()
					stableRestarts = ctr.RestartCount
				}
				if time.Since(stableSince) >= stabilityWindow {
					return nil
				}
			} else {
				stableSince = time.Time{}
			}
		}
		select {
		case <-deadline.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("container %s did not become healthy within %s", ctr.Name, timeout)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
