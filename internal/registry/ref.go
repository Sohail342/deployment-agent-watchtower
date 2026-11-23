package registry

import (
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// Ref is a parsed image reference. Host is normalized so docker.io and
// index.docker.io compare as the same registry.
type Ref struct {
	Raw        string
	Name       string
	Host       string
	Repository string
	RepoName   string
	Tag        string
	Digest     string
	Pinned     bool
}

// Parse normalizes an image reference. Unqualified names belong to docker.io.
func Parse(image string) (Ref, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return Ref{}, fmt.Errorf("empty image reference")
	}
	parsed, err := name.ParseReference(image)
	if err != nil {
		return Ref{}, fmt.Errorf("parse image %q: %w", image, err)
	}
	ref := Ref{
		Raw:        image,
		Host:       NormalizeHost(parsed.Context().RegistryStr()),
		Repository: parsed.Context().RepositoryStr(),
		RepoName:   parsed.Context().Name(),
	}
	switch t := parsed.(type) {
	case name.Tag:
		ref.Name = t.Name()
		ref.Tag = t.TagStr()
	case name.Digest:
		ref.Name = t.Name()
		ref.Digest = t.DigestStr()
		ref.Pinned = true
	default:
		ref.Name = parsed.Name()
		ref.Tag = parsed.Identifier()
	}
	return ref, nil
}

// LocalTag is the tag the engine should move after a digest pull.
func (r Ref) LocalTag() string {
	if r.Pinned {
		return ""
	}
	if r.Raw != "" && !strings.Contains(r.Raw, "@") {
		return r.Raw
	}
	return r.Name
}

// WithDigest returns repo@digest for the parsed repository.
func (r Ref) WithDigest(digest string) string {
	return r.RepoName + "@" + strings.TrimPrefix(digest, "@")
}

// NormalizeHost maps Docker Hub aliases onto docker.io.
func NormalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	switch host {
	case "", "docker.io", "index.docker.io", "registry-1.docker.io":
		return "docker.io"
	default:
		return host
	}
}

// MatchDigest returns the digest from repoDigests that belongs to image.
// Digest strings are compared without the algorithm prefix differences
// introduced by docker.io versus index.docker.io.
func MatchDigest(image string, repoDigests []string) string {
	ref, err := Parse(image)
	if err != nil {
		return ""
	}
	want := normalizeRepo(ref.RepoName)
	for _, rd := range repoDigests {
		namePart, digest, ok := strings.Cut(rd, "@")
		if !ok || digest == "" {
			continue
		}
		if normalizeRepo(namePart) == want {
			return digest
		}
	}
	return ""
}

func normalizeRepo(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return ""
	}
	parsed, err := name.NewRepository(repo)
	if err != nil {
		return strings.ToLower(repo)
	}
	return NormalizeHost(parsed.RegistryStr()) + "/" + parsed.RepositoryStr()
}
