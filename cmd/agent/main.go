package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"deployment-agent/internal/core"
	"deployment-agent/internal/docker"
	"deployment-agent/internal/registry"
	"deployment-agent/pkg/logger"
)

func main() {
	opt := parse()
	log := logger.New(opt.level, os.Stderr)
	if err := start(log, opt); err != nil {
		log.Error("agent stopped", "err", err)
		os.Exit(1)
	}
}

type options struct {
	dockerHost, stateDir, listen, composeBin, name, level string
	interval, jitter, healthTimeout, stopTimeout          time.Duration
	reconfigure                                           bool
}

func parse() options {
	opt := options{}
	flag.StringVar(&opt.dockerHost, "docker-host", "", "Docker daemon host. Defaults to DOCKER_HOST or the local socket")
	flag.StringVar(&opt.stateDir, "state-dir", core.DefaultStateDir(), "directory for the session and rollback memory")
	flag.DurationVar(&opt.interval, "interval", 5*time.Minute, "time between reconciles")
	flag.DurationVar(&opt.jitter, "jitter", 30*time.Second, "extra random delay added to the interval")
	flag.StringVar(&opt.listen, "listen", "127.0.0.1:9090", "local-only health address")
	flag.StringVar(&opt.level, "log-level", "info", "debug, info, warn, or error")
	flag.StringVar(&opt.composeBin, "compose-bin", "docker-compose", "Compose v2 binary used to recreate a project")
	flag.StringVar(&opt.name, "name", "deployment-agent", "container name of this agent, when it is containerized")
	flag.BoolVar(&opt.reconfigure, "reconfigure", false, "ask for the pull mode again")
	flag.DurationVar(&opt.healthTimeout, "health-timeout", 60*time.Second, "how long a recreated container may take to become healthy")
	flag.DurationVar(&opt.stopTimeout, "stop-timeout", 30*time.Second, "how long to wait for a container to stop before replacing it")
	flag.Parse()
	return opt
}

func start(log *slog.Logger, opt options) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := os.MkdirAll(opt.stateDir, 0o755); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(opt.stateDir, "agent.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("another deployment agent holds %s", opt.stateDir)
	}
	defer lock.Unlock()

	eng, err := docker.Dial(opt.dockerHost, log)
	if err != nil {
		return err
	}
	defer eng.Close()

	ln, err := docker.ListenLocal(opt.listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()

	inv, err := core.NewInventory(eng, opt.stateDir, log)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	interactive := isTerminal(os.Stdin)
	var prompt registry.PromptFunc
	if interactive {
		prompt = core.RegistryPrompt(os.Stdin, os.Stdout)
	}
	agent := &core.Agent{
		Inv: inv, Eng: eng,
		Resolver: registry.NewRouter(prompt, func(ctx context.Context, host, user, secret string) error {
			return eng.Login(ctx, host, user, secret)
		}),
		Log: log, Interval: opt.interval, Jitter: opt.jitter,
		HealthTimeout: opt.healthTimeout, StopTimeout: opt.stopTimeout,
		ComposeBin: opt.composeBin, SelfName: opt.name, Hostname: host,
		StateDir: opt.stateDir, Reconfigure: opt.reconfigure, Interactive: interactive,
		In: os.Stdin, Out: os.Stdout,
	}
	err = agent.Run(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, core.ErrDeclined) {
		return nil
	}
	return err
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
