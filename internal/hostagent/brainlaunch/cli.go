package brainlaunch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CLIDocker is the production Docker, a thin wrapper over the `docker` CLI —
// the same approach as internal/lifecycle's cliDocker. It is the integration
// seam exercised by the QEMU lane, not unit tests (there is no Docker daemon in
// the brain test environment); the bootstrap logic in Launch is covered against
// the Docker interface with a fake.
type CLIDocker struct{}

// NewCLIDocker returns the production Docker backed by the `docker` CLI.
func NewCLIDocker() CLIDocker { return CLIDocker{} }

// ImagePresent uses `docker images -q <ref>`: it prints the image ID when the
// image exists and nothing when it doesn't, exiting 0 either way — so a
// non-empty result is "present" and a non-zero exit is a real Docker error
// (daemon down), not "absent".
func (CLIDocker) ImagePresent(ctx context.Context, ref string) (bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "images", "-q", ref).Output()
	if err != nil {
		return false, fmt.Errorf("docker images -q %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func (CLIDocker) Load(ctx context.Context, tarPath string) error {
	if out, err := exec.CommandContext(ctx, "docker", "load", "-i", tarPath).CombinedOutput(); err != nil {
		return fmt.Errorf("docker load -i %s: %w\n%s", tarPath, err, out)
	}
	return nil
}

// ImageLabel reads one OCI label off an image. A label the image doesn't carry
// renders empty (Go template index of an absent map key), which the caller
// treats as a mismatch.
func (CLIDocker) ImageLabel(ctx context.Context, ref, label string) (string, error) {
	format := fmt.Sprintf(`{{index .Config.Labels %q}}`, label)
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", format, ref).Output()
	if err != nil {
		return "", fmt.Errorf("docker image inspect %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ContainerExists filters by exact name (`^/<name>$`) over all containers,
// running or not, so a stopped brain still counts as present.
func (CLIDocker) ContainerExists(ctx context.Context, name string) (bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "name=^/"+name+"$", "--format", "{{.Names}}").Output()
	if err != nil {
		return false, fmt.Errorf("docker ps -a --filter name=%s: %w", name, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func (CLIDocker) Run(ctx context.Context, spec RunSpec) error {
	args := []string{"run", "-d", "--name", spec.Name}
	if spec.Restart != "" {
		args = append(args, "--restart", spec.Restart)
	}
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	for _, a := range spec.Aliases {
		args = append(args, "--network-alias", a)
	}
	for _, m := range spec.Mounts {
		v := m.Source + ":" + m.Target
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	for _, e := range spec.Env {
		args = append(args, "-e", e.Key+"="+e.Value)
	}
	args = append(args, spec.Image)
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run %s: %w\n%s", spec.Image, err, out)
	}
	return nil
}

// NetworkCreate creates a Docker network, treating "already exists" as success
// so host-agent can seed the ingress network idempotently across restarts.
func (CLIDocker) NetworkCreate(ctx context.Context, name string) error {
	out, err := exec.CommandContext(ctx, "docker", "network", "create", name).CombinedOutput()
	if err != nil && strings.Contains(string(out), "already exists") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("docker network create %s: %w\n%s", name, err, out)
	}
	return nil
}
