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
// Discovery announces per LAN interface: NetworkManager (via DBus) supplies
// the set of active ethernet/WiFi interfaces, each app name is published on
// every LAN interface with that interface's own address, and a watcher replays
// the announcements (and keeps avahi-daemon.conf's allow-interfaces current)
// when the network changes. See DISCOVERY.md # Interface scoping.
//
// Build requirements:
//   - Linux only
//   - CGO enabled (for PAM)
//   - libpam0g-dev installed (apt install libpam0g-dev)
//   - /etc/pam.d/molma present (copy dev/pam/molma)
//   - avahi-daemon running with the system DBus accessible
//   - Must run as root (pam_unix.so requires privilege)
//
// See docs/progress/host-agent-pam-verify.md and
// docs/progress/avahi-dbus-publisher.md for full context and known gaps.
package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/molmaos/molma/internal/hostagent"
	"github.com/molmaos/molma/internal/hostagent/avahipublisher"
	"github.com/molmaos/molma/internal/hostagent/clockhealth"
	"github.com/molmaos/molma/internal/hostagent/diskusage"
	"github.com/molmaos/molma/internal/hostagent/healthsource"
	"github.com/molmaos/molma/internal/hostagent/journalsource"
	"github.com/molmaos/molma/internal/hostagent/netstate"
	"github.com/molmaos/molma/internal/hostagent/pamverifier"
	"github.com/molmaos/molma/internal/hostagent/procsource"
	"github.com/molmaos/molma/internal/hostagent/rampressure"
	"github.com/molmaos/molma/internal/hostagent/rebootrequired"
	"github.com/molmaos/molma/internal/hostagent/servicehealth"
	"github.com/molmaos/molma/internal/hostagent/usermgr"
	"github.com/molmaos/molma/internal/protocol"
)

func main() {
	// NetworkManager is the source of truth for the LAN set (#130): the
	// publisher announces every app name per LAN interface with that
	// interface's address, and the sync keeps avahi-daemon.conf's
	// allow-interfaces plus the committed entry groups aligned across IP
	// changes and interface add/remove.
	prov := &netstate.NMProvider{}
	pub := &avahipublisher.DBusPublisher{HostSuffix: protocol.AppHostSuffix, LAN: prov.LANInterfaces}
	avahiSync := &avahipublisher.Sync{Publisher: pub, LAN: prov.LANInterfaces}

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
	// userdel -r -f (see docs/progress/host-agent-delete-user.md).
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
	a.Disk = diskusage.New()
	a.Reboot = rebootrequired.New()
	a.System = procsource.New()
	a.Net = prov

	// Align avahi-daemon with the current LAN set once at startup, then keep
	// it aligned from the NetworkManager watcher. Startup failure is non-fatal:
	// unprivileged dev runs can't write /etc/avahi or restart the daemon, and
	// every step is idempotent — the next network event retries.
	if err := avahiSync.Apply(); err != nil {
		slog.Warn("avahi sync at startup", "err", err)
	}
	watchCtx, stopWatch := context.WithCancel(context.Background())
	go func() {
		if err := prov.Watch(watchCtx, func() {
			if err := avahiSync.Apply(); err != nil {
				slog.Error("avahi sync", "err", err)
			}
		}); err != nil {
			slog.Error("netstate watch unavailable; IP-change replay disabled", "err", err)
		}
	}()
	// On shutdown, stop the watcher and close both DBus connections (the
	// publisher's and the provider's). os.Exit skips defers on the error path
	// below, so that path closes explicitly too.
	defer func() {
		stopWatch()
		_ = pub.Close()
		_ = prov.Close()
	}()

	mux := http.NewServeMux()
	a.Mount(mux)

	slog.Info("host-agent-real listening", "sock", sockPath)
	srv := &http.Server{Handler: hostagent.LogRequests(mux)}
	if err := srv.Serve(ln); err != nil {
		slog.Error("serve", "err", err)
		// os.Exit skips the deferred cleanup; run it by hand first.
		stopWatch()
		_ = pub.Close()
		_ = prov.Close()
		os.Exit(1)
	}
}
