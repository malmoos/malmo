// Command host-agent is the FAKE host-agent: it speaks the real
// BRAIN_HOST_PROTOCOL.md wire format over a real UNIX socket, but its host
// operations are canned (no LUKS, no apt, no PAM). This is the binary used in
// the inner dev loop (make dev / make run-agent).
// See docs/dev/running-locally.md for the real binary (cmd/host-agent-real).
//
// Env vars:
//
//	MALMO_AGENT_SOCK  — UNIX socket path (default protocol.SocketPath)
//	MALMO_STATE_DIR   — when set, persist the fake user maps (passwords +
//	                    roles) to <dir>/fake-shadow.json so accounts survive a
//	                    restart, standing in for /etc/shadow. When unset, the
//	                    maps are in-memory only and a restart forgets every
//	                    account. The dev stack exports this (same dir as the
//	                    brain's malmo.db).
//	MALMO_HEALTH_PATH — when set, back the storage category of GET
//	                    /v1/health/system from this file (read via the same
//	                    FilesystemHealthSource the real binary uses). When
//	                    unset, the storage category is an empty findings list
//	                    ("storage looks healthy").
//	MALMO_DEV_AVAHI   — when "1", publish per-app .local names via the real
//	                    Avahi DBus publisher instead of the in-memory fake, so
//	                    <slug>.local resolves on the LAN (and from other
//	                    devices) in dev. Requires avahi-daemon running; runs
//	                    unprivileged. `make dev` sets this. All other host ops
//	                    stay fake — this only swaps the discovery publisher.
//	MALMO_FAKE_NO_GPU — when "1", GET /v1/system/gpu reports no usable GPU
//	                    instead of the default synthetic Intel iGPU, so the
//	                    `gpu: true` install refusal is exercisable in dev.
package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/malmoos/malmo/internal/hostagent"
	"github.com/malmoos/malmo/internal/hostagent/avahipublisher"
	"github.com/malmoos/malmo/internal/hostagent/healthsource"
	"github.com/malmoos/malmo/internal/hostagent/netstate"
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
	_ = os.Chmod(sockPath, 0o660)

	// The discovery publisher is the only host op that can be made real
	// unprivileged (Avahi's default DBus policy allows any user to publish),
	// so dev can opt into real .local announcements while every other host op
	// stays fake. Default is the in-memory fake, keeping make run-agent and
	// the hermetic test-health.sh free of any avahi-daemon dependency.
	var pub hostagent.Publisher = hostagent.NewFakePublisher(protocol.AppHostSuffix)
	if os.Getenv("MALMO_DEV_AVAHI") == "1" {
		pub = &avahipublisher.DBusPublisher{HostSuffix: protocol.AppHostSuffix}
		slog.Info("host-agent (fake) using real Avahi DBus publisher for .local names")
	}

	a := hostagent.New(nil, pub) // verifier wired after construction
	a.Verifier = hostagent.NewFakeVerifier(a)
	// The fake agent stands in for /etc/shadow, which the real binary persists
	// for free. Without a backing file, a `make dev` restart wipes every
	// account's password while the brain's SQLite keeps the user + session
	// rows, so a fresh login after clearing cookies fails. Persist into the
	// same MALMO_STATE_DIR the brain uses (the dev stack exports it).
	if dir := os.Getenv("MALMO_STATE_DIR"); dir != "" {
		statePath := filepath.Join(dir, "fake-shadow.json")
		if err := a.EnablePersistence(statePath); err != nil {
			slog.Error("host-agent (fake) load persisted user state", "path", statePath, "err", err)
			os.Exit(1)
		}
		slog.Info("host-agent (fake) persisting user state", "path", statePath)
	}
	if healthPath := os.Getenv("MALMO_HEALTH_PATH"); healthPath != "" {
		a.Health = healthsource.New(healthPath)
		slog.Info("host-agent (fake) wired to storage health file", "path", healthPath)
	}
	// No journald in the dev loop, so stream real Docker container output via
	// `docker logs -f --timestamps`. The compose replica suffix (-1) is probed
	// automatically; falls back to the bare stem for standalone containers.
	a.Logs = &dockerLogSource{}
	// On Linux, wire real /proc counters so make dev shows live CPU and RAM.
	// On other platforms newSystemSampler returns nil and the agent falls back
	// to synthetic monotonic counters (agent.go:447).
	a.System = newSystemSampler()
	// No real data drive in the dev loop, so GET /v1/system/status reports a
	// canned free/total (≈412 GiB free of a 1 TiB drive) — enough for the
	// install plan's free_bytes to render a plausible figure natively.
	a.Disk = hostagent.NewFakeDiskReporter(412<<30, 1<<40)
	// No real drives either, so the Storage bars report two canned volumes
	// (System ≈18 GiB free of 64 GiB, Data ≈412 GiB free of 1 TiB) — the panel
	// shows both bars in dev without a second physical drive. Data matches the
	// FakeDiskReporter figure above so the two status fields stay coherent.
	a.DiskSpace = hostagent.NewFakeDiskSpaceReporter(
		protocol.DiskSpace{Label: "System", FreeBytes: 18 << 30, TotalBytes: 64 << 30},
		protocol.DiskSpace{Label: "Data", FreeBytes: 412 << 30, TotalBytes: 1 << 40},
	)
	// No real /dev/dri in the dev loop, so GET /v1/system/gpu reports a
	// synthetic Intel iGPU (render GID 104, Debian's usual `render` group) so
	// a `gpu: true` install exercises the full override path natively.
	// MALMO_FAKE_NO_GPU=1 flips it to "no usable GPU" for the refusal path.
	gpu := protocol.SystemGPU{Present: true, Vendor: "intel", RenderGID: 104}
	if os.Getenv("MALMO_FAKE_NO_GPU") == "1" {
		gpu = protocol.SystemGPU{}
		slog.Info("host-agent (fake) reporting no GPU (MALMO_FAKE_NO_GPU=1)")
	}
	a.GPU = hostagent.NewFakeGPUReporter(gpu)
	// No NetworkManager in the dev loop either: a fixed plausible LAN set
	// keeps GET /v1/discovery/state's interfaces field stable regardless of
	// the dev box's real network.
	a.Net = hostagent.NewFakeNetState(netstate.LANInterface{Name: "eth0", Index: 2, IPv4: "192.168.1.20"})
	// Back the /v1/files/* family (the in-dashboard file manager) with in-process
	// ops as the dev operator — no UID drop, since the dev brain and this agent
	// are the same unprivileged operator (mirroring resolve-home). "home" is the
	// operator's own home; "shared" is a dev stand-in for /srv/malmo/shared, under
	// MALMO_STATE_DIR when set (so make clean wipes it) else ~/.malmo-shared.
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Error("host-agent (fake) resolve home dir", "err", err)
		os.Exit(1)
	}
	sharedBase := filepath.Join(home, ".malmo-shared")
	if dir := os.Getenv("MALMO_STATE_DIR"); dir != "" {
		sharedBase = filepath.Join(dir, "shared")
	}
	a.Files = hostagent.NewFakeFileManager(home, sharedBase)

	mux := http.NewServeMux()
	a.Mount(mux)

	slog.Info("host-agent (fake) listening", "sock", sockPath)
	srv := &http.Server{Handler: hostagent.LogRequests(mux)}
	if err := srv.Serve(ln); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
