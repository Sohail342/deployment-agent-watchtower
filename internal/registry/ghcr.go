package registry

import (
	"context"

	"github.com/google/go-containerregistry/pkg/authn"
)

// GHCR serves ghcr.io. A personal access token with read:packages is the password.
type GHCR struct{}

func (GHCR) Name() string { return "ghcr" }

func (GHCR) Match(host string) bool {
	return NormalizeHost(host) == "ghcr.io"
}

func (GHCR) Authenticator(_ context.Context, _ string, base authn.Authenticator) (authn.Authenticator, error) {
	if base == nil {
		return authn.Anonymous, nil
	}
	return base, nil
}
