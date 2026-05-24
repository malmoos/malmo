// Command host-agent-real is the production host-agent binary. It uses real
// PAM for password verification (POST /v1/auth/verify-password) but keeps
// set-password / set-role / delete-user as an in-memory fake — those three
// operations are not yet wired to the system.
//
// Build requirements:
//   - Linux only
//   - CGO enabled
//   - libpam0g-dev installed (apt install libpam0g-dev)
//   - /etc/pam.d/malmo present (copy dev/pam/malmo)
//   - Must run as root (pam_unix.so requires privilege)
//
// See docs/progress/0011-host-agent-pam-verify.md for the full context and
// known gaps.
package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/malmo/malmo/internal/hostagent"
	"github.com/malmo/malmo/internal/hostagent/pamverifier"
	"github.com/malmo/malmo/internal/protocol"
)

func main() {
	// Loud warning: three operations are still fake even in this real binary.
	// Brain's bootstrap path (POST /setup → SetPassword) does NOT write to
	// /etc/shadow. Tracked as Tier-B follow-up:
	// docs/progress/0011-host-agent-pam-verify.md.
	slog.Warn("host-agent-real: set-password/set-role/delete-user are NOT wired to the system (fake in-memory) — see docs/progress/0011-host-agent-pam-verify.md")

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

	// verifyPassword uses real PAM; all other auth ops stay in-memory.
	a := hostagent.New(&pamverifier.PAMVerifier{Service: "malmo"})

	mux := http.NewServeMux()
	a.Mount(mux)

	slog.Info("host-agent-real listening", "sock", sockPath)
	srv := &http.Server{Handler: hostagent.LogRequests(mux)}
	if err := srv.Serve(ln); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
