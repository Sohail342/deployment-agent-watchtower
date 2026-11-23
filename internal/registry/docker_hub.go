package registry

import (
	"context"

	"github.com/google/go-containerregistry/pkg/authn"
)

// DockerHub serves docker.io and its aliases.
type DockerHub struct{}

func (DockerHub) Name() string { return "docker_hub" }

func (DockerHub) Match(host string) bool {
	return NormalizeHost(host) == "docker.io"
}

func (DockerHub) Authenticator(_ context.Context, _ string, base authn.Authenticator) (authn.Authenticator, error) {
	if base == nil {
		return authn.Anonymous, nil
	}
	return base, nil
}
