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
	// NetworkCreate creates a Docker network, treating "already exists" as
	// success (idempotent). host-agent seeds the shared ingress network the
	// proxy, brain, Caddy and UI all attach to.
	NetworkCreate(ctx context.Context, name string) error
}

// RunSpec is one `docker run -d` invocation — a container host-agent launches.
type RunSpec struct {
	Name    string
	Image   string
	Restart string   // Docker restart policy, e.g. "unless-stopped"
	Network string   // Docker network to attach to (empty → default bridge)
	Aliases []string // extra network aliases (beyond the container name)
	Mounts  []Mount  // bind mounts
	Env     []EnvVar
}

// Mount is a host→container bind mount.
type Mount struct {
	Source   string // host path
	Target   string // container path
	ReadOnly bool   // mount :ro (e.g. the Docker socket into the proxy)
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

	// --- control-plane transport (M1b) ---

	// Network is the shared Docker network the proxy, brain, Caddy and UI all
	// attach to (CONTROL_PLANE.md # Caddy is malmo substrate). host-agent seeds
	// it; the brain references it as external when it reconciles Caddy + UI.
	Network string
	// ProxyImage / ProxyImageTar are the docker-socket-proxy image and its
	// offline bundle (docker-loaded if the image is absent, same as the brain).
	ProxyImage    string
	ProxyImageTar string
	// ProxyContainerName is the proxy container's name (idempotency key).
	ProxyContainerName string
	// ControlPlaneDir is the host directory holding the control-plane compose +
	// caddy.json. Passed to the brain as MALMO_CONTROL_PLANE_DIR and bind-mounted
	// same-path (the daemon resolves compose bind sources as host paths) so the
	// brain's `docker compose up` finds caddy.json where the daemon expects it.
	ControlPlaneDir string
	// UIUpstream is the malmo-ui dial target passed to the brain as
	// MALMO_DASHBOARD_UI_UPSTREAM; its presence is what makes the brain install
	// the dashboard route. Empty leaves the brain without a dashboard route.
	UIUpstream string
	// CaddyImage selects the Caddy image the brain's control-plane `docker compose
	// up` runs, passed through as MALMO_CADDY_IMAGE (the compose substitutes
	// ${MALMO_CADDY_IMAGE:-caddy:2-alpine}; ControlPlaneUp inherits the brain's
	// env). The hosted profile sets it to the caddy-dns/acmedns build, which the
	// "*.<box-id>.malmo.network" wildcard cert needs (ACME DNS-01 — os #207/C3b).
	// Empty leaves compose on stock caddy:2-alpine — the appliance, which does no
	// ACME — so the var is only emitted when set.
	CaddyImage string
	// CatalogDir is the Door-1 catalog the brain installs apps from, passed as
	// MALMO_CATALOG_DIR. It must live under DataDir (the default does) so it is
	// already visible in the brain via the DataDir bind mount — the brain only
	// reads it (manifests, icons), so no separate mount is needed. Empty leaves
	// the brain on its own default (./catalog), which is empty in a container.
	CatalogDir string
	// OfflineInstall sets MALMO_OFFLINE_INSTALL on the brain — trust the
	// catalog-promised digest of a locally-loaded image when its pull fails
	// (APP_LIFECYCLE.md # image digest pinning). Set on a baked, registry-less
	// box (the air-gapped QEMU lane); off on a box with a registry.
	OfflineInstall bool
	// ProfileMarkerPath is the host path of the environment-profile marker
	// (/etc/malmo/profile — ENVIRONMENT.md # How the profile is realized). The
	// brain reads it at startup to resolve appliance vs hosted (the /setup gate,
	// the mDNS-publish skip, …), but it runs in a container that only mounts the
	// agent socket dir + DataDir — neither covers /etc/malmo — so without this it
	// always reads "appliance" (the no-op default) regardless of what the image
	// stamped. host-agent mounts the marker read-only at the same path so the
	// containerized brain resolves the profile exactly as it would natively.
	// Empty (an unmarked appliance box, `make dev`) skips the mount; the brain
	// then resolves appliance, which is correct for an unmarked box.
	ProfileMarkerPath string
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

// proxyDialAlias is the network alias the brain dials the socket-proxy by
// (CONTROL_PLANE.md # Docker socket exposure: "tcp://docker-proxy:2375"). The
// proxy container's own name carries the malmo- prefix; this alias keeps the
// brain's DOCKER_HOST stable regardless of that name.
const proxyDialAlias = "docker-proxy"

// dockerSockPath is the raw Docker socket, bind-mounted read-only into the proxy
// — the one place on the box it is exposed to a container. The brain never gets
// it (CONTROL_PLANE.md # Locked: Docker socket exposure).
const dockerSockPath = "/var/run/docker.sock"

// proxyAllowlist is the docker-socket-proxy env allowlist — the endpoint
// families the brain needs to manage app + control-plane containers, kept in
// sync with dev/control-plane/compose.yml. EXEC and host-bind mounts stay denied
// (the proxy defaults them off); managed-DB provisioning runs the engine's
// client in a one-shot `docker run` container (CONTAINERS/POST), not `docker
// exec`, so it needs no EXEC (DECISIONS.md 2026-06-15 — re-architected off exec).
func proxyAllowlist() []EnvVar {
	return []EnvVar{
		{Key: "POST", Value: "1"},
		{Key: "PING", Value: "1"},
		{Key: "VERSION", Value: "1"},
		{Key: "INFO", Value: "1"},
		{Key: "CONTAINERS", Value: "1"},
		{Key: "IMAGES", Value: "1"},
		{Key: "NETWORKS", Value: "1"},
		{Key: "VOLUMES", Value: "1"},
	}
}

// EnsureTransport seeds the brain's Docker transport before Launch: the shared
// ingress network and the docker-socket-proxy that fronts the raw socket. host-
// agent owns this because the brain cannot bootstrap its own sole Docker path —
// the proxy IS that path (the spike, socket-proxy-compose-validation.md). It is
// idempotent: an existing network or proxy container is left untouched, so it is
// safe across host-agent restarts. A failure is returned for the caller to log;
// host-agent treats it as non-fatal (the brain then comes up unable to reach
// Docker, the same degraded posture as before the proxy existed).
func EnsureTransport(ctx context.Context, d Docker, cfg Config) error {
	if err := d.NetworkCreate(ctx, cfg.Network); err != nil {
		return fmt.Errorf("create ingress network %q: %w", cfg.Network, err)
	}

	present, err := d.ImagePresent(ctx, cfg.ProxyImage)
	if err != nil {
		return fmt.Errorf("check proxy image %q: %w", cfg.ProxyImage, err)
	}
	if !present {
		slog.Info("proxy image absent; loading bundled tarball",
			"image", cfg.ProxyImage, "path", cfg.ProxyImageTar)
		if err := d.Load(ctx, cfg.ProxyImageTar); err != nil {
			return fmt.Errorf("docker load %q: %w", cfg.ProxyImageTar, err)
		}
	}

	exists, err := d.ContainerExists(ctx, cfg.ProxyContainerName)
	if err != nil {
		return fmt.Errorf("check proxy container %q: %w", cfg.ProxyContainerName, err)
	}
	if exists {
		slog.Info("proxy container already present; leaving it to Docker",
			"container", cfg.ProxyContainerName)
		return nil
	}
	if err := d.Run(ctx, proxyRunSpec(cfg)); err != nil {
		return fmt.Errorf("run proxy container: %w", err)
	}
	slog.Info("socket-proxy launched",
		"container", cfg.ProxyContainerName, "image", cfg.ProxyImage)
	return nil
}

// proxyRunSpec assembles the docker-socket-proxy `docker run`. The raw socket is
// mounted read-only; the brain reaches the proxy by the docker-proxy alias.
func proxyRunSpec(cfg Config) RunSpec {
	return RunSpec{
		Name:    cfg.ProxyContainerName,
		Image:   cfg.ProxyImage,
		Restart: "unless-stopped",
		Network: cfg.Network,
		Aliases: []string{proxyDialAlias},
		Mounts:  []Mount{{Source: dockerSockPath, Target: dockerSockPath, ReadOnly: true}},
		Env:     proxyAllowlist(),
	}
}

// runSpec assembles the brain's `docker run` invocation from cfg. The brain
// mounts the host-agent socket directory (to reach host-agent) and the data
// root (SQLite state lives under it), reads its config from MALMO_* env, and
// runs with restart=unless-stopped so Docker supervises it after launch. It is
// deliberately not given the Docker socket — the brain reaches Docker only
// through the host-agent-seeded socket-proxy at tcp://docker-proxy:2375
// (CONTROL_PLANE.md # Docker socket exposure). It joins the ingress network so
// it can reach the proxy + Caddy admin by name, and carries the env that points
// it at the proxy, Caddy, and the staged control-plane compose.
func runSpec(cfg Config) RunSpec {
	sockDir := filepath.Dir(cfg.SocketPath)
	mounts := []Mount{
		{Source: sockDir, Target: sockDir},
		{Source: cfg.DataDir, Target: cfg.DataDir},
	}
	// Mount the environment-profile marker read-only at the same path so the
	// containerized brain reads it exactly as it would natively (see
	// Config.ProfileMarkerPath). Skipped when empty — an unmarked box resolves
	// appliance, the no-op default.
	if cfg.ProfileMarkerPath != "" {
		mounts = append(mounts, Mount{Source: cfg.ProfileMarkerPath, Target: cfg.ProfileMarkerPath, ReadOnly: true})
	}
	env := []EnvVar{
		{Key: "MALMO_STATE_DIR", Value: cfg.StateDir},
		{Key: "MALMO_AGENT_SOCK", Value: cfg.SocketPath},
		{Key: "DOCKER_HOST", Value: "tcp://" + proxyDialAlias + ":2375"},
		{Key: "MALMO_CADDY_ADMIN", Value: "http://malmo-caddy:2019"},
		// The app-unresponsive probe dials Caddy by service name, not the
		// container's own loopback (the natively-run dev default).
		{Key: "MALMO_CADDY_PROBE_URL", Value: "http://malmo-caddy"},
		{Key: "MALMO_CONTROL_PLANE_DIR", Value: cfg.ControlPlaneDir},
		{Key: "MALMO_DASHBOARD_UI_UPSTREAM", Value: cfg.UIUpstream},
	}
	// The Door-1 catalog (under DataDir, already mounted — see Config.CatalogDir).
	// Set only when configured so an unset CatalogDir leaves the brain on its own
	// default rather than pointing it at an empty "".
	if cfg.CatalogDir != "" {
		env = append(env, EnvVar{Key: "MALMO_CATALOG_DIR", Value: cfg.CatalogDir})
	}
	// The hosted Caddy image (caddy-dns/acmedns build). Emit only when set so an
	// unset CaddyImage leaves the control-plane compose on its stock-caddy default
	// (the appliance does no ACME — Config.CaddyImage).
	if cfg.CaddyImage != "" {
		env = append(env, EnvVar{Key: "MALMO_CADDY_IMAGE", Value: cfg.CaddyImage})
	}
	// Air-gapped install mode (registry-less box). Emit only when set — the brain
	// defaults it off (a box with a registry pulls + verifies as usual).
	if cfg.OfflineInstall {
		env = append(env, EnvVar{Key: "MALMO_OFFLINE_INSTALL", Value: "true"})
	}
	return RunSpec{
		Name:    cfg.ContainerName,
		Image:   cfg.Image,
		Restart: "unless-stopped",
		Network: cfg.Network,
		Mounts:  mounts,
		Env:     env,
	}
}
