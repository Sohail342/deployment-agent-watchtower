package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	registrytypes "github.com/moby/moby/api/types/registry"
)

// Credentials is a username and secret for one registry host.
type Credentials struct {
	Username string
	Password string
}

// PromptFunc asks the operator for credentials. A nil prompt means the
// agent cannot ask, which is the case for a non-interactive restart.
type PromptFunc func(host string) (Credentials, error)

// SaveFunc persists credentials the way docker login does.
type SaveFunc func(ctx context.Context, host, username, password string) error

// Keychain resolves credentials already stored on the machine.
type Keychain interface {
	Resolve(authn.Resource) (authn.Authenticator, error)
}

// Resolver returns a remote manifest digest and the engine pull header.
type Resolver interface {
	Digest(ctx context.Context, image string) (string, error)
	PullAuth(ctx context.Context, image string) (string, error)
}

// Router picks a registry implementation from the image host.
type Router struct {
	providers []Provider
	head      func(ctx context.Context, image string, auth authn.Authenticator) (string, error)
	keychain  Keychain
	prompt    PromptFunc
	save      SaveFunc

	mu     sync.Mutex
	memory map[string]Credentials
	asked  map[string]struct{}
}

// Provider matches a registry host and builds an authenticator.
type Provider interface {
	Name() string
	Match(host string) bool
	Authenticator(ctx context.Context, host string, base authn.Authenticator) (authn.Authenticator, error)
}

// NewRouter wires the built-in registries. save may be nil.
func NewRouter(prompt PromptFunc, save SaveFunc) *Router {
	return &Router{
		providers: []Provider{
			ECR{},
			ArtifactRegistry{},
			GHCR{},
			DockerHub{},
		},
		head:     remoteHead,
		keychain: authn.DefaultKeychain,
		prompt:   prompt,
		save:     save,
		memory:   map[string]Credentials{},
		asked:    map[string]struct{}{},
	}
}

// Classify reports which provider owns host. Tests and logs use this.
func Classify(host string) string {
	host = NormalizeHost(host)
	for _, p := range []Provider{ECR{}, ArtifactRegistry{}, GHCR{}, DockerHub{}} {
		if p.Match(host) {
			return p.Name()
		}
	}
	return Generic{}.Name()
}

// Digest resolves the current manifest digest of a tagged image.
func (r *Router) Digest(ctx context.Context, image string) (string, error) {
	ref, err := Parse(image)
	if err != nil {
		return "", err
	}
	if ref.Pinned {
		return ref.Digest, nil
	}
	auth, err := r.authenticator(ctx, ref.Host)
	if err != nil {
		return "", err
	}
	digest, err := r.head(ctx, ref.Name, auth)
	if err == nil {
		return digest, nil
	}
	if !unauthorized(err) {
		return "", err
	}
	auth, err = r.promptOnce(ctx, ref.Host)
	if err != nil {
		return "", fmt.Errorf("registry %s: %w", ref.Host, err)
	}
	digest, err = r.head(ctx, ref.Name, auth)
	if err != nil {
		return "", err
	}
	return digest, nil
}

// PullAuth is the base64 engine header for image. Anonymous pulls return an empty string.
func (r *Router) PullAuth(ctx context.Context, image string) (string, error) {
	ref, err := Parse(image)
	if err != nil {
		return "", err
	}
	auth, err := r.authenticator(ctx, ref.Host)
	if err != nil {
		return "", err
	}
	header, err := encodeAuth(auth, ref.Host)
	if err != nil || header != "" || r.prompt == nil {
		return header, err
	}
	// A previous 401 may have stored credentials after Digest returned.
	return header, nil
}

func (r *Router) authenticator(ctx context.Context, host string) (authn.Authenticator, error) {
	host = NormalizeHost(host)
	if cred, ok := r.cached(host); ok {
		return authn.FromConfig(authn.AuthConfig{Username: cred.Username, Password: cred.Password}), nil
	}
	base, err := r.resolveKeychain(host)
	if err != nil {
		base = authn.Anonymous
	}
	provider := r.provider(host)
	return provider.Authenticator(ctx, host, base)
}

func (r *Router) provider(host string) Provider {
	host = NormalizeHost(host)
	for _, p := range r.providers {
		if p.Match(host) {
			return p
		}
	}
	return Generic{}
}

func (r *Router) resolveKeychain(host string) (authn.Authenticator, error) {
	if r.keychain == nil {
		return authn.Anonymous, nil
	}
	reg, err := name.NewRegistry(registryName(host), name.WeakValidation)
	if err != nil {
		return nil, err
	}
	return r.keychain.Resolve(reg)
}

