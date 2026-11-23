package docker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ComposeUp runs the Compose v2 binary. The subprocess does not inherit the
// agent environment, so registry tokens are not interpolated into the project.
func (c *Client) ComposeUp(ctx context.Context, bin string, spec ComposeSpec) error {
	if strings.TrimSpace(bin) == "" {
		bin = "docker-compose"
	}
	if len(spec.Files) == 0 {
		return fmt.Errorf("compose file is required")
	}
	args := make([]string, 0, 16)
	for _, file := range spec.Files {
		args = append(args, "-f", file)
	}
	if spec.Project != "" {
		args = append(args, "--project-name", spec.Project)
	}
	if spec.WorkDir != "" {
		args = append(args, "--project-directory", spec.WorkDir)
	}
	wait := spec.Wait
	if wait <= 0 {
		wait = 60 * time.Second
	}
	args = append(args,
		"up", "-d",
		"--wait",
		"--wait-timeout", strconv.Itoa(seconds(wait)),
		"--pull", "never",
	)
	cmd := exec.CommandContext(ctx, bin, args...)
	if spec.WorkDir != "" {
		cmd.Dir = spec.WorkDir
	}
	cmd.Env = composeEnv(spec.Project)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if len(detail) > 4000 {
			detail = detail[len(detail)-4000:]
		}
		return fmt.Errorf("compose up: %w: %s", err, detail)
	}
	return nil
}

func composeEnv(project string) []string {
	keep := []string{"PATH", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "SystemRoot", "WINDIR", "SSL_CERT_FILE", "SSL_CERT_DIR"}
	env := make([]string, 0, len(keep)+1)
	for _, key := range keep {
		if val, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+val)
		}
	}
	if project != "" {
		env = append(env, "COMPOSE_PROJECT_NAME="+project)
	}
	return env
}
