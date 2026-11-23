package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"deployment-agent/internal/docker"
)

type fakeSource struct {
	mu         sync.Mutex
	containers []docker.Container
	images     []docker.Image
	events     chan docker.Event
	errs       chan error
}

func (f *fakeSource) ListContainers(context.Context) ([]docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]docker.Container(nil), f.containers...), nil
}

func (f *fakeSource) ListImages(context.Context) ([]docker.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]docker.Image(nil), f.images...), nil
}

func (f *fakeSource) Events(context.Context) (<-chan docker.Event, <-chan error) {
	return f.events, f.errs
}

func TestBuildModelReadsComposeLabelsAndDigests(t *testing.T) {
	model := buildModel([]docker.Container{
		{
			ID: "c1", Name: "demo-api-1", Image: "ghcr.io/acme/api:prod", ImageID: "sha256:img",
			State: "running", Running: true,
			Labels: map[string]string{
				labelProject: "demo",
				labelService: "api",
				labelFiles:   "/opt/apps/demo/compose.yaml",
				labelWorkDir: "/opt/apps/demo",
			},
		},
		{ID: "c2", Name: "web", Image: "nginx:alpine", ImageID: "sha256:ngx", State: "exited"},
	}, []docker.Image{
		{ID: "sha256:img", RepoTags: []string{"ghcr.io/acme/api:prod"}, RepoDigests: []string{"ghcr.io/acme/api@sha256:dead"}},
		{ID: "sha256:ngx", RepoTags: []string{"nginx:alpine"}, RepoDigests: []string{"docker.io/library/nginx@sha256:beef"}},
	})
	if len(model.Containers) != 2 || len(model.Projects) != 1 {
		t.Fatalf("%+v", model)
	}
	if model.Containers[0].CurrentDigest != "sha256:dead" || model.Containers[0].ComposeService != "api" {
		t.Fatalf("api container: %+v", model.Containers[0])
	}
	if model.Projects[0].ConfigFiles[0] != "/opt/apps/demo/compose.yaml" {
		t.Fatalf("project: %+v", model.Projects[0])
	}
	if len(model.Registries) != 2 || model.Registries[0] != "docker.io" || model.Registries[1] != "ghcr.io" {
		t.Fatalf("registries: %v", model.Registries)
	}
}

func TestEventResyncAddsContainer(t *testing.T) {
	src := &fakeSource{
		containers: []docker.Container{{ID: "1", Name: "web", Image: "nginx:alpine", State: "running", Running: true, ImageID: "img"}},
		images:     []docker.Image{{ID: "img", RepoTags: []string{"nginx:alpine"}, RepoDigests: []string{"docker.io/library/nginx@sha256:aaa"}}},
		events:     make(chan docker.Event, 1),
		errs:       make(chan error),
	}
	inv, err := NewInventory(src, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := inv.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	go inv.Watch(ctx)
	src.mu.Lock()
	src.containers = append(src.containers, docker.Container{ID: "2", Name: "new", Image: "redis:7", State: "running", Running: true})
	src.mu.Unlock()
	src.events <- docker.Event{Type: "container", Action: "start", ID: "2"}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(inv.Model().Containers) == 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("model did not observe the new container: %+v", inv.Model().Containers)
}

func TestMemoryCommitAndReject(t *testing.T) {
	inv, err := NewInventory(&fakeSource{}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.Begin("web", "nginx:alpine", "sha256:old", "sha256:new"); err != nil {
		t.Fatal(err)
	}
	if mem, ok := inv.Get("web"); !ok || mem.Status != "updating" || mem.LastGoodDigest != "sha256:old" {
		t.Fatalf("%+v %v", mem, ok)
	}
	if err := inv.Reject("web", "sha256:new"); err != nil {
		t.Fatal(err)
	}
	if inv.Rejected()["web"] != "sha256:new" {
		t.Fatalf("%v", inv.Rejected())
	}
}
