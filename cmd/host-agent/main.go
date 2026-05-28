// Command host-agent is the FAKE host-agent: it speaks the real
// BRAIN_HOST_PROTOCOL.md wire format over a real UNIX socket, but its host
// operations are canned (no Avahi, no LUKS, no apt, no PAM). This is the
// binary used in the inner dev loop (make dev / make run-agent).
// See docs/dev/running-locally.md for the real binary (cmd/host-agent-real).
//
// Env vars:
//
//	MALMO_AGENT_SOCK  — UNIX socket path (default protocol.SocketPath)
//	MALMO_HEALTH_PATH — when set, serve GET /v1/health/storage from this
//	                    file (read via the same FilesystemHealthSource the
//	                    real binary uses). When unset, the endpoint returns
//	                    an empty findings list ("storage looks healthy").
package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/malmo/malmo/internal/hostagent"
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

	a := hostagent.New(nil, hostagent.NewFakePublisher(".malmo.local")) // verifier wired after construction
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
