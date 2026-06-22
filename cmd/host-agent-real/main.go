// Command host-agent-real is the production host-agent binary. The shared code
// here binds and serves the UNIX socket, mounts the hostagent HTTP handlers,
// and launches the brain container on first boot (CONTROL_PLANE.md # Locked:
// host-agent launches the brain container). Which host-op seams get wired onto
// the agent is build-profile-specific and lives in two build-tagged files:
//
//   - wiring_appliance.go (default, //go:build !hosted) — the full appliance
//     integration: real PAM, user management, the health/system reporters, and
//     the LAN discovery stack (NetworkManager via netstate + per-interface
//     Avahi announcements via avahipublisher, kept aligned by a network watcher).
//   - wiring_hosted.go (//go:build hosted) — the slim hosted-cloud variant
//     (ENVIRONMENT.md # How the profile is realized): the same kept seams with
//     the LAN/discovery stack compiled out (no NetworkManager, no Avahi, no
//     watcher). Built with `go build -tags hosted ./cmd/host-agent-real` for the
//     cloud image (#203/C2).
//
// Both variants wire real PAM (POST /v1/auth/verify-password) and real user
// management, so both builds are Linux + CGO and need libpam0g-dev +
// /etc/pam.d/malmo and must run as root (pam_unix.so requires privilege). The
// appliance build additionally needs avahi-daemon running with the system DBus
// accessible; the hosted build does not (it publishes nothing).
//
// See docs/progress/host-agent-pam-verify.md, docs/progress/avahi-dbus-publisher.md,
// and docs/progress/slim-cloud-host-agent.md for full context and known gaps.
package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/malmoos/malmo/internal/hostagent"
	"github.com/malmoos/malmo/internal/hostagent/brainlaunch"
	"github.com/malmoos/malmo/internal/profile"
	"github.com/malmoos/malmo/internal/protocol"
)

func main() {
	sockPath := os.Getenv("MALMO_AGENT_SOCK")
	if sockPath == "" {
		sockPath = protocol.SocketPath
	}

	if err := os.RemoveAll(sockPath); err != nil {
		slog.Error("remove stale socket", "sock", sockPath, "err", err)
		os.Exit(1)
	}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		slog.Error("listen", "sock", sockPath, "err", err)
		os.Exit(1)
	}
	defer ln.Close()
	// 0660 root:malmo — brain's container UID is in the malmo group.
	_ = os.Chmod(sockPath, 0o660)

	// buildAgent wires the host-op seams for this build profile and returns a
	// cleanup to run on shutdown (appliance: stop the network watcher + close the
	// Avahi/NM DBus connections; hosted: nothing). It is defined per build tag in
	// wiring_appliance.go (!hosted) / wiring_hosted.go (hosted).
	a, cleanup := buildAgent()
	defer cleanup()

	mux := http.NewServeMux()
	a.Mount(mux)

	// First-boot brain bootstrap (CONTROL_PLANE.md # Locked: host-agent launches
	// the brain container; BUILD.md # First-boot brain bootstrap). Docker is
	// ready by here — host-agent.service is ordered After=docker.service. The
	// socket is already bound above, so the brain can reach it on first call.
	// Best-effort: a failure (including a refused protocol-major mismatch) leaves
	// host-agent serving its socket so the box stays diagnosable; it does not
	// tear host-agent down.
	brainCtx, brainCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	brainCfg := brainLaunchConfig(sockPath)
	// Seed the brain's Docker transport (ingress network + socket-proxy) before
	// launching the brain — the brain reaches Docker only through this proxy
	// (CONTROL_PLANE.md # Docker socket exposure), and the brain then reconciles
	// Caddy + malmo-ui through it. Best-effort, like the launch itself: a failure
	// leaves the brain degraded (no Docker reach) but host-agent keeps serving so
	// the box stays diagnosable.
	if err := brainlaunch.EnsureTransport(brainCtx, brainlaunch.NewCLIDocker(), brainCfg); err != nil {
		slog.Error("seed brain transport failed; host-agent continues serving", "err", err)
	}
	if err := brainlaunch.Launch(brainCtx, brainlaunch.NewCLIDocker(), brainCfg); err != nil {
		slog.Error("brain launch failed; host-agent continues serving", "err", err)
	}
	brainCancel()

	slog.Info("host-agent-real listening", "sock", sockPath)
	srv := &http.Server{Handler: hostagent.LogRequests(mux)}
	if err := srv.Serve(ln); err != nil {
		slog.Error("serve", "err", err)
		// os.Exit skips the deferred cleanup; run it by hand first.
		cleanup()
		os.Exit(1)
	}
}

