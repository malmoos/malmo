package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/molmaos/molma/internal/protocol"
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
	// ServiceUp brings up a managed-service compose project
	// (SERVICE_PROVISIONING.md # Tier 1). Unlike ComposeUp it uses only the
	// generated compose.yml + .env (no per-app override): a service instance is a
	// brain-owned container, not a user app.
	ServiceUp(ctx context.Context, dir, project string) (string, error)
	// Exec runs a command inside a named container (`docker exec <name> <args…>`)
	// and returns combined output. The brain uses it to provision per-app
	// databases/roles via the service container's own client (e.g. psql), so it
	// never has to join the service's Docker network (DECISIONS.md 2026-06-02 —
	// control plane off app-reachable networks).
	Exec(ctx context.Context, container string, args []string) (string, error)
	Inspect(ctx context.Context, instanceID, mainService string) (running bool, health string, err error)
	NetworkCreate(ctx context.Context, name string, internal bool) error
	NetworkRemove(ctx context.Context, name string) error
	PSManaged(ctx context.Context) (map[string]bool, error)
	// RestartCounts reports each managed instance's cumulative container
	// RestartCount (max across the instance's containers), keyed by
	// instance_id. Used by the brain's container-restart-loop detector to
	// sample restart deltas over a window (HEALTH.md # Detector catalog, locus
	// D). Read-only; needs only the proxy's CONTAINERS endpoint family.
	RestartCounts(ctx context.Context) (map[string]int, error)
	// ManagedContainers reports every managed container's owning instance_id,
	// compose service, running state, and StartedAt. Used by the brain's
	// app-unresponsive probe to gate on the main service's steady-running state
	// and start-period grace (HEALTH.md # Detector catalog, locus C). Read-only;
	// needs only the proxy's CONTAINERS endpoint family.
	ManagedContainers(ctx context.Context) ([]ManagedContainer, error)
	// RemoveContainersByInstance is the orphan-teardown escape hatch: when the
	// instance dir is gone, compose can't drive the cleanup, so we kill all
	// containers labeled molma.instance_id=<id> directly.
	RemoveContainersByInstance(ctx context.Context, instanceID string) error
	// RemoveImage removes one locally-stored image by its pinned `repo@sha256:…`
	// reference (APP_LIFECYCLE.md # stop, start, uninstall — uninstall-time image
	// reclaim). Un-forced: if the image is still referenced (another tag, a
	// stopped container), docker refuses and the caller treats it as best-effort.
	RemoveImage(ctx context.Context, ref string) error
}

// ManagedContainer is one managed container's identity and liveness, as read
// from `docker inspect`. Service is the compose service name
// (com.docker.compose.service label); StartedAt is zero for a never-started
// container.
type ManagedContainer struct {
	InstanceID string
	Service    string
	Running    bool
	StartedAt  time.Time
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
	// by writeOverride to build bind-mount sources and the user: directive for
	// personal-scope app instances.
	ResolveHome(ctx context.Context, user string) (protocol.ResolveHomeResponse, error)
	// WellKnownIdentity returns the fixed host service identities: the molma-app
	// service UID/GID a household instance runs as, and the molma-shared GID a
	// shared-source folder mount joins via group_add.
	WellKnownIdentity(ctx context.Context) (protocol.WellKnownIdentityResponse, error)
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

func (cliDocker) ServiceUp(ctx context.Context, dir, project string) (string, error) {
	// A service project has a single generated compose.yml + .env — no override.
	base := []string{"compose", "-f", "compose.yml", "--env-file", ".env", "-p", project, "up", "-d"}
	cmd := exec.CommandContext(ctx, "docker", base...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (cliDocker) Exec(ctx context.Context, container string, args []string) (string, error) {
	full := append([]string{"exec", container}, args...)
	out, err := exec.CommandContext(ctx, "docker", full...).CombinedOutput()
	return string(out), err
}

func (cliDocker) Inspect(ctx context.Context, instanceID, mainService string) (bool, string, error) {
	cid, err := exec.CommandContext(ctx, "docker", "ps", "-q",
		"--filter", "label=molma.instance_id="+instanceID,
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
		"--filter", "label=molma.instance_id="+instanceID).Output()
	if err != nil {
		return err
	}
	for _, cid := range strings.Fields(string(ids)) {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", cid).Run()
	}
	return nil
}

func (cliDocker) RemoveImage(ctx context.Context, ref string) error {
	if out, err := exec.CommandContext(ctx, "docker", "rmi", ref).CombinedOutput(); err != nil {
		return fmt.Errorf("rmi %s: %w\n%s", ref, err, out)
	}
	return nil
}

func (cliDocker) RestartCounts(ctx context.Context) (map[string]int, error) {
	ids, err := exec.CommandContext(ctx, "docker", "ps", "-aq",
		"--filter", "label=molma.managed=true").Output()
	if err != nil {
		return nil, err
	}
	idList := strings.Fields(string(ids))
	res := map[string]int{}
	if len(idList) == 0 {
		return res, nil
	}
	// One `docker inspect` over all managed containers: one line each, mapping
	// the owning instance_id to that container's cumulative RestartCount.
	args := append([]string{"inspect", "--format",
		`{{index .Config.Labels "molma.instance_id"}} {{.RestartCount}}`}, idList...)
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		// An instance can have several containers; the loop is per-container, so
		// take the max — raise if any one container is crash-looping.
		if n > res[parts[0]] {
			res[parts[0]] = n
		}
	}
	return res, nil
}

func (cliDocker) ManagedContainers(ctx context.Context) ([]ManagedContainer, error) {
	ids, err := exec.CommandContext(ctx, "docker", "ps", "-aq",
		"--filter", "label=molma.managed=true").Output()
	if err != nil {
		return nil, err
	}
	idList := strings.Fields(string(ids))
	if len(idList) == 0 {
		return nil, nil
	}
	// One `docker inspect` over all managed containers: instance_id, compose
	// service, running flag, and StartedAt (RFC3339) per line.
	args := append([]string{"inspect", "--format",
		`{{index .Config.Labels "molma.instance_id"}} {{index .Config.Labels "com.docker.compose.service"}} {{.State.Running}} {{.State.StartedAt}}`}, idList...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		// Docker daemon unreachable (not a TOCTOU race — that produces partial
		// stdout). Return the error so the caller can skip this tick.
		return nil, err
	}
	// TOCTOU: a container removed between the `docker ps` above and this inspect
	// causes Docker to exit non-zero but still outputs the containers it inspected.
	// Parse partial output — the missing containers simply won't appear.
	var res []ManagedContainer
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 4 {
			continue
		}
		// Zero time on a parse failure (never-started container) — the caller's
		// start-period gate treats it as not-yet-eligible, which is correct.
		started, _ := time.Parse(time.RFC3339Nano, parts[3])
		res = append(res, ManagedContainer{
			InstanceID: parts[0],
			Service:    parts[1],
			Running:    parts[2] == "true",
			StartedAt:  started,
		})
	}
	return res, nil
}

func (cliDocker) PSManaged(ctx context.Context) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label=molma.managed=true",
		"--format", `{{.Label "molma.instance_id"}} {{.State}}`).Output()
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
