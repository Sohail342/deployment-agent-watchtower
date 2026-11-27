package discovery

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"

	"github.com/compose-spec/compose-go/v2/cli"

	"deployment-agent/internal/registry"
)

// ComposeSet is the project the operator pointed at, matched to containers
// already running on the machine.
type ComposeSet struct {
	File       string
	Project    string
	WorkDir    string
	Candidates []Candidate
}

// LoadCompose reads a compose file and pairs each service image with a
// container from the live model when one exists.
func LoadCompose(ctx context.Context, file, project string, containers []Container) (ComposeSet, error) {
	file = NormalizeComposePath(file)
	optsFns := []cli.ProjectOptionsFn{
		cli.WithWorkingDirectory(filepath.Dir(file)),
		cli.WithEnvFiles(),
		cli.WithDotEnv,
	}
	if project != "" {
		optsFns = append(optsFns, cli.WithName(project))
	}
	options, err := cli.NewProjectOptions([]string{file}, optsFns...)
	if err != nil {
		return ComposeSet{}, err
	}
	loaded, err := options.LoadProject(ctx)
	if err != nil {
		return ComposeSet{}, fmt.Errorf("load compose file: %w", err)
	}
	byService := map[string]Container{}
	for _, ctr := range containers {
		if ctr.ComposeProject == loaded.Name && ctr.ComposeService != "" {
			byService[ctr.ComposeService] = ctr
		}
	}
	names := make([]string, 0, len(loaded.Services))
	for name := range loaded.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	set := ComposeSet{
		File:    file,
		Project: loaded.Name,
		WorkDir: loaded.WorkingDir,
	}
	for _, name := range names {
		svc := loaded.Services[name]
		if svc.Image == "" {
			continue
		}
		ref, err := registry.Parse(svc.Image)
		cand := Candidate{
			Key:     loaded.Name + "/" + name,
			Name:    name,
			Service: name,
			Image:   svc.Image,
		}
		if ctr, ok := byService[name]; ok {
			cand.ContainerID = ctr.ID
			cand.Name = ctr.Name
			cand.CurrentDigest = ctr.CurrentDigest
			cand.Stopped = !ctr.Running
		}
		if err != nil {
			cand.LocalOnly = true
		} else {
			cand.Host = ref.Host
			cand.Pinned = ref.Pinned
		}
		set.Candidates = append(set.Candidates, cand)
	}
	return set, nil
}

// NormalizeComposePath turns a Compose label path into a path this process can open.
// Docker Desktop on Windows records drive paths with a leading slash
func NormalizeComposePath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, `"'`)
	original := path
	if len(path) >= 3 && (path[0] == '/' || path[0] == '\\') && path[2] == ':' && isDriveLetter(path[1]) {
		path = path[1:]
	}
	if runtime.GOOS == "windows" {
		lower := strings.ToLower(path)
		if strings.HasPrefix(lower, "/mnt/") && len(path) > 7 && path[6] == '/' && isDriveLetter(path[5]) {
			path = string(unicode.ToUpper(rune(path[5]))) + `:\` + filepath.FromSlash(path[7:])
		} else if len(path) >= 4 && path[0] == '/' && path[2] == '/' && isDriveLetter(path[1]) && !strings.Contains(path, ":") {
			path = string(unicode.ToUpper(rune(path[1]))) + `:\` + filepath.FromSlash(path[3:])
		}
	}
	if path == original {
		return path
	}
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func isDriveLetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}
