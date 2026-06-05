// Command host-agent is the FAKE host-agent: it speaks the real
// BRAIN_HOST_PROTOCOL.md wire format over a real UNIX socket, but its host
// operations are canned (no LUKS, no apt, no PAM). This is the binary used in
// the inner dev loop (make dev / make run-agent).
// See docs/dev/running-locally.md for the real binary (cmd/host-agent-real).
//
// Env vars:
//
//	MOLMA_AGENT_SOCK  — UNIX socket path (default protocol.SocketPath)
//	MOLMA_STATE_DIR   — when set, persist the fake user maps (passwords +
//	                    roles) to <dir>/fake-shadow.json so accounts survive a
//	                    restart, standing in for /etc/shadow. When unset, the
//	                    maps are in-memory only and a restart forgets every
//	                    account. The dev stack exports this (same dir as the
//	                    brain's molma.db).
//	MOLMA_HEALTH_PATH — when set, back the storage category of GET
//	                    /v1/health/system from this file (read via the same
//	                    FilesystemHealthSource the real binary uses). When
//	                    unset, the storage category is an empty findings list
//	                    ("storage looks healthy").
//	MOLMA_DEV_AVAHI   — when "1", publish per-app .local names via the real
//	                    Avahi DBus publisher instead of the in-memory fake, so
//	                    <slug>.local resolves on the LAN (and from other
//	                    devices) in dev. Requires avahi-daemon running; runs
//	                    unprivileged. `make dev` sets this. All other host ops
//	                    stay fake — this only swaps the discovery publisher.
package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/molmaos/molma/internal/hostagent"
	"github.com/molmaos/molma/internal/hostagent/avahipublisher"
	"github.com/molmaos/molma/internal/hostagent/healthsource"
	"github.com/molmaos/molma/internal/protocol"
)

func main() {
	sockPath := os.Getenv("MOLMA_AGENT_SOCK")
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
	if os.Getenv("MOLMA_DEV_AVAHI") == "1" {
		pub = &avahipublisher.DBusPublisher{HostSuffix: protocol.AppHostSuffix}
		slog.Info("host-agent (fake) using real Avahi DBus publisher for .local names")
	}

	a := hostagent.New(nil, pub) // verifier wired after construction
	a.Verifier = hostagent.NewFakeVerifier(a)
	// The fake agent stands in for /etc/shadow, which the real binary persists
	// for free. Without a backing file, a `make dev` restart wipes every
	// account's password while the brain's SQLite keeps the user + session
	// rows, so a fresh login after clearing cookies fails. Persist into the
	// same MOLMA_STATE_DIR the brain uses (the dev stack exports it).
	if dir := os.Getenv("MOLMA_STATE_DIR"); dir != "" {
		statePath := filepath.Join(dir, "fake-shadow.json")
		if err := a.EnablePersistence(statePath); err != nil {
			slog.Error("host-agent (fake) load persisted user state", "path", statePath, "err", err)
			os.Exit(1)
		}
		slog.Info("host-agent (fake) persisting user state", "path", statePath)
	}
	if healthPath := os.Getenv("MOLMA_HEALTH_PATH"); healthPath != "" {
		a.Health = healthsource.New(healthPath)
		slog.Info("host-agent (fake) wired to storage health file", "path", healthPath)
	}
	// No real journald or Docker journald log driver in the dev loop, so the
	// per-app Logs tab is fed a synthetic ticker — one plausible line per second
	// per followed container — so the brain→UI streaming path is exercisable
	// end-to-end natively (BRAIN_HOST_PROTOCOL.md # Pattern C).
	a.Logs = hostagent.NewFakeLogSource(time.Second)

	mux := http.NewServeMux()
	a.Mount(mux)

	slog.Info("host-agent (fake) listening", "sock", sockPath)
	srv := &http.Server{Handler: hostagent.LogRequests(mux)}
	if err := srv.Serve(ln); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
