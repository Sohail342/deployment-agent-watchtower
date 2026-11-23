package registry

import "testing"

func TestParseUnqualifiedAndPinned(t *testing.T) {
	got, err := Parse("nginx:alpine")
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "docker.io" || got.Repository != "library/nginx" || got.Tag != "alpine" || got.Pinned {
		t.Fatalf("unexpected parse: %+v", got)
	}
	if got.LocalTag() != "nginx:alpine" {
		t.Fatalf("local tag: %s", got.LocalTag())
	}

	pinned, err := Parse("ghcr.io/acme/api@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !pinned.Pinned || pinned.Host != "ghcr.io" || pinned.Digest != "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("unexpected pinned parse: %+v", pinned)
	}
}

func TestMatchDigestIgnoresHubAlias(t *testing.T) {
	got := MatchDigest("nginx:alpine", []string{
		"docker.io/library/nginx@sha256:deadbeef",
		"ghcr.io/other/app@sha256:fff",
	})
	if got != "sha256:deadbeef" {
		t.Fatalf("digest: %q", got)
	}
}

func TestClassifyHost(t *testing.T) {
	cases := []struct {
		host string
		kind string
	}{
		{"docker.io", "docker_hub"},
		{"index.docker.io", "docker_hub"},
		{"ghcr.io", "ghcr"},
		{"123456789012.dkr.ecr.us-east-1.amazonaws.com", "ecr"},
		{"us-docker.pkg.dev", "artifact_registry"},
		{"registry.example.com", "generic"},
		{"localhost:5000", "generic"},
	}
	for _, tc := range cases {
		if got := Classify(tc.host); got != tc.kind {
			t.Errorf("Classify(%s) = %s, want %s", tc.host, got, tc.kind)
		}
	}
}
