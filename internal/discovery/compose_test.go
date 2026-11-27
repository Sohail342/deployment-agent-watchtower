package discovery

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadComposeMatchesRunningService(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	body := []byte("name: demo\nservices:\n  api:\n    image: ghcr.io/acme/api:prod\n  web:\n    image: nginx:alpine\n")
	if err := os.WriteFile(file, body, 0o644); err != nil {
		t.Fatal(err)
	}
	containers := []Container{{
		ID: "1", Name: "demo-api-1", Image: "ghcr.io/acme/api:prod", Running: true,
		CurrentDigest: "sha256:old", ComposeProject: "demo", ComposeService: "api",
	}}
	set, err := LoadCompose(context.Background(), file, "", containers)
	if err != nil {
		t.Fatal(err)
	}
	if set.Project != "demo" || len(set.Candidates) != 2 {
		t.Fatalf("%+v", set)
	}
	if set.Candidates[0].Service != "api" || set.Candidates[0].CurrentDigest != "sha256:old" {
		t.Fatalf("api: %+v", set.Candidates[0])
	}
	if set.Candidates[1].Service != "web" || set.Candidates[1].ContainerID != "" {
		t.Fatalf("web: %+v", set.Candidates[1])
	}
}

func TestNormalizeComposePathStripsWindowsDriveSlash(t *testing.T) {
	got := NormalizeComposePath(`/D:\projects\deployment_agent\deployments\docker-compose.yaml`)
	if runtime.GOOS == "windows" {
		want := `D:\projects\deployment_agent\deployments\docker-compose.yaml`
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
		return
	}
	if got == "" || got[0] == '/' {
		t.Fatalf("leading slash was kept: %q", got)
	}
}
