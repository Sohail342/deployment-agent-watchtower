package discovery

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

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
