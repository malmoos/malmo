// Command host-agent-real is the production host-agent binary. It uses real
// PAM for password verification (POST /v1/auth/verify-password), the Avahi
// DBus API for publish/unpublish (POST /v1/discovery/publish|unpublish),
// useradd+chpasswd for set-password (POST /v1/auth/set-password), gpasswd
// for set-role (POST /v1/auth/set-role), userdel -r -f for delete-user
// (POST /v1/auth/delete-user), and serves GET /v1/health/system from
// /run/molma/health/storage.json (storage category) plus `systemctl is-active`
// over the core-unit allowlist (services category, service-down). All host ops
// now hit the real system.
//
// Build requirements:
//   - Linux only
//   - CGO enabled (for PAM)
//   - libpam0g-dev installed (apt install libpam0g-dev)
//   - /etc/pam.d/molma present (copy dev/pam/molma)
//   - avahi-daemon running with the system DBus accessible
//   - Must run as root (pam_unix.so requires privilege)
//
// See docs/progress/0011-host-agent-pam-verify.md and
// docs/progress/0013-avahi-dbus-publisher.md for full context and known gaps.
package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/molmaos/molma/internal/hostagent"
	"github.com/molmaos/molma/internal/hostagent/avahipublisher"
	"github.com/molmaos/molma/internal/hostagent/clockhealth"
	"github.com/molmaos/molma/internal/hostagent/healthsource"
	"github.com/molmaos/molma/internal/hostagent/journalsource"
	"github.com/molmaos/molma/internal/hostagent/pamverifier"
	"github.com/molmaos/molma/internal/hostagent/rampressure"
	"github.com/molmaos/molma/internal/hostagent/servicehealth"
	"github.com/molmaos/molma/internal/hostagent/usermgr"
	"github.com/molmaos/molma/internal/protocol"
)

func main() {
	pub := &avahipublisher.DBusPublisher{HostSuffix: protocol.AppHostSuffix}

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
	// 0660 root:molma — brain's container UID is in the molma group.
	_ = os.Chmod(sockPath, 0o660)

	// verifyPassword uses real PAM; Avahi A records are published via DBus;
	// set-password writes to /etc/shadow via useradd+chpasswd; set-role
	// flips sudo group membership via gpasswd; delete-user shells out to
	// userdel -r -f (see docs/progress/0017-host-agent-delete-user.md).
	a := hostagent.New(
		&pamverifier.PAMVerifier{Service: "molma"},
		pub,
	)
	a.UserMgr = &usermgr.LinuxUserManager{}
	a.Health = healthsource.New(healthsource.DefaultPath)
	a.Services = servicehealth.New()
	a.Time = clockhealth.New()
	a.Logs = journalsource.New()
	a.Resources = rampressure.New()

	mux := http.NewServeMux()
	a.Mount(mux)

	slog.Info("host-agent-real listening", "sock", sockPath)
	srv := &http.Server{Handler: hostagent.LogRequests(mux)}
	if err := srv.Serve(ln); err != nil {
		slog.Error("serve", "err", err)
		_ = pub.Close()
		os.Exit(1)
	}
	_ = pub.Close()
}
