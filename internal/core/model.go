package core

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ContainerView is one container the agent has read from the daemon.
type ContainerView struct {
	ID             string
	Name           string
	Image          string
	ImageID        string
	State          string
	Running        bool
	Health         string
	NetworkMode    string
	Labels         map[string]string
	Mounts         []string
	Networks       []string
	CurrentDigest  string
	ComposeProject string
	ComposeService string
	ConfigFiles    string
	WorkDir        string
}

// ImageView is one image on the daemon.
type ImageView struct {
	ID          string
	RepoTags    []string
	RepoDigests []string
	UsedBy      []string
}

// ComposeProject is inferred from labels Docker already stores.
type ComposeProject struct {
	Name        string
	ConfigFiles []string
	WorkDir     string
	Services    []string
	Containers  []string
}

// HostModel is everything the agent currently knows about the machine.
type HostModel struct {
	Containers []ContainerView
	Images     []ImageView
	Projects   []ComposeProject
	Registries []string
}

// RunningCount is the number of containers in the running state.
func (m HostModel) RunningCount() int {
	n := 0
	for _, c := range m.Containers {
		if c.Running {
			n++
		}
	}
	return n
}

// ServiceMemory is rollback state for one container or compose service.
type ServiceMemory struct {
	Image          string    `json:"image"`
	LastGoodDigest string    `json:"last_good_digest"`
	DesiredDigest  string    `json:"desired_digest"`
	RejectedDigest string    `json:"rejected_digest,omitempty"`
	Status         string    `json:"status"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Mode is the pull strategy chosen at the prompt.
type Mode string

const (
	ModeCompose    Mode = "compose"
	ModeWatchtower Mode = "watchtower"
)

// Session is the operator's answer. It is not a catalog of workloads.
type Session struct {
	Mode           Mode   `json:"mode"`
	ComposeFile    string `json:"compose_file,omitempty"`
	ComposeProject string `json:"compose_project,omitempty"`
	IncludeSelf    bool   `json:"include_self"`
	Confirmed      bool   `json:"confirmed"`
}

// ErrDeclined means the operator chose not to start pulling.
var ErrDeclined = fmt.Errorf("operator declined the first pull")

// DefaultStateDir is the rollback-memory directory.
func DefaultStateDir() string {
	if runtime.GOOS == "windows" {
		base, err := os.UserCacheDir()
		if err != nil {
			return `C:\ProgramData\deployment-agent`
		}
		return filepath.Join(base, "deployment-agent")
	}
	return "/var/lib/deployment-agent"
}

// FormatInventory renders the host model for the operator.
func FormatInventory(m HostModel) string {
	regs := "(none)"
	if len(m.Registries) > 0 {
		regs = strings.Join(m.Registries, ", ")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Host: %d containers (%d running), %d images, %d compose projects\n",
		len(m.Containers), m.RunningCount(), len(m.Images), len(m.Projects))
	fmt.Fprintf(&b, "Registries seen: %s\n", regs)
	if len(m.Containers) > 0 {
		b.WriteString("\nContainers:\n")
		views := append([]ContainerView(nil), m.Containers...)
		sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
		for _, c := range views {
			state := c.State
			if state == "" {
				state = "unknown"
			}
			digest := c.CurrentDigest
			if digest == "" {
				digest = "no registry digest"
			}
			fmt.Fprintf(&b, "  - %s  %s  %s  %s\n", c.Name, state, c.Image, digest)
		}
	}
	if len(m.Projects) > 0 {
		b.WriteString("\nCompose projects:\n")
		for i, p := range m.Projects {
			file := "compose file not on disk"
			if len(p.ConfigFiles) > 0 {
				file = strings.Join(p.ConfigFiles, ", ")
			}
			fmt.Fprintf(&b, "  %d) %s — %s\n", i+1, p.Name, file)
		}
	}
	return b.String()
}
