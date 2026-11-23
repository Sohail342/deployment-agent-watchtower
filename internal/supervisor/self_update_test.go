package supervisor

import (
	"context"
	"errors"
	"testing"

	"deployment-agent/internal/docker"
)

type fakeEng struct {
	docker.Engine
	handoff  bool
	restored bool
	removed  bool
	pulled   string
}

func (f *fakeEng) PullAndTag(_ context.Context, _, digest, _ string) error {
	f.pulled = digest
	return nil
}
func (f *fakeEng) Handoff(context.Context, string, map[string]string) (docker.RecreateResult, error) {
	f.handoff = true
	return docker.RecreateResult{NewID: "new", PreviousID: "old", OriginalName: "deployment-agent"}, nil
}
func (f *fakeEng) Restore(context.Context, docker.RecreateResult) error {
	f.restored = true
	return nil
}
func (f *fakeEng) Remove(context.Context, string) error {
	f.removed = true
	return nil
}
func (f *fakeEng) Inspect(context.Context, string) (docker.Container, error) {
	return docker.Container{Running: true}, nil
}

type fakeResolve struct{ digest string }

func (f fakeResolve) Digest(context.Context, string) (string, error) { return f.digest, nil }
func (f fakeResolve) PullAuth(context.Context, string) (string, error) {
	return "", nil
}

func TestHandoffExitsWhenInsideContainer(t *testing.T) {
	eng := &fakeEng{}
	exited := -1
	h := Handoff{
		Eng: eng, Resolver: fakeResolve{digest: "sha256:new"},
		SelfName: "deployment-agent", Hostname: "abcdef123456",
		Exit: func(code int) { exited = code },
		Wait: func(context.Context, string) error { return nil },
	}
	model := []Container{{
		ID: "abcdef123456ffff", Name: "deployment-agent", Image: "ghcr.io/acme/agent:latest",
		CurrentDigest: "sha256:old",
	}}
	if err := h.Run(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	if !eng.handoff || exited != 0 || eng.removed {
		t.Fatalf("handoff %v exit %d removed %v", eng.handoff, exited, eng.removed)
	}
}

func TestHandoffRestoresOnFailure(t *testing.T) {
	eng := &fakeEng{}
	h := Handoff{
		Eng: eng, Resolver: fakeResolve{digest: "sha256:new"},
		SelfName: "deployment-agent", Hostname: "host",
		Wait: func(context.Context, string) error { return errors.New("unhealthy") },
	}
	model := []Container{{
		ID: "other", Name: "deployment-agent", Image: "ghcr.io/acme/agent:latest",
		CurrentDigest: "sha256:old",
	}}
	if err := h.Run(context.Background(), model); err == nil {
		t.Fatal("expected error")
	}
	if !eng.restored {
		t.Fatal("expected restore")
	}
}

func TestNoopWhenDigestMatches(t *testing.T) {
	eng := &fakeEng{}
	h := Handoff{Eng: eng, Resolver: fakeResolve{digest: "sha256:old"}, SelfName: "deployment-agent"}
	model := []Container{{
		ID: "1", Name: "deployment-agent", Image: "ghcr.io/acme/agent:latest", CurrentDigest: "sha256:old",
	}}
	if err := h.Run(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	if eng.handoff {
		t.Fatal("did not expect a handoff")
	}
}