func (r *Router) promptOnce(ctx context.Context, host string) (authn.Authenticator, error) {
	host = NormalizeHost(host)
	r.mu.Lock()
	if cred, ok := r.memory[host]; ok {
		r.mu.Unlock()
		return authn.FromConfig(authn.AuthConfig{Username: cred.Username, Password: cred.Password}), nil
	}
	if _, seen := r.asked[host]; seen {
		r.mu.Unlock()
		return nil, fmt.Errorf("authentication failed")
	}
	r.asked[host] = struct{}{}
	prompt := r.prompt
	r.mu.Unlock()
	if prompt == nil {
		return nil, fmt.Errorf("authentication required and no interactive prompt is available")
	}
	cred, err := prompt(host)
	if err != nil {
		return nil, err
	}
	if cred.Username == "" && cred.Password == "" {
		return nil, fmt.Errorf("empty credentials")
	}
	r.remember(host, cred)
	if r.save != nil {
		if err := r.save(ctx, host, cred.Username, cred.Password); err != nil {
			return nil, err
		}
	}
	return authn.FromConfig(authn.AuthConfig{Username: cred.Username, Password: cred.Password}), nil
}

func (r *Router) remember(host string, cred Credentials) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.memory[NormalizeHost(host)] = cred
}

func (r *Router) cached(host string) (Credentials, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cred, ok := r.memory[NormalizeHost(host)]
	return cred, ok
}

func remoteHead(ctx context.Context, image string, auth authn.Authenticator) (string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", err
	}
	if auth == nil {
		auth = authn.Anonymous
	}
	desc, err := remote.Head(ref, remote.WithContext(ctx), remote.WithAuth(auth))
	if err != nil {
		return "", err
	}
	return desc.Digest.String(), nil
}

func unauthorized(err error) bool {
	var te *transport.Error
	if errors.As(err, &te) {
		return te.StatusCode == 401 || te.StatusCode == 403
	}
	return false
}

func encodeAuth(auth authn.Authenticator, host string) (string, error) {
	if auth == nil || auth == authn.Anonymous {
		return "", nil
	}
	cfg, err := auth.Authorization()
	if err != nil {
		return "", err
	}
	user, secret := cfg.Username, cfg.Password
	if user == "" && secret == "" && cfg.Auth != "" {
		raw, decErr := base64.StdEncoding.DecodeString(cfg.Auth)
		if decErr == nil {
			user, secret, _ = strings.Cut(string(raw), ":")
		}
	}
	if user == "" && secret == "" && cfg.IdentityToken == "" && cfg.RegistryToken == "" {
		return "", nil
	}
	payload := registrytypes.AuthConfig{
		Username:      user,
		Password:      secret,
		ServerAddress: host,
		IdentityToken: cfg.IdentityToken,
		RegistryToken: cfg.RegistryToken,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(buf), nil
}

func registryName(host string) string {
	if NormalizeHost(host) == "docker.io" {
		return "index.docker.io"
	}
	return host
}

// SaveDockerConfig writes credentials into the Docker config or credential helper.
func SaveDockerConfig(host, username, password string) error {
	path, err := dockerConfigPath()
	if err != nil {
		return err
	}
	cfg, err := readDockerConfig(path)
	if err != nil {
		return err
	}
	server := serverURL(host)
	if cfg.CredsStore != "" {
		return storeHelper(cfg.CredsStore, server, username, password)
	}
	if helper := cfg.CredHelpers[NormalizeHost(host)]; helper != "" {
		return storeHelper(helper, server, username, password)
	}
	if cfg.Auths == nil {
		cfg.Auths = map[string]dockerAuth{}
	}
	cfg.Auths[server] = dockerAuth{Auth: base64.StdEncoding.EncodeToString([]byte(username + ":" + password))}
	return writeDockerConfig(path, cfg)
}

type dockerConfigFile struct {
	Auths       map[string]dockerAuth `json:"auths"`
	CredsStore  string                `json:"credsStore,omitempty"`
	CredHelpers map[string]string     `json:"credHelpers,omitempty"`
}

type dockerAuth struct {
	Auth string `json:"auth"`
}

func dockerConfigPath() (string, error) {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return filepath.Join(dir, "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".docker", "config.json"), nil
}

func readDockerConfig(path string) (dockerConfigFile, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return dockerConfigFile{Auths: map[string]dockerAuth{}}, nil
		}
		return dockerConfigFile{}, err
	}
	var cfg dockerConfigFile
	if len(strings.TrimSpace(string(buf))) == 0 {
		return dockerConfigFile{Auths: map[string]dockerAuth{}}, nil
	}
	if err := json.Unmarshal(buf, &cfg); err != nil {
		return dockerConfigFile{}, err
	}
	if cfg.Auths == nil {
		cfg.Auths = map[string]dockerAuth{}
	}
	return cfg, nil
}

func writeDockerConfig(path string, cfg dockerConfigFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	return os.WriteFile(path, buf, 0o600)
}

func serverURL(host string) string {
	if NormalizeHost(host) == "docker.io" {
		return "https://index.docker.io/v1/"
	}
	return "https://" + host
}

func storeHelper(helper, server, username, password string) error {
	payload, err := json.Marshal(map[string]string{
		"ServerURL": server,
		"Username":  username,
		"Secret":    password,
	})
	if err != nil {
		return err
	}
	cmd := exec.Command("docker-credential-"+helper, "store")
	cmd.Stdin = strings.NewReader(string(payload))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker-credential-%s store: %w: %s", helper, err, strings.TrimSpace(string(out)))
	}
	return nil
}
