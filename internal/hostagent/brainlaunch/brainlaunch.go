// Package brainlaunch implements host-agent's first-boot brain bootstrap: the
// short sequence that brings the malmo-brain container up during host-agent's
// own startup, after Docker is ready (CONTROL_PLANE.md # Locked: host-agent
// launches the brain container; BUILD.md # First-boot brain bootstrap).
//
// The sequence is deliberately small — host-agent stays minimal (CONTROL_PLANE.md
// # Locked: implementation specifics):
//
//  1. If Docker doesn't already have the brain image, docker-load the bundled
//     tarball (offline-first; the box boots with zero internet).
//  2. Lockstep check: read the brain image's declared protocol major from its
//     OCI label and refuse to launch a brain whose major this host-agent does
//     not speak — a botched partial update is caught here, with a clear error,
//     instead of as first-request failures.
//  3. Run the brain container with restart=unless-stopped. After that, Docker
//     keeps the brain alive across host-agent restarts; host-agent does not
//     supervise it in steady state — so a brain container that already exists
//     is left untouched.
//
// The registry-pull step (BUILD.md # First-boot brain bootstrap step 3) is
// deferred with the rest of the real update path (epic #161); only the bundled
// image is used today.
package brainlaunch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"

	"github.com/malmoos/malmo/internal/protocol"
)

// ErrProtocolMismatch is returned (wrapped) when the brain image's declared
// wire-protocol major does not match the major this host-agent speaks. Callers
// discriminate it with errors.Is to distinguish a refused launch (a real
// version-skew condition) from an operational failure (Docker unreachable, a
// missing tarball).
var ErrProtocolMismatch = errors.New("brain protocol version mismatch")

// Docker is the narrow slice of the `docker` CLI the brain bootstrap needs.
// Consumer-side interface (CLAUDE.md # Go code discipline): the production impl
// shells out to docker (see CLIDocker); tests pass a recording fake. Only this
// package consumes it, so it lives here, not in a provider package.
type Docker interface {
	// ImagePresent reports whether Docker already has the named image locally.
	ImagePresent(ctx context.Context, ref string) (bool, error)
	// Load docker-loads an image tarball (`docker load -i <path>`).
	Load(ctx context.Context, tarPath string) error
	// ImageLabel returns the value of one OCI label on an image, or "" if the
	// image carries no such label.
	ImageLabel(ctx context.Context, ref, label string) (string, error)
	// ContainerExists reports whether a container with the given name exists, in
	// any state (running, exited, created).
	ContainerExists(ctx context.Context, name string) (bool, error)
	// Run starts a detached container (`docker run -d …`).
	Run(ctx context.Context, spec RunSpec) error
}

// RunSpec is one `docker run -d` invocation — the container the brain runs in.
type RunSpec struct {
	Name    string
	Image   string
	Restart string  // Docker restart policy, e.g. "unless-stopped"
	Mounts  []Mount // bind mounts
	Env     []EnvVar
}

// Mount is a host→container bind mount.
type Mount struct {
	Source string // host path
	Target string // container path
}

// EnvVar is one `-e KEY=VALUE` for the container.
type EnvVar struct {
	Key   string
	Value string
}

// Config is the brain bootstrap's configuration. host-agent builds it from its
// environment (production defaults, overridable for the test lane).
type Config struct {
	// Image is the brain image reference to launch (tag or digest).
	Image string
	// ImageTar is the bundled tarball docker-loaded when Image is absent.
	ImageTar string
	// ContainerName is the brain container's name (the idempotency key).
	ContainerName string
	// DataDir is the host data root bind-mounted into the brain (state lives
	// under it). StateDir is the brain's state directory (under DataDir).
	DataDir  string
	StateDir string
	// SocketPath is the host-agent socket the brain dials. Its directory is
	// bind-mounted into the brain so the socket node is visible at the same path.
	SocketPath string
}

// Launch runs the first-boot brain bootstrap. It is idempotent: a brain
// container that already exists is left to Docker (host-agent does not supervise
// it), so re-running across host-agent restarts is a no-op. A failure — load
// error, Docker unreachable, or a refused protocol mismatch — is returned for
// the caller to log; host-agent treats it as non-fatal and keeps serving so the
// box stays diagnosable.
func Launch(ctx context.Context, d Docker, cfg Config) error {
	// 1. Ensure the image is present, docker-loading the bundled tarball if not.
	present, err := d.ImagePresent(ctx, cfg.Image)
	if err != nil {
		return fmt.Errorf("check brain image %q: %w", cfg.Image, err)
	}
	if !present {
		slog.Info("brain image absent; loading bundled tarball",
			"image", cfg.Image, "path", cfg.ImageTar)
		if err := d.Load(ctx, cfg.ImageTar); err != nil {
			return fmt.Errorf("docker load %q: %w", cfg.ImageTar, err)
		}
	}

	// 2. Lockstep major-version check before launch.
	want := strconv.Itoa(protocol.Major)
	got, err := d.ImageLabel(ctx, cfg.Image, protocol.ImageProtocolMajorLabel)
	if err != nil {
		return fmt.Errorf("read brain protocol label: %w", err)
	}
	if got != want {
		return fmt.Errorf("%w: brain image %q declares protocol major %q, host-agent speaks %q",
			ErrProtocolMismatch, cfg.Image, got, want)
	}

	// 3. Start the brain container if it isn't already there.
	exists, err := d.ContainerExists(ctx, cfg.ContainerName)
	if err != nil {
		return fmt.Errorf("check brain container %q: %w", cfg.ContainerName, err)
	}
	if exists {
		slog.Info("brain container already present; leaving it to Docker",
			"container", cfg.ContainerName)
		return nil
	}

	if err := d.Run(ctx, runSpec(cfg)); err != nil {
		return fmt.Errorf("run brain container: %w", err)
	}
	slog.Info("brain container launched",
		"container", cfg.ContainerName, "image", cfg.Image)
	return nil
}

// runSpec assembles the brain's `docker run` invocation from cfg. The brain
// mounts the host-agent socket directory (to reach host-agent) and the data
// root (SQLite state lives under it), reads its config from MALMO_* env, and
// runs with restart=unless-stopped so Docker supervises it after launch. It is
// deliberately not given the Docker socket — the brain reaches Docker only
// through the socket-proxy the control-plane stack brings up later
// (CONTROL_PLANE.md # Docker socket exposure), so until then it runs degraded.
func runSpec(cfg Config) RunSpec {
	sockDir := filepath.Dir(cfg.SocketPath)
	return RunSpec{
		Name:    cfg.ContainerName,
		Image:   cfg.Image,
		Restart: "unless-stopped",
		Mounts: []Mount{
			{Source: sockDir, Target: sockDir},
			{Source: cfg.DataDir, Target: cfg.DataDir},
		},
		Env: []EnvVar{
			{Key: "MALMO_STATE_DIR", Value: cfg.StateDir},
			{Key: "MALMO_AGENT_SOCK", Value: cfg.SocketPath},
		},
	}
}
