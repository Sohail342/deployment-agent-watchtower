package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"deployment-agent/internal/discovery"
	"deployment-agent/internal/registry"
)

// Prompt asks how this process should pull. r and w are the operator console.
func Prompt(ctx context.Context, r io.Reader, w io.Writer, model HostModel, selfName, hostname string) (Session, error) {
	if _, err := io.WriteString(w, FormatInventory(model)); err != nil {
		return Session{}, err
	}
	if _, err := io.WriteString(w, `
How should this agent pull?
  1) Compose file — pull the images in a compose file and recreate that project
  2) Watchtower — watch running containers and pull each image from its own registry

`); err != nil {
		return Session{}, err
	}
	in := bufio.NewReader(r)
	choice, err := ask(in, w, "Choice [1/2]: ", func(s string) bool { return s == "1" || s == "2" })
	if err != nil {
		return Session{}, err
	}
	if choice == "1" {
		return promptCompose(ctx, in, w, model)
	}
	return promptWatchtower(in, w, model, selfName, hostname)
}

func promptCompose(ctx context.Context, in *bufio.Reader, w io.Writer, model HostModel) (Session, error) {
	if len(model.Projects) > 0 {
		if _, err := io.WriteString(w, "\nDiscovered compose projects:\n"); err != nil {
			return Session{}, err
		}
		for i, p := range model.Projects {
			file := "compose file not on disk"
			if len(p.ConfigFiles) > 0 {
				file = strings.Join(p.ConfigFiles, ", ")
			}
			if _, err := fmt.Fprintf(w, "  %d) %s — %s\n", i+1, p.Name, file); err != nil {
				return Session{}, err
			}
		}
	}
	answer, err := ask(in, w, "Compose file path or project number: ", func(s string) bool { return s != "" })
	if err != nil {
		return Session{}, err
	}
	file, project, err := resolveComposeChoice(answer, model.Projects)
	if err != nil {
		return Session{}, err
	}
	set, err := discovery.LoadCompose(ctx, file, project, toDiscovery(model))
	if err != nil {
		return Session{}, err
	}
	if _, err := io.WriteString(w, "\nServices:\n"); err != nil {
		return Session{}, err
	}
	if len(set.Candidates) == 0 {
		if _, err := io.WriteString(w, "  (no images in this file)\n"); err != nil {
			return Session{}, err
		}
	}
	for _, c := range set.Candidates {
		host := c.Host
		if host == "" {
			host = "unparsed"
		}
		if _, err := fmt.Fprintf(w, "  - %s %s (%s)\n", c.Service, c.Image, host); err != nil {
			return Session{}, err
		}
	}
	ok, err := askYes(in, w, "Pull and recreate these now, then keep watching? [Y/n]: ", true)
	if err != nil {
		return Session{}, err
	}
	return Session{Mode: ModeCompose, ComposeFile: file, ComposeProject: set.Project, Confirmed: ok}, nil
}

func promptWatchtower(in *bufio.Reader, w io.Writer, model HostModel, selfName, hostname string) (Session, error) {
	include, err := askYes(in, w, "Include this agent container? [y/N]: ", false)
	if err != nil {
		return Session{}, err
	}
	cands := discovery.Watchtower(toDiscovery(model), selfName, hostname)
	if err := writeCandidates(w, cands); err != nil {
		return Session{}, err
	}
	ok, err := askYes(in, w, "Pull and recreate these now, then keep watching? [Y/n]: ", true)
	if err != nil {
		return Session{}, err
	}
	return Session{Mode: ModeWatchtower, IncludeSelf: include, Confirmed: ok}, nil
}

