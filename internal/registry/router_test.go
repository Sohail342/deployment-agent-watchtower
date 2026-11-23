package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

type anonymousKeychain struct{}

func (anonymousKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	return authn.Anonymous, nil
}

func TestDigestPromptsOnceOnUnauthorized(t *testing.T) {
	var calls int
	var prompted int
	r := NewRouter(func(host string) (Credentials, error) {
		prompted++
		if host != "ghcr.io" {
			t.Fatalf("host %s", host)
		}
		return Credentials{Username: "bot", Password: "tok"}, nil
	}, func(context.Context, string, string, string) error { return nil })
	r.keychain = anonymousKeychain{}
	r.head = func(_ context.Context, image string, auth authn.Authenticator) (string, error) {
		calls++
		if image != "ghcr.io/acme/api:prod" {
			t.Fatalf("image %s", image)
		}
		cfg, err := auth.Authorization()
		if err != nil {
			return "", err
		}
		if cfg.Password == "" {
			return "", &transport.Error{StatusCode: 401}
		}
		if cfg.Username != "bot" || cfg.Password != "tok" {
			t.Fatalf("auth %+v", cfg)
		}
		return "sha256:abc", nil
	}

	got, err := r.Digest(context.Background(), "ghcr.io/acme/api:prod")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sha256:abc" || prompted != 1 || calls != 2 {
		t.Fatalf("digest %s prompted %d calls %d", got, prompted, calls)
	}
	header, err := r.PullAuth(context.Background(), "ghcr.io/acme/api:prod")
	if err != nil {
		t.Fatal(err)
	}
	if header == "" {
		t.Fatal("expected pull auth header")
	}
}

func TestDigestDoesNotPromptTwice(t *testing.T) {
	r := NewRouter(func(string) (Credentials, error) {
		return Credentials{}, errors.New("nope")
	}, nil)
	r.keychain = anonymousKeychain{}
	r.head = func(context.Context, string, authn.Authenticator) (string, error) {
		return "", &transport.Error{StatusCode: 401}
	}
	if _, err := r.Digest(context.Background(), "example.com/app:1"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := r.Digest(context.Background(), "example.com/app:1"); err == nil {
		t.Fatal("expected cached failure")
	}
}
