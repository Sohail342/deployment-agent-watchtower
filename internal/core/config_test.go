package core

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptWatchtower(t *testing.T) {
	model := HostModel{
		Containers: []ContainerView{
			{Name: "web", Image: "nginx:alpine", State: "running", Running: true, CurrentDigest: "sha256:aaa"},
			{Name: "job", Image: "app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", State: "running", Running: true, CurrentDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
			{Name: "old", Image: "redis:7", State: "exited", CurrentDigest: "sha256:ccc"},
		},
		Registries: []string{"docker.io"},
	}
	var in bytes.Buffer
	in.WriteString("2\nn\n\n")
	var out bytes.Buffer
	session, err := Prompt(context.Background(), &in, &out, model, "deployment-agent", "")
	if err != nil {
		t.Fatal(err)
	}
	if session.Mode != ModeWatchtower || session.IncludeSelf || !session.Confirmed {
		t.Fatalf("%+v", session)
	}
	text := out.String()
	if !strings.Contains(text, "3 containers (2 running)") {
		t.Fatalf("inventory missing: %s", text)
	}
	if !strings.Contains(text, "web") || !strings.Contains(text, "Pinned") || !strings.Contains(text, "Stopped") {
		t.Fatalf("candidate listing missing: %s", text)
	}
}

func TestPromptComposeFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte("services:\n  web:\n    image: nginx:alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var in bytes.Buffer
	in.WriteString("1\n" + file + "\ny\n")
	var out bytes.Buffer
	session, err := Prompt(context.Background(), &in, &out, HostModel{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if session.Mode != ModeCompose || session.ComposeFile != file || !session.Confirmed {
		t.Fatalf("%+v", session)
	}
	if !strings.Contains(out.String(), "nginx:alpine") {
		t.Fatalf("services not printed: %s", out.String())
	}
}

func TestSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Session{Mode: ModeWatchtower, IncludeSelf: true, Confirmed: true}
	if err := SaveSession(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%+v", got)
	}
}