func writeCandidates(w io.Writer, cands []discovery.Candidate) error {
	var active, pinned, stopped []discovery.Candidate
	for _, c := range cands {
		switch {
		case c.IsAgent:
			continue
		case c.Stopped:
			stopped = append(stopped, c)
		case c.Pinned:
			pinned = append(pinned, c)
		default:
			active = append(active, c)
		}
	}
	if _, err := io.WriteString(w, "Candidates:\n"); err != nil {
		return err
	}
	if len(active) == 0 {
		if _, err := io.WriteString(w, "  (none running with a registry tag)\n"); err != nil {
			return err
		}
	}
	for _, c := range active {
		host := c.Host
		if c.LocalOnly {
			host = "local"
		}
		if _, err := fmt.Fprintf(w, "  - %s (%s @ %s)\n", c.Name, c.Image, host); err != nil {
			return err
		}
	}
	if len(pinned) > 0 {
		if _, err := io.WriteString(w, "Pinned (not polled):\n"); err != nil {
			return err
		}
		for _, c := range pinned {
			if _, err := fmt.Fprintf(w, "  - %s (%s)\n", c.Name, c.Image); err != nil {
				return err
			}
		}
	}
	if len(stopped) > 0 {
		if _, err := io.WriteString(w, "Stopped (not started):\n"); err != nil {
			return err
		}
		for _, c := range stopped {
			if _, err := fmt.Fprintf(w, "  - %s (%s)\n", c.Name, c.Image); err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func resolveComposeChoice(answer string, projects []ComposeProject) (string, string, error) {
	if n, err := strconv.Atoi(answer); err == nil {
		if n < 1 || n > len(projects) {
			return "", "", fmt.Errorf("project number %d is out of range", n)
		}
		p := projects[n-1]
		if len(p.ConfigFiles) == 0 {
			return "", "", fmt.Errorf("project %s has no compose file on disk", p.Name)
		}
		return p.ConfigFiles[0], p.Name, nil
	}
	file := answer
	info, err := os.Stat(file)
	if err != nil {
		return "", "", fmt.Errorf("compose file: %w", err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("%s is a directory", file)
	}
	return file, "", nil
}

func ask(in *bufio.Reader, w io.Writer, label string, valid func(string) bool) (string, error) {
	for tries := 0; tries < 3; tries++ {
		if _, err := io.WriteString(w, label); err != nil {
			return "", err
		}
		line, err := in.ReadString('\n')
		if err != nil && len(line) == 0 {
			return "", err
		}
		line = strings.TrimSpace(line)
		if valid(line) {
			return line, nil
		}
		if _, err := io.WriteString(w, "Unrecognized answer.\n"); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("too many invalid answers")
}

func askYes(in *bufio.Reader, w io.Writer, label string, defaultYes bool) (bool, error) {
	line, err := ask(in, w, label, func(string) bool { return true })
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "":
		return defaultYes, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return defaultYes, nil
	}
}

func toDiscovery(model HostModel) []discovery.Container {
	out := make([]discovery.Container, 0, len(model.Containers))
	for _, c := range model.Containers {
		out = append(out, discovery.Container{
			ID:             c.ID,
			Name:           c.Name,
			Image:          c.Image,
			State:          c.State,
			Running:        c.Running,
			NetworkMode:    c.NetworkMode,
			Labels:         c.Labels,
			CurrentDigest:  c.CurrentDigest,
			ComposeProject: c.ComposeProject,
			ComposeService: c.ComposeService,
		})
	}
	return out
}

// LoadSession reads the last prompt answer.
func LoadSession(dir string) (Session, error) {
	buf, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return Session{}, err
	}
	var s Session
	if err := json.Unmarshal(buf, &s); err != nil {
		return Session{}, err
	}
	if !s.Confirmed || (s.Mode != ModeCompose && s.Mode != ModeWatchtower) {
		return Session{}, fmt.Errorf("session is incomplete")
	}
	return s, nil
}

// SaveSession writes the prompt answer so a later restart can continue.
func SaveSession(dir string, s Session) error {
	return writeJSON(filepath.Join(dir, "session.json"), s)
}

// RegistryPrompt asks for a username and token after a 401.
func RegistryPrompt(r io.Reader, w io.Writer) func(host string) (registry.Credentials, error) {
	in := bufio.NewReader(r)
	return func(host string) (registry.Credentials, error) {
		if _, err := fmt.Fprintf(w, "Registry %s returned 401.\nUsername: ", host); err != nil {
			return registry.Credentials{}, err
		}
		user, err := in.ReadString('\n')
		if err != nil && strings.TrimSpace(user) == "" {
			return registry.Credentials{}, err
		}
		if _, err := io.WriteString(w, "Token: "); err != nil {
			return registry.Credentials{}, err
		}
		token, err := in.ReadString('\n')
		if err != nil && strings.TrimSpace(token) == "" {
			return registry.Credentials{}, err
		}
		return registry.Credentials{Username: strings.TrimSpace(user), Password: strings.TrimSpace(token)}, nil
	}
}
