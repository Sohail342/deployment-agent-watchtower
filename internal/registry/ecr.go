package registry

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/google/go-containerregistry/pkg/authn"
)

var ecrHost = regexp.MustCompile(`(?i)^[0-9]+\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com(\.cn)?$`)

var (
	ecrMu     sync.Mutex
	ecrTokens = map[string]ecrCache{}
)

// ECR takes credentials from the AWS default chain. Nothing is written into a config file.
type ECR struct{}

type ecrCache struct {
	auth    authn.Authenticator
	expires time.Time
}

func (ECR) Name() string { return "ecr" }

func (ECR) Match(host string) bool {
	return ecrHost.MatchString(strings.TrimSpace(host))
}

func (ECR) Authenticator(ctx context.Context, host string, _ authn.Authenticator) (authn.Authenticator, error) {
	ecrMu.Lock()
	defer ecrMu.Unlock()
	if hit, ok := ecrTokens[host]; ok && time.Now().Before(hit.expires) {
		return hit.auth, nil
	}
	region, err := ecrRegion(host)
	if err != nil {
		return nil, err
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("ecr config: %w", err)
	}
	out, err := ecr.NewFromConfig(cfg).GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return nil, fmt.Errorf("ecr token: %w", err)
	}
	if len(out.AuthorizationData) == 0 || out.AuthorizationData[0].AuthorizationToken == nil {
		return nil, fmt.Errorf("ecr token: empty authorization data")
	}
	raw, err := base64.StdEncoding.DecodeString(*out.AuthorizationData[0].AuthorizationToken)
	if err != nil {
		return nil, fmt.Errorf("ecr token decode: %w", err)
	}
	user, secret, ok := strings.Cut(string(raw), ":")
	if !ok {
		return nil, fmt.Errorf("ecr token decode: missing separator")
	}
	auth := authn.FromConfig(authn.AuthConfig{Username: user, Password: secret})
	exp := time.Now().Add(10 * time.Minute)
	if out.AuthorizationData[0].ExpiresAt != nil {
		exp = out.AuthorizationData[0].ExpiresAt.Add(-time.Minute)
	}
	ecrTokens[host] = ecrCache{auth: auth, expires: exp}
	return auth, nil
}

func ecrRegion(host string) (string, error) {
	parts := strings.Split(host, ".")
	if len(parts) < 4 || parts[1] != "dkr" || parts[2] != "ecr" {
		return "", fmt.Errorf("cannot read region from ecr host %q", host)
	}
	return parts[3], nil
}
