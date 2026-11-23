package registry

import (
	"context"

	"github.com/google/go-containerregistry/pkg/authn"
)

// Generic is any other Distribution registry. The token challenge is handled
// by the manifest client once an authenticator is attached.
type Generic struct{}

func (Generic) Name() string { return "generic" }

func (Generic) Match(string) bool { return true }

func (Generic) Authenticator(_ context.Context, _ string, base authn.Authenticator) (authn.Authenticator, error) {
	if base == nil {
		return authn.Anonymous, nil
	}
	return base, nil
}
