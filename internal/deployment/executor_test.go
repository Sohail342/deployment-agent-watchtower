package deployment

import (
	"context"
	"errors"
	"testing"
	"time"

	"deployment-agent/internal/docker"
)

type memStub struct {
	status   string
	rejected string
	good     string
}

func (m *memStub) Begin(_, _, from, _ string) error {
	m.status = "updating"
	m.good = from
	return nil
}
func (m *memStub) Commit(_, digest string) error {
	m.status = "idle"
	m.good = digest
	m.rejected = ""
	return nil
}
func (m *memStub) Reject(_, digest string) error {
	m.status = "idle"
	m.rejected = digest
	return nil
}
func (m *memStub) Abort(string) error {
	m.status = "idle"
	return nil
}

type engStub struct {
	recreated bool
	restored  bool
	pulled    string
}

func (e *engStub) PullAndTag(_ context.Context, _, digest, _ string) error {
	e.pulled = digest
	return nil
}
func (e *engStub) Recreate(context.Context, string, map[string]string, time.Duration) (docker.RecreateResult, error) {
	e.recreated = true
	return docker.RecreateResult{NewID: "new", PreviousID: "old", OriginalName: "web"}, nil
}
func (e *engStub) Restore(context.Context, docker.RecreateResult) error {
	e.restored = true
	return nil
}
func (e *engStub) Inspect(context.Context, string) (docker.Container, error) {
	return docker.Container{}, nil
}
func (e *engStub) ListContainers(context.Context) ([]docker.Container, error)  { return nil, nil }
func (e *engStub) ComposeUp(context.Context, string, docker.ComposeSpec) error { return nil }

type authStub struct{}

func (authStub) PullAuth(context.Context, string) (string, error) { return "hdr", nil }

func TestRuntimeRollsBackWhenUnhealthy(t *testing.T) {
	eng := &engStub{}
	mem := &memStub{}
	ex := &Executor{
		Eng: eng, Resolver: authStub{}, Memory: mem,
		Wait: func(context.Context, string) error { return errors.New("unhealthy") },
	}
	ex.ApplyRuntime(context.Background(), Plan{Steps: []Step{{
		Key: "web", ContainerID: "old", Name: "web", Image: "nginx:alpine",
		FromDigest: "sha256:old", ToDigest: "sha256:new",
	}}})
	if !eng.recreated || !eng.restored {
		t.Fatalf("recreated %v restored %v", eng.recreated, eng.restored)
	}
	if mem.rejected != "sha256:new" || eng.pulled != "sha256:new" {
		t.Fatalf("memory %+v pulled %s", mem, eng.pulled)
	}
}
