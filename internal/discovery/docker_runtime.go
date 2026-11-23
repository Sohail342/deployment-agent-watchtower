package discovery

import "deployment-agent/internal/registry"

// Container is one container from the live host model.
type Container struct {
	ID             string
	Name           string
	Image          string
	State          string
	Running        bool
	NetworkMode    string
	Labels         map[string]string
	CurrentDigest  string
	ComposeProject string
	ComposeService string
}

// Candidate is one image the chosen mode may pull.
type Candidate struct {
	Key           string
	ContainerID   string
	Name          string
	Service       string
	Image         string
	Host          string
	CurrentDigest string
	Pinned        bool
	LocalOnly     bool
	Stopped       bool
	IsAgent       bool
}

// Watchtower builds the candidate set from every container on the machine.
// The agent container is reported and is not a normal candidate. Callers pass
// includeSelf only so the prompt can show it; the update itself is a handoff.
func Watchtower(containers []Container, selfName, hostname string) []Candidate {
	selfID := selfContainerID(containers, selfName, hostname)
	out := make([]Candidate, 0, len(containers))
	for _, ctr := range containers {
		if ctr.Name == "" || hasPreviousSuffix(ctr.Name) {
			continue
		}
		if isContainerNetwork(ctr.NetworkMode) {
			continue
		}
		ref, err := registry.Parse(ctr.Image)
		cand := Candidate{
			Key:           ctr.Name,
			ContainerID:   ctr.ID,
			Name:          ctr.Name,
			Image:         ctr.Image,
			CurrentDigest: ctr.CurrentDigest,
			Stopped:       !ctr.Running,
			IsAgent:       ctr.ID != "" && ctr.ID == selfID,
		}
		if err != nil {
			cand.LocalOnly = true
			out = append(out, cand)
			continue
		}
		cand.Host = ref.Host
		cand.Image = ctr.Image
		cand.Pinned = ref.Pinned
		if !ref.Pinned && ctr.CurrentDigest == "" {
			cand.LocalOnly = true
		}
		out = append(out, cand)
	}
	return out
}

func selfContainerID(containers []Container, selfName, hostname string) string {
	for _, ctr := range containers {
		if hostname != "" && len(hostname) >= 12 && len(ctr.ID) >= len(hostname) && ctr.ID[:len(hostname)] == hostname {
			return ctr.ID
		}
	}
	for _, ctr := range containers {
		if selfName != "" && ctr.Name == selfName {
			return ctr.ID
		}
	}
	return ""
}

func isContainerNetwork(mode string) bool {
	return len(mode) >= len("container:") && mode[:len("container:")] == "container:"
}

func hasPreviousSuffix(name string) bool {
	const suffix = ".deployment-agent-previous"
	return len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix
}
