package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/malmo/malmo/internal/protocol"
)

// DockerDriver is the narrow surface lifecycle needs from Docker. Production
// is a thin `docker`/`docker compose` CLI wrapper; tests use a recording fake
// (see internal/lifecycle/lifecycletest). Extracting this is the enabling
// refactor for the test plan in docs/dev/testing-brain.md.
type DockerDriver interface {
	Pull(ctx context.Context, image string) error
	ImageInspect(ctx context.Context, image string) (RepoDigests, error)
	ComposeUp(ctx context.Context, dir, project string) (string, error)
	ComposeDown(ctx context.Context, dir, project string) (string, error)
	ComposeStop(ctx context.Context, dir, project string) (string, error)
	Inspect(ctx context.Context, instanceID, mainService string) (running bool, health string, err error)
	NetworkCreate(ctx context.Context, name string, internal bool) error
	NetworkRemove(ctx context.Context, name string) error
	PSManaged(ctx context.Context) (map[string]bool, error)
	// RemoveContainersByInstance is the orphan-teardown escape hatch: when the
	// instance dir is gone, compose can't drive the cleanup, so we kill all
	// containers labeled malmo.instance_id=<id> directly.
	RemoveContainersByInstance(ctx context.Context, instanceID string) error
}

// RepoDigests is the `RepoDigests` field of `docker image inspect`: a list of
// `repo@sha256:…` strings the local image is known under.
type RepoDigests []string

// CaddyDriver is the slice of caddy.Client that lifecycle uses.
type CaddyDriver interface {
	EnsureServer(ctx context.Context, listen string) error
	AddRoute(ctx context.Context, instanceID, host, upstream string) error
	AddSplashRoute(ctx context.Context, instanceID, host, appName, state string) error
	RemoveRoute(ctx context.Context, instanceID string) error
}

// HostDriver is the slice of hostclient.Client that lifecycle uses.
type HostDriver interface {
	Publish(ctx context.Context, slug string) (protocol.PublishResponse, error)
	Unpublish(ctx context.Context, slug string) error
	// ResolveHome returns the owner's home directory path, UID, and GID. Used
	// by writeOverride (slice 4) to build bind-mount sources and user: directives
	// for personal app instances.
	ResolveHome(ctx context.Context, user string) (protocol.ResolveHomeResponse, error)
}

// Admitter validates a verbatim compose. Default impl is admission.Check; tests
// inject admission.CheckStructure to skip the `docker compose config -q`
// syntax pass that needs a real daemon.
type Admitter func(ctx context.Context, composeBytes []byte) error

// NewCLIDocker returns the production driver: shells out to `docker` and
// `docker compose` exactly as the previous inline calls did.
func NewCLIDocker() DockerDriver { return cliDocker{} }

type cliDocker struct{}

func (cliDocker) Pull(ctx context.Context, image string) error {
	if out, err := exec.CommandContext(ctx, "docker", "pull", image).CombinedOutput(); err != nil {
		return fmt.Errorf("pull %s: %w\n%s", image, err, out)
	}
	return nil
}

func (cliDocker) ImageInspect(ctx context.Context, image string) (RepoDigests, error) {
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect",
		"--format", "{{json .RepoDigests}}", image).Output()
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", image, err)
	}
	var rd RepoDigests
	if err := json.Unmarshal(out, &rd); err != nil {
		return nil, fmt.Errorf("parse RepoDigests for %s: %w", image, err)
	}
	return rd, nil
}

func (cliDocker) ComposeUp(ctx context.Context, dir, project string) (string, error) {
	return composeRun(ctx, dir, project, "up", "-d")
}

func (cliDocker) ComposeDown(ctx context.Context, dir, project string) (string, error) {
	return composeRun(ctx, dir, project, "down", "-v")
}

func (cliDocker) ComposeStop(ctx context.Context, dir, project string) (string, error) {
	return composeRun(ctx, dir, project, "stop")
}

func composeRun(ctx context.Context, dir, project string, args ...string) (string, error) {
	base := []string{
		"compose",
		"-f", "compose.yml",
		"-f", "compose.override.yml",
		"--env-file", ".env",
		"-p", project,
	}
	cmd := exec.CommandContext(ctx, "docker", append(base, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (cliDocker) Inspect(ctx context.Context, instanceID, mainService string) (bool, string, error) {
	cid, err := exec.CommandContext(ctx, "docker", "ps", "-q",
		"--filter", "label=malmo.instance_id="+instanceID,
		"--filter", "label=com.docker.compose.service="+mainService).Output()
	container := strings.TrimSpace(string(cid))
	if err != nil || container == "" {
		return false, "", fmt.Errorf("main_service container not found")
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{.State.Running}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}`,
		container).Output()
	if err != nil {
		return false, "", err
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) < 2 {
		return false, "", fmt.Errorf("unexpected inspect output: %q", out)
	}
	return parts[0] == "true", parts[1], nil
}

func (cliDocker) NetworkCreate(ctx context.Context, name string, internal bool) error {
	args := []string{"network", "create"}
	if internal {
		args = append(args, "--internal")
	}
	args = append(args, name)
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil && strings.Contains(string(out), "already exists") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func (cliDocker) NetworkRemove(ctx context.Context, name string) error {
	return exec.CommandContext(ctx, "docker", "network", "rm", name).Run()
}

func (cliDocker) RemoveContainersByInstance(ctx context.Context, instanceID string) error {
	ids, err := exec.CommandContext(ctx, "docker", "ps", "-aq",
		"--filter", "label=malmo.instance_id="+instanceID).Output()
	if err != nil {
		return err
	}
	for _, cid := range strings.Fields(string(ids)) {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", cid).Run()
	}
	return nil
}

func (cliDocker) PSManaged(ctx context.Context) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label=malmo.managed=true",
		"--format", `{{.Label "malmo.instance_id"}} {{.State}}`).Output()
	if err != nil {
		return nil, err
	}
	res := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		id := parts[0]
		running := len(parts) > 1 && parts[1] == "running"
		res[id] = res[id] || running
	}
	return res, nil
}
