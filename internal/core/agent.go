package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"deployment-agent/internal/deployment"
	"deployment-agent/internal/discovery"
	"deployment-agent/internal/docker"
	"deployment-agent/internal/registry"
	"deployment-agent/internal/supervisor"
)

// Agent reconciles the machine from a live Docker model and one prompted mode.
type Agent struct {
	Inv           *Inventory
	Eng           docker.Engine
	Resolver      registry.Resolver
	Log           *slog.Logger
	Interval      time.Duration
	Jitter        time.Duration
	HealthTimeout time.Duration
	StopTimeout   time.Duration
	ComposeBin    string
	SelfName      string
	Hostname      string
	StateDir      string
	Reconfigure   bool
	Interactive   bool
	In            io.Reader
	Out           io.Writer
	Session       Session
}

// Run syncs the daemon, asks for a mode when needed, and reconciles until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.Eng.Ping(ctx); err != nil {
		return fmt.Errorf("docker ping: %w", err)
	}
	if err := a.Inv.Sync(ctx); err != nil {
		return err
	}
	if err := a.Eng.Recover(ctx); err != nil {
		a.log().Warn("startup recovery", "err", err)
	}
	if err := a.Inv.Sync(ctx); err != nil {
		return err
	}
	a.settleMemory()
	session, err := a.ensureSession(ctx)
	if err != nil {
		return err
	}
	a.Session = session
	go a.Inv.Watch(ctx)
	return Run(ctx, a.Interval, a.Jitter, a.log(), a.reconcile)
}

func (a *Agent) ensureSession(ctx context.Context) (Session, error) {
	if !a.Reconfigure {
		if session, err := LoadSession(a.StateDir); err == nil {
			a.log().Info("resumed session", "mode", session.Mode, "compose_file", session.ComposeFile)
			return session, nil
		}
	}
	if !a.Interactive {
		return Session{}, fmt.Errorf("no saved session and stdin is not a terminal")
	}
	session, err := Prompt(ctx, a.In, a.Out, a.Inv.Model(), a.SelfName, a.Hostname)
	if err != nil {
		return Session{}, err
	}
	if !session.Confirmed {
		return Session{}, ErrDeclined
	}
	if err := SaveSession(a.StateDir, session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (a *Agent) reconcile(ctx context.Context) error {
	if err := a.Inv.Sync(ctx); err != nil {
		return err
	}
	model := a.Inv.Model()
	ex := &deployment.Executor{
		Eng:           a.Eng,
		Resolver:      a.Resolver,
		Memory:        a.Inv,
		Log:           a.log(),
		HealthTimeout: a.HealthTimeout,
		StopTimeout:   a.StopTimeout,
		ComposeBin:    a.ComposeBin,
		StateDir:      a.StateDir,
	}
	switch a.Session.Mode {
	case ModeCompose:
		set, err := discovery.LoadCompose(ctx, a.Session.ComposeFile, a.Session.ComposeProject, containersOf(model))
		if err != nil {
			return err
		}
		plan := deployment.Build(set.Candidates, a.resolve(ctx, set.Candidates), a.Inv.Rejected())
		if len(plan.Steps) == 0 {
			a.log().Info("compose project is current", "project", set.Project)
			return nil
		}
		if err := ex.ApplyCompose(ctx, plan, set.File, set.Project, set.WorkDir); err != nil {
			a.log().Error("compose update failed", "project", set.Project, "err", err)
		}
	default:
		cands := discovery.Watchtower(containersOf(model), a.SelfName, a.Hostname)
		plan := deployment.Build(cands, a.resolve(ctx, cands), a.Inv.Rejected())
		if len(plan.Steps) == 0 {
			a.log().Info("running containers are current", "candidates", len(cands))
		} else {
			ex.ApplyRuntime(ctx, plan)
		}
		if a.Session.IncludeSelf {
			err := (supervisor.Handoff{
				Eng:           a.Eng,
				Resolver:      a.Resolver,
				Log:           a.log(),
				SelfName:      a.SelfName,
				Hostname:      a.Hostname,
				HealthTimeout: a.HealthTimeout,
			}).Run(ctx, selfContainers(a.Inv.Model()))
			if err != nil {
				a.log().Error("self-update failed", "err", err)
			}
		}
	}
	return nil
}

func (a *Agent) resolve(ctx context.Context, cands []discovery.Candidate) map[string]string {
	out := map[string]string{}
	for _, cand := range cands {
		if cand.Pinned || cand.LocalOnly || cand.Stopped || cand.IsAgent {
			continue
		}
		digest, err := a.Resolver.Digest(ctx, cand.Image)
		if err != nil {
			a.log().Warn("registry digest", "image", cand.Image, "err", err)
			continue
		}
		out[cand.Key] = digest
	}
	return out
}

func (a *Agent) settleMemory() {
	model := a.Inv.Model()
	for key, mem := range a.Inv.Updating() {
		view, ok := findKey(model, key)
		if !ok {
			_ = a.Inv.Abort(key)
			continue
		}
		if view.Running && view.Health != "unhealthy" && mem.DesiredDigest != "" && (view.CurrentDigest == "" || view.CurrentDigest == mem.DesiredDigest) {
			_ = a.Inv.Commit(key, mem.DesiredDigest)
			continue
		}
		if mem.DesiredDigest != "" {
			_ = a.Inv.Reject(key, mem.DesiredDigest)
			continue
		}
		_ = a.Inv.Abort(key)
	}
}

func findKey(model HostModel, key string) (ContainerView, bool) {
	for _, ctr := range model.Containers {
		if ctr.Name == key || ctr.ComposeProject+"/"+ctr.ComposeService == key {
			return ctr, true
		}
	}
	return ContainerView{}, false
}

func containersOf(model HostModel) []discovery.Container {
	out := make([]discovery.Container, 0, len(model.Containers))
	for _, ctr := range model.Containers {
		out = append(out, discovery.Container{
			ID:             ctr.ID,
			Name:           ctr.Name,
			Image:          ctr.Image,
			State:          ctr.State,
			Running:        ctr.Running,
			NetworkMode:    ctr.NetworkMode,
			Labels:         ctr.Labels,
			CurrentDigest:  ctr.CurrentDigest,
			ComposeProject: ctr.ComposeProject,
			ComposeService: ctr.ComposeService,
		})
	}
	return out
}

func selfContainers(model HostModel) []supervisor.Container {
	out := make([]supervisor.Container, 0, len(model.Containers))
	for _, ctr := range model.Containers {
		out = append(out, supervisor.Container{
			ID: ctr.ID, Name: ctr.Name, Image: ctr.Image, CurrentDigest: ctr.CurrentDigest,
		})
	}
	return out
}

func (a *Agent) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}
