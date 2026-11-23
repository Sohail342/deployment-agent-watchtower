package docker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type fakeAPI struct {
	ins     container.InspectResponse
	order   []string
	created string
	removed []string
	fail    string
}

func (f *fakeAPI) inspect(context.Context, string) (container.InspectResponse, error) {
	f.order = append(f.order, "inspect")
	return f.ins, nil
}

func (f *fakeAPI) stop(context.Context, string, time.Duration) error {
	f.order = append(f.order, "stop")
	f.ins.State.Running = false
	f.ins.State.Status = container.StateExited
	return nil
}

func (f *fakeAPI) rename(_ context.Context, id, name string) error {
	f.order = append(f.order, "rename:"+name)
	if f.ins.ID == id {
		f.ins.Name = "/" + name
	}
	return nil
}

func (f *fakeAPI) remove(_ context.Context, id string) error {
	f.order = append(f.order, "remove:"+id)
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeAPI) start(_ context.Context, id string) error {
	f.order = append(f.order, "start:"+id)
	if id == f.ins.ID {
		f.ins.State.Running = true
		f.ins.State.Status = container.StateRunning
	}
	if f.fail == "start" && id == "new" {
		return errStart
	}
	return nil
}

func (f *fakeAPI) create(_ context.Context, opts client.ContainerCreateOptions) (string, error) {
	f.order = append(f.order, "create:"+opts.Name)
	if opts.Config == nil || opts.Config.Labels["deployment-agent.managed"] != "true" {
		return "", errStart
	}
	if opts.HostConfig == nil || opts.HostConfig.Binds[0] != "/data:/data" {
		return "", errStart
	}
	f.created = "new"
	return "new", nil
}

func (f *fakeAPI) connect(_ context.Context, networkName, containerID string, _ *network.EndpointSettings) error {
	f.order = append(f.order, "connect:"+networkName+":"+containerID)
	return nil
}

type startErr struct{}

func (startErr) Error() string { return "start failed" }

var errStart = startErr{}

func sampleInspect() container.InspectResponse {
	return container.InspectResponse{
		ID:   "old",
		Name: "/web",
		State: &container.State{
			Status:  container.StateRunning,
			Running: true,
		},
		Config: &container.Config{
			Image: "nginx:alpine",
			Cmd:   []string{"nginx"},
			Labels: map[string]string{
				"app": "web",
			},
		},
		HostConfig: &container.HostConfig{
			Binds:       []string{"/data:/data"},
			NetworkMode: "bridge",
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {},
				"extra":  {},
			},
		},
	}
}

func TestRecreatePreservesBindsAndRollsBackOnStartFailure(t *testing.T) {
	api := &fakeAPI{ins: sampleInspect(), fail: "start"}
	_, err := replace(context.Background(), api, "old", map[string]string{"deployment-agent.previous-digest": "sha256:old"}, true, time.Second)
	if err == nil {
		t.Fatal("expected start failure")
	}
	joined := strings.Join(api.order, " ")
	if !strings.Contains(joined, "stop") || !strings.Contains(joined, "rename:web"+PreviousSuffix) || !strings.Contains(joined, "create:web") {
		t.Fatalf("order: %s", joined)
	}
	if !strings.Contains(joined, "rename:web") || !strings.Contains(joined, "start:old") {
		t.Fatalf("expected rollback rename and start, order: %s", joined)
	}
	if api.ins.Name != "/web" {
		t.Fatalf("name after rollback: %s", api.ins.Name)
	}
}

func TestRecreateSuccessOrder(t *testing.T) {
	api := &fakeAPI{ins: sampleInspect()}
	res, err := replace(context.Background(), api, "old", nil, true, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.NewID != "new" || res.OriginalName != "web" {
		t.Fatalf("%+v", res)
	}
	joined := strings.Join(api.order, " ")
	if !strings.Contains(joined, "connect:extra:new") {
		t.Fatalf("missing extra network: %s", joined)
	}
}
