package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"deployment-agent/internal/docker"
	"deployment-agent/internal/registry"
)

const (
	labelProject = "com.docker.compose.project"
	labelService = "com.docker.compose.service"
	labelFiles   = "com.docker.compose.project.config_files"
	labelWorkDir = "com.docker.compose.project.working_dir"
)

// Source is the part of the engine the inventory reads.
type Source interface {
	ListContainers(ctx context.Context) ([]docker.Container, error)
	ListImages(ctx context.Context) ([]docker.Image, error)
	Events(ctx context.Context) (<-chan docker.Event, <-chan error)
}

// Inventory is the live host model plus rollback memory.
type Inventory struct {
	src Source
	dir string
	log *slog.Logger

	mu     sync.RWMutex
	model  HostModel
	memory map[string]ServiceMemory
}

// NewInventory loads rollback memory from dir.
func NewInventory(src Source, dir string, log *slog.Logger) (*Inventory, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	inv := &Inventory{src: src, dir: dir, log: log, memory: map[string]ServiceMemory{}}
	if err := inv.loadMemory(); err != nil {
		return nil, err
	}
	return inv, nil
}

// Sync replaces the host model from a full daemon read.
func (inv *Inventory) Sync(ctx context.Context) error {
	containers, err := inv.src.ListContainers(ctx)
	if err != nil {
		return err
	}
	images, err := inv.src.ListImages(ctx)
	if err != nil {
		return err
	}
	model := buildModel(containers, images)
	inv.mu.Lock()
	inv.model = model
	inv.mu.Unlock()
	return nil
}

// Model returns the latest host model.
func (inv *Inventory) Model() HostModel {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	return inv.model
}

// Watch applies Docker events by resyncing. A full resync also heals missed events.
func (inv *Inventory) Watch(ctx context.Context) {
	events, errs := inv.src.Events(ctx)
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-errs:
			if ok && err != nil && ctx.Err() == nil {
				inv.log.Warn("docker events", "err", err)
			}
			return
		case _, ok := <-events:
			if !ok {
				return
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(300*time.Millisecond, func() {
				if ctx.Err() != nil {
					return
				}
				sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				if err := inv.Sync(sctx); err != nil && ctx.Err() == nil {
					inv.log.Warn("resync", "err", err)
				}
			})
		}
	}
}

func (inv *Inventory) Get(key string) (ServiceMemory, bool) {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	mem, ok := inv.memory[key]
	return mem, ok
}

func (inv *Inventory) Rejected() map[string]string {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	out := map[string]string{}
	for key, mem := range inv.memory {
		if mem.RejectedDigest != "" {
			out[key] = mem.RejectedDigest
		}
	}
	return out
}

func (inv *Inventory) Begin(key, image, from, to string) error {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	mem := inv.memory[key]
	if mem.LastGoodDigest == "" && from != "" {
		mem.LastGoodDigest = from
	}
	mem.Image = image
	mem.DesiredDigest = to
	mem.Status = "updating"
	mem.UpdatedAt = time.Now().UTC()
	inv.memory[key] = mem
	return inv.saveMemoryLocked()
}

func (inv *Inventory) Commit(key, digest string) error {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	mem := inv.memory[key]
	mem.LastGoodDigest = digest
	mem.DesiredDigest = digest
	mem.RejectedDigest = ""
	mem.Status = "idle"
	mem.UpdatedAt = time.Now().UTC()
	inv.memory[key] = mem
	return inv.saveMemoryLocked()
}

func (inv *Inventory) Reject(key, digest string) error {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	mem := inv.memory[key]
	mem.RejectedDigest = digest
	mem.DesiredDigest = ""
	mem.Status = "idle"
	mem.UpdatedAt = time.Now().UTC()
	inv.memory[key] = mem
	return inv.saveMemoryLocked()
}

