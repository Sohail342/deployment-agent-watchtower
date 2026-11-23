package deployment

import (
	"testing"

	"deployment-agent/internal/discovery"
)

func TestBuildSkipsUnchangedPinnedAndRejected(t *testing.T) {
	cands := []discovery.Candidate{
		{Key: "web", Name: "web", Image: "nginx:alpine", CurrentDigest: "sha256:old"},
		{Key: "same", Name: "same", Image: "redis:7", CurrentDigest: "sha256:same"},
		{Key: "pin", Name: "pin", Image: "app@sha256:fff", Pinned: true, CurrentDigest: "sha256:fff"},
		{Key: "local", Name: "local", Image: "myapp:dev", LocalOnly: true},
		{Key: "stopped", Name: "stopped", Image: "nginx:alpine", Stopped: true, CurrentDigest: "sha256:old"},
		{Key: "bad", Name: "bad", Image: "nginx:alpine", CurrentDigest: "sha256:old"},
		{Key: "agent", Name: "agent", IsAgent: true, CurrentDigest: "sha256:old"},
	}
	remote := map[string]string{
		"web": "sha256:new", "same": "sha256:same", "bad": "sha256:rejected", "agent": "sha256:new",
	}
	plan := Build(cands, remote, map[string]string{"bad": "sha256:rejected"})
	if len(plan.Steps) != 1 || plan.Steps[0].Key != "web" || plan.Steps[0].ToDigest != "sha256:new" {
		t.Fatalf("%+v", plan.Steps)
	}
}
