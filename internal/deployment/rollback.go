package deployment

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"deployment-agent/internal/docker"
	"deployment-agent/internal/registry"
)

// RollbackCompose pins services back to the last good digest and reconciles
// the project with the Compose CLI. The previous image must still be local.
func RollbackCompose(ctx context.Context, eng interface {
	ComposeUp(context.Context, string, docker.ComposeSpec) error
}, bin, dir string, spec docker.ComposeSpec, steps []Step) error {
	var b strings.Builder
	b.WriteString("services:\n")
	n := 0
	for _, step := range steps {
		if step.FromDigest == "" || step.Service == "" {
			continue
		}
		if !safeName(step.Service) {
			return fmt.Errorf("service name %q cannot be written into a rollback file", step.Service)
		}
		ref, err := registry.Parse(step.Image)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "  %s:\n    image: %q\n", step.Service, ref.WithDigest(step.FromDigest))
		n++
	}
	if n == 0 {
		return fmt.Errorf("no previous digest is available for rollback")
	}
	path := filepath.Join(dir, "rollback-override.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return err
	}
	defer os.Remove(path)
	spec.Files = append(append([]string{}, spec.Files...), path)
	return eng.ComposeUp(ctx, bin, spec)
}

func safeName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}