func (inv *Inventory) Abort(key string) error {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	mem := inv.memory[key]
	mem.DesiredDigest = ""
	mem.Status = "idle"
	mem.UpdatedAt = time.Now().UTC()
	inv.memory[key] = mem
	return inv.saveMemoryLocked()
}

func (inv *Inventory) Updating() map[string]ServiceMemory {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	out := map[string]ServiceMemory{}
	for key, mem := range inv.memory {
		if mem.Status == "updating" {
			out[key] = mem
		}
	}
	return out
}

type memoryFile struct {
	Version  int                      `json:"version"`
	Services map[string]ServiceMemory `json:"services"`
}

func (inv *Inventory) loadMemory() error {
	path := filepath.Join(inv.dir, "memory.json")
	buf, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var file memoryFile
	if err := json.Unmarshal(buf, &file); err != nil {
		return err
	}
	if file.Services != nil {
		inv.memory = file.Services
	}
	return nil
}

func (inv *Inventory) saveMemoryLocked() error {
	return writeJSON(filepath.Join(inv.dir, "memory.json"), memoryFile{Version: 1, Services: inv.memory})
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func buildModel(containers []docker.Container, images []docker.Image) HostModel {
	byID := map[string]docker.Image{}
	for _, img := range images {
		byID[img.ID] = img
	}
	model := HostModel{}
	regs := map[string]struct{}{}
	projects := map[string]*ComposeProject{}
	used := map[string][]string{}
	for _, ctr := range containers {
		view := ContainerView{
			ID:            ctr.ID,
			Name:          ctr.Name,
			Image:         ctr.Image,
			ImageID:       ctr.ImageID,
			State:         ctr.State,
			Running:       ctr.Running,
			Health:        ctr.Health,
			NetworkMode:   ctr.NetworkMode,
			Labels:        ctr.Labels,
			Mounts:        ctr.Mounts,
			Networks:      ctr.Networks,
			CurrentDigest: registry.MatchDigest(ctr.Image, byID[ctr.ImageID].RepoDigests),
		}
		if view.Labels != nil {
			view.ComposeProject = view.Labels[labelProject]
			view.ComposeService = view.Labels[labelService]
			view.ConfigFiles = view.Labels[labelFiles]
			view.WorkDir = view.Labels[labelWorkDir]
		}
		if ref, err := registry.Parse(ctr.Image); err == nil {
			regs[ref.Host] = struct{}{}
		}
		if view.ComposeProject != "" {
			p := projects[view.ComposeProject]
			if p == nil {
				p = &ComposeProject{Name: view.ComposeProject, WorkDir: view.WorkDir}
				projects[view.ComposeProject] = p
			}
			if view.ConfigFiles != "" && len(p.ConfigFiles) == 0 {
				p.ConfigFiles = splitFiles(view.ConfigFiles)
			}
			if view.WorkDir != "" {
				p.WorkDir = view.WorkDir
			}
			if view.ComposeService != "" {
				p.Services = append(p.Services, view.ComposeService)
			}
			p.Containers = append(p.Containers, view.Name)
		}
		if ctr.ImageID != "" {
			used[ctr.ImageID] = append(used[ctr.ImageID], ctr.Name)
		}
		model.Containers = append(model.Containers, view)
	}
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if ref, err := registry.Parse(tag); err == nil {
				regs[ref.Host] = struct{}{}
			}
		}
		model.Images = append(model.Images, ImageView{
			ID:          img.ID,
			RepoTags:    img.RepoTags,
			RepoDigests: img.RepoDigests,
			UsedBy:      used[img.ID],
		})
	}
	for host := range regs {
		model.Registries = append(model.Registries, host)
	}
	sort.Strings(model.Registries)
	names := make([]string, 0, len(projects))
	for name := range projects {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := projects[name]
		sort.Strings(p.Services)
		sort.Strings(p.Containers)
		model.Projects = append(model.Projects, *p)
	}
	sort.Slice(model.Containers, func(i, j int) bool { return model.Containers[i].Name < model.Containers[j].Name })
	return model
}

func splitFiles(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
