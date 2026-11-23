package registry

import (
	"context"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"golang.org/x/oauth2/google"
)

// ArtifactRegistry uses application default credentials. A 401 still falls
// through to the one-time prompt in the router.
type ArtifactRegistry struct{}

func (ArtifactRegistry) Name() string { return "artifact_registry" }

func (ArtifactRegistry) Match(host string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(host)), ".pkg.dev")
}

func (ArtifactRegistry) Authenticator(ctx context.Context, _ string, base authn.Authenticator) (authn.Authenticator, error) {
	ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return fallbackAuth(base), nil
	}
	tok, err := ts.Token()
	if err != nil {
		return fallbackAuth(base), nil
	}
	return authn.FromConfig(authn.AuthConfig{
		Username: "oauth2accesstoken",
		Password: tok.AccessToken,
	}), nil
}

func fallbackAuth(base authn.Authenticator) authn.Authenticator {
	if base == nil {
		return authn.Anonymous
	}
	return base
}
