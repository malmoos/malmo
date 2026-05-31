// Command host-agent is the FAKE host-agent: it speaks the real
// BRAIN_HOST_PROTOCOL.md wire format over a real UNIX socket, but its host
// operations are canned (no LUKS, no apt, no PAM). This is the binary used in
// the inner dev loop (make dev / make run-agent).
// See docs/dev/running-locally.md for the real binary (cmd/host-agent-real).
//
// Env vars:
//
//	MALMO_AGENT_SOCK  — UNIX socket path (default protocol.SocketPath)
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
package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/malmo/malmo/internal/hostagent"
	"github.com/malmo/malmo/internal/hostagent/avahipublisher"
	"github.com/malmo/malmo/internal/hostagent/healthsource"
	"github.com/malmo/malmo/internal/protocol"
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
	if healthPath := os.Getenv("MALMO_HEALTH_PATH"); healthPath != "" {
		a.Health = healthsource.New(healthPath)
		slog.Info("host-agent (fake) wired to storage health file", "path", healthPath)
	}

	mux := http.NewServeMux()
	a.Mount(mux)

	slog.Info("host-agent (fake) listening", "sock", sockPath)
	srv := &http.Server{Handler: hostagent.LogRequests(mux)}
	if err := srv.Serve(ln); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