// brainLaunchConfig builds the brain bootstrap config from the environment.
// Defaults are the production paths (BUILD.md # First-boot brain bootstrap); the
// image ref and bundled-tarball path are overridable so the QEMU test lane can
// point at its dev tag and baked bundle. The data root is fixed at /var/lib/malmo
// (STORAGE.md), with the brain's SQLite state under it; the brain dials the same
// agent socket host-agent just bound.
func brainLaunchConfig(sockPath string) brainlaunch.Config {
	const dataDir = "/var/lib/malmo"
	// The control-plane compose + caddy.json are staged under dataDir so the
	// brain's `docker compose up` bind-mounts caddy.json at a path the Docker
	// daemon resolves identically on host and in the brain container (the
	// same-path constraint — socket-proxy-compose-validation.md). The proxy image
	// + bundle default to the names baked by dev/test-qemu / the ISO build.
	controlPlaneDir := env("MALMO_CONTROL_PLANE_DIR", filepath.Join(dataDir, "control-plane"))
	// The brain reads the environment-profile marker from inside its container,
	// which mounts neither /etc/malmo nor anything covering it — so host-agent
	// must hand it across. Resolve the host marker path (the brain's own default,
	// overridable for tests) and mount it only when it exists as a regular file:
	// an unmarked appliance box has no marker, and a same-path bind of a missing
	// source would make Docker auto-create a root-owned directory there. The brain
	// reads its default /etc/malmo/profile inside the container, so the mount is
	// same-path; see brainlaunch.Config.ProfileMarkerPath.
	profileMarker := env("MALMO_PROFILE_PATH", profile.DefaultMarkerPath)
	if fi, err := os.Stat(profileMarker); err != nil || !fi.Mode().IsRegular() {
		profileMarker = ""
	}
	return brainlaunch.Config{
		Image:         env("MALMO_BRAIN_IMAGE", "malmo-brain:latest"),
		ImageTar:      env("MALMO_BRAIN_IMAGE_TAR", filepath.Join(dataDir, "brain-image.tar")),
		ContainerName: "malmo-brain",
		DataDir:       dataDir,
		StateDir:      filepath.Join(dataDir, "state"),
		SocketPath:    sockPath,

		Network:            env("MALMO_INGRESS_NETWORK", "malmo-ingress"),
		ProxyImage:         env("MALMO_PROXY_IMAGE", "tecnativa/docker-socket-proxy:v0.4.2"),
		ProxyImageTar:      env("MALMO_PROXY_IMAGE_TAR", filepath.Join(controlPlaneDir, "images", "docker-socket-proxy.tar")),
		ProxyContainerName: "malmo-docker-proxy",
		ControlPlaneDir:    controlPlaneDir,
		UIUpstream:         env("MALMO_DASHBOARD_UI_UPSTREAM", "malmo-ui:80"),
		// Empty by default: the control-plane compose then runs stock caddy:2-alpine
		// (the appliance, no ACME). The hosted image sets MALMO_CADDY_IMAGE to the
		// caddy-dns/acmedns build for the wildcard cert (os #207/C3b).
		CaddyImage: env("MALMO_CADDY_IMAGE", ""),
		// The Door-1 catalog the brain installs from, staged under dataDir so it
		// rides the brain's DataDir mount (brainlaunch.Config.CatalogDir).
		CatalogDir:        env("MALMO_CATALOG_DIR", filepath.Join(dataDir, "catalog")),
		OfflineInstall:    envBool("MALMO_OFFLINE_INSTALL"),
		ProfileMarkerPath: profileMarker,
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envBool reports whether key is set to a truthy value (strconv.ParseBool:
// 1/t/true/…). Unset or unparseable → false (the safe default — a box with a
// registry). Deliberately no `def` parameter and no warn-on-unparseable (unlike
// cmd/brain's envBool): every host-agent caller wants false-on-anything-odd, and
// host-agent startup must never block or get noisy over a malformed *optional*
// env. The two are not shared for the same reason `env` isn't — small per-binary
// helpers, no internal package for two cmd/ consumers (CLAUDE.md # no premature
// abstraction).
func envBool(key string) bool {
	b, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && b
}
