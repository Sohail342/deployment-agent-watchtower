//go:build integration

package docker_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"deployment-agent/internal/core"
	"deployment-agent/internal/deployment"
	"deployment-agent/internal/discovery"
	"deployment-agent/internal/docker"
	"deployment-agent/pkg/logger"
)

func TestDaemonRecreateAndRollback(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	eng, err := docker.Dial("", logger.New("error", os.Stderr))
	if err != nil {
		t.Skip(err)
	}
	if err := eng.Ping(ctx); err != nil {
		t.Skip(err)
	}
	for _, image := range []string{"alpine:3.20", "busybox:1.36"} {
		if err := exec.CommandContext(ctx, "docker", "image", "inspect", image).Run(); err != nil {
			if err := exec.CommandContext(ctx, "docker", "pull", image).Run(); err != nil {
				t.Skipf("%s is not available", image)
			}
		}
	}

	const (
		name = "deployment-agent-it"
		tag  = "deployment-agent-it:app"
	)
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		_ = exec.Command("docker", "rmi", tag).Run()
	})
	if out, err := exec.CommandContext(ctx, "docker", "tag", "alpine:3.20", tag).CombinedOutput(); err != nil {
		t.Fatalf("tag alpine: %v %s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--name", name, tag, "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("run: %v %s", err, out)
	}
	inv, err := core.NewInventory(eng, t.TempDir(), logger.New("error", os.Stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	var id string
	for _, ctr := range inv.Model().Containers {
		if ctr.Name == name {
			id = ctr.ID
		}
	}
	if id == "" {
		t.Fatal("inventory did not include the test container")
	}
	if out, err := exec.CommandContext(ctx, "docker", "tag", "busybox:1.36", tag).CombinedOutput(); err != nil {
		t.Fatalf("retag: %v %s", err, out)
	}
	res, err := eng.Recreate(ctx, id, map[string]string{"deployment-agent.service": name}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := eng.Inspect(ctx, res.NewID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != name || !got.Running {
		t.Fatalf("replacement: %+v", got)
	}
	if err := eng.Restore(ctx, res); err != nil {
		t.Fatal(err)
	}
	restored, err := eng.Inspect(ctx, res.PreviousID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Name != name || !restored.Running {
		t.Fatalf("rollback: %+v", restored)
	}
}

func TestComposeFileIsTheOnlyProjectPlanned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte("name: demo\nservices:\n  web:\n    image: nginx:alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := discovery.LoadCompose(ctx, file, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := deployment.Build(set.Candidates, map[string]string{"demo/web": "sha256:new"}, nil)
	if len(plan.Steps) != 1 || plan.Steps[0].Service != "web" {
		t.Fatalf("%+v", plan.Steps)
	}
	other := discovery.Watchtower([]discovery.Container{{
		Name: "unrelated", Image: "redis:7", Running: true, CurrentDigest: "sha256:old",
	}}, "", "")
	if len(deployment.Build(other, map[string]string{"unrelated": "sha256:new"}, nil).Steps) != 1 {
		t.Fatal("watchtower planning should stay independent of the compose file")
	}
	if strings.Contains(set.File, "redis") {
		t.Fatal("compose set included an unrelated image")
	}
}
