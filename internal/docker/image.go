package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/moby/moby/client"

	"deployment-agent/internal/registry"
)

// PullAndTag pulls image@digest and moves the original tag onto that image.
func (c *Client) PullAndTag(ctx context.Context, tagRef, digest, authHeader string) error {
	ref, err := registry.Parse(tagRef)
	if err != nil {
		return err
	}
	if ref.Pinned {
		return fmt.Errorf("image %s is already pinned", tagRef)
	}
	digest = strings.TrimSpace(digest)
	if !strings.HasPrefix(digest, "sha256:") && !strings.Contains(digest, ":") {
		return fmt.Errorf("digest %q is not a manifest digest", digest)
	}
	pinned := ref.WithDigest(digest)
	pull, err := c.api.ImagePull(ctx, pinned, client.ImagePullOptions{RegistryAuth: authHeader})
	if err != nil {
		return fmt.Errorf("pull %s: %w", pinned, err)
	}
	defer pull.Close()
	if err := pull.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", pinned, err)
	}
	ins, err := c.api.ImageInspect(ctx, pinned)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", pinned, err)
	}
	if !digestPresent(ins.RepoDigests, digest) {
		return fmt.Errorf("pulled %s but digest %s is not in RepoDigests", pinned, digest)
	}
	target := ref.LocalTag()
	if _, err := c.api.ImageTag(ctx, client.ImageTagOptions{Source: pinned, Target: target}); err != nil {
		return fmt.Errorf("tag %s as %s: %w", pinned, target, err)
	}
	c.log.Info("pulled image", "image", target, "digest", digest)
	return nil
}

func digestPresent(repoDigests []string, digest string) bool {
	for _, item := range repoDigests {
		if strings.HasSuffix(item, "@"+digest) || item == digest {
			return true
		}
	}
	return false
}
