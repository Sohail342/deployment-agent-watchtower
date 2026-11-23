package deployment

import (
	"context"
	"log/slog"
	"time"

	"deployment-agent/internal/docker"
)

// Memory records the digest that last passed a health check.
type Memory interface {
	Begin(key, image, from, to string) error
	Commit(key, digest string) error
	Reject(key, digest string) error
	Abort(key string) error
}

// Resolver supplies the engine pull header for an image.
type Resolver interface {
	PullAuth(ctx context.Context, image string) (string, error)
}

// Runtime is the daemon surface used while applying a plan.
type Runtime interface {
	PullAndTag(ctx context.Context, tagRef, digest, authHeader string) error
	Recreate(ctx context.Context, id string, labels map[string]string, stopTimeout time.Duration) (docker.RecreateResult, error)
	Restore(ctx context.Context, res docker.RecreateResult) error
	Inspect(ctx context.Context, id string) (docker.Container, error)
	ListContainers(ctx context.Context) ([]docker.Container, error)
	ComposeUp(ctx context.Context, bin string, spec docker.ComposeSpec) error
}

// Executor pulls and recreates one candidate at a time.
type Executor struct {
	Eng           Runtime
	Resolver      Resolver
	Memory        Memory
	Log           *slog.Logger
	HealthTimeout time.Duration
	StopTimeout   time.Duration
	ComposeBin    string
	StateDir      string
	Wait          func(ctx context.Context, id string) error
}

// ApplyRuntime recreates each changed container and rolls one generation back
// when the new container does not stay healthy.
func (e *Executor) ApplyRuntime(ctx context.Context, plan Plan) {
	for _, step := range plan.Steps {
		if step.ContainerID == "" {
			e.log().Warn("skip container with no id", "name", step.Name)
			continue
		}
		if err := e.runtimeStep(ctx, step); err != nil {
			e.log().Error("update failed", "name", step.Name, "image", step.Image, "err", err)
		}
	}
}

func (e *Executor) runtimeStep(ctx context.Context, step Step) error {
	if err := e.Memory.Begin(step.Key, step.Image, step.FromDigest, step.ToDigest); err != nil {
		return err
	}
	if err := e.pull(ctx, step); err != nil {
		_ = e.Memory.Abort(step.Key)
		return err
	}
	res, err := e.Eng.Recreate(ctx, step.ContainerID, labelsFor(step), e.StopTimeout)
	if err != nil {
		_ = e.Memory.Abort(step.Key)
		return err
	}
	if err := e.wait(ctx, res.NewID); err != nil {
		if rerr := e.Eng.Restore(ctx, res); rerr != nil {
			e.log().Error("rollback failed", "name", step.Name, "previous", res.PreviousID, "err", rerr)
		}
		_ = e.Memory.Reject(step.Key, step.ToDigest)
		return err
	}
	return e.Memory.Commit(step.Key, step.ToDigest)
}

// ApplyCompose pulls changed service images, then lets Compose recreate the project.
func (e *Executor) ApplyCompose(ctx context.Context, plan Plan, file, project, workdir string) error {
	if len(plan.Steps) == 0 {
		return nil
	}
	for _, step := range plan.Steps {
		if err := e.Memory.Begin(step.Key, step.Image, step.FromDigest, step.ToDigest); err != nil {
			return err
		}
		if err := e.pull(ctx, step); err != nil {
			_ = e.Memory.Abort(step.Key)
			return err
		}
	}
	spec := docker.ComposeSpec{Files: []string{file}, Project: project, WorkDir: workdir, Wait: e.HealthTimeout}
	if err := e.Eng.ComposeUp(ctx, e.ComposeBin, spec); err != nil {
		e.rollbackCompose(ctx, spec, plan)
		return err
	}
	var failed error
	for _, step := range plan.Steps {
		id := e.findService(ctx, project, step.Service)
		if id == "" {
			failed = errMissing(step.Service)
			break
		}
		if err := e.wait(ctx, id); err != nil {
			failed = err
			break
		}
	}
	if failed != nil {
		e.rollbackCompose(ctx, spec, plan)
		return failed
	}
	for _, step := range plan.Steps {
		if err := e.Memory.Commit(step.Key, step.ToDigest); err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) rollbackCompose(ctx context.Context, spec docker.ComposeSpec, plan Plan) {
	if err := RollbackCompose(ctx, e.Eng, e.ComposeBin, e.StateDir, spec, plan.Steps); err != nil {
		e.log().Error("compose rollback failed", "err", err)
	}
	for _, step := range plan.Steps {
		_ = e.Memory.Reject(step.Key, step.ToDigest)
	}
}

func (e *Executor) pull(ctx context.Context, step Step) error {
	auth, err := e.Resolver.PullAuth(ctx, step.Image)
	if err != nil {
		return err
	}
	return e.Eng.PullAndTag(ctx, step.Image, step.ToDigest, auth)
}

func (e *Executor) wait(ctx context.Context, id string) error {
	if e.Wait != nil {
		return e.Wait(ctx, id)
	}
	return WaitHealthy(ctx, func(ctx context.Context) (docker.Container, error) {
		return e.Eng.Inspect(ctx, id)
	}, e.HealthTimeout)
}

func (e *Executor) findService(ctx context.Context, project, service string) string {
	list, err := e.Eng.ListContainers(ctx)
	if err != nil {
		return ""
	}
	for _, ctr := range list {
		if ctr.Labels["com.docker.compose.project"] == project && ctr.Labels["com.docker.compose.service"] == service {
			return ctr.ID
		}
	}
	return ""
}

func (e *Executor) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func labelsFor(step Step) map[string]string {
	return map[string]string{
		"deployment-agent.previous-digest": step.FromDigest,
		"deployment-agent.service":         step.Name,
	}
}

type missingService string

func (m missingService) Error() string { return "compose service not running: " + string(m) }

func errMissing(service string) error { return missingService(service) }
