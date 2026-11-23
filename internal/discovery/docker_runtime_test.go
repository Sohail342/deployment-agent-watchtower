package discovery

import "testing"

func TestWatchtowerSkipsPinnedLocalAndContainerNetwork(t *testing.T) {
	containers := []Container{
		{ID: "aaa111", Name: "web", Image: "nginx:alpine", Running: true, CurrentDigest: "sha256:aaa"},
		{ID: "bbb222", Name: "job", Image: "ghcr.io/acme/job@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Running: true, CurrentDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{ID: "ccc333", Name: "built", Image: "myapp:dev", Running: true},
		{ID: "ddd444", Name: "sidecar", Image: "busybox", Running: true, NetworkMode: "container:aaa111", CurrentDigest: "sha256:bbb"},
		{ID: "eee555", Name: "old", Image: "redis:7", Running: false, CurrentDigest: "sha256:ccc"},
		{ID: "fff666", Name: "web.deployment-agent-previous", Image: "nginx:alpine", Running: false, CurrentDigest: "sha256:aaa"},
		{ID: "abcabcabcabc", Name: "deployment-agent", Image: "ghcr.io/acme/agent:latest", Running: true, CurrentDigest: "sha256:ddd"},
	}
	got := Watchtower(containers, "deployment-agent", "abcabcabcabc")
	byName := map[string]Candidate{}
	for _, c := range got {
		byName[c.Name] = c
	}
	if _, ok := byName["sidecar"]; ok {
		t.Fatal("container network mode should be skipped")
	}
	if _, ok := byName["web.deployment-agent-previous"]; ok {
		t.Fatal("previous generation should be skipped")
	}
	if !byName["job"].Pinned {
		t.Fatal("expected pinned digest")
	}
	if !byName["built"].LocalOnly {
		t.Fatal("image without a repo digest is local")
	}
	if !byName["old"].Stopped {
		t.Fatal("stopped container should be marked")
	}
	if !byName["deployment-agent"].IsAgent {
		t.Fatal("expected agent container")
	}
	if byName["web"].Host != "docker.io" || byName["web"].CurrentDigest != "sha256:aaa" {
		t.Fatalf("web: %+v", byName["web"])
	}
}
