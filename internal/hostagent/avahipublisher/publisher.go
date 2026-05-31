//go:build linux

// Package avahipublisher publishes per-app A-record aliases via the Avahi DBus
// API. It replaces the earlier static-file approach (0012 false start) which
// could not publish raw A records — Avahi static files announce *services*, not
// bare hostname aliases. See DECISIONS.md entry 2026-05-24 and
// docs/progress/0013-avahi-dbus-publisher.md for the full story.
//
// # Mechanism
//
// For each app slug the publisher:
//  1. Calls org.freedesktop.Avahi.Server.EntryGroupNew to get a fresh group.
//  2. Calls org.freedesktop.Avahi.EntryGroup.AddAddress with the slug hostname
//     and the box's local IPv4 address.
//  3. Calls org.freedesktop.Avahi.EntryGroup.Commit to start the announcement.
//
// Groups are stored in p.groups[slug]. Unpublish calls EntryGroup.Free, which
// withdraws the announcement and releases the DBus object.
//
// # Restart durability
//
// DBus groups are lost when host-agent restarts. The brain re-publishes all
// running instances at startup via lifecycle.Reconcile (which already calls
// m.host.Publish per running instance). Mid-life host-agent restart while the
// brain is running is a known gap — see progress/0013.
//
// # Local-IP detection
//
// First non-loopback, non-link-local (169.254.x.x) IPv4 address on any
// interface. Works for single-primary-interface boxes; multi-homed / dual-stack
// will need a future tweak (see Known Gaps in the progress doc).
//
// # Non-Linux builds
//
// dbus_other.go provides a stub DBusPublisher for !linux that returns
// "not supported on this OS" from every method. The fake binary (cmd/host-agent)
// never instantiates DBusPublisher; this exists so the package compiles on macOS.
package avahipublisher

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
)

// Avahi DBus constants from <avahi-common/defs.h>.
const (
	avahiInterfaceUnspec int32 = -1
	avahiProtoUnspec     int32 = -1

	// AddAddress publish flag. NO_REVERSE = don't publish the reverse PTR
	// record. Avahi already owns 192.168.x.x → <hostname>.local via the host's
	// own address registration; re-publishing the reverse for a second name
	// triggers "Local name collision" because the PTR is unique by IP.
	// Forward A record collisions are handled by Avahi's own probing.
	avahiPublishFlagsAlias uint32 = 16
)

// avahiService and avahiServer are the DBus service/path/interface constants.
const (
	avahiService         = "org.freedesktop.Avahi"
	avahiServerPath      = dbus.ObjectPath("/")
	avahiServerIface     = "org.freedesktop.Avahi.Server"
	avahiEntryGroupIface = "org.freedesktop.Avahi.EntryGroup"
)

// ErrCollision is returned by Publish when Avahi reports a name collision for
// the requested hostname. Callers that need to react differently (e.g. slug
// rename) can check with errors.Is.
var ErrCollision = errors.New("avahipublisher: name collision")

// DBusPublisher publishes per-app A-record aliases via Avahi's DBus API.
// Zero value is not usable — construct via &DBusPublisher{HostSuffix: ...}.
// Safe for concurrent use.
type DBusPublisher struct {
	// HostSuffix is appended to each slug to form the announced hostname
	// (production: protocol.AppHostSuffix, ".local").
	HostSuffix string

	// localIP may be set in tests (via a subtype or field override); otherwise
	// it is detected lazily on the first Publish call and cached.
	localIP string

	mu     sync.Mutex
	conn   *dbus.Conn
	groups map[string]dbus.ObjectPath // slug → Avahi EntryGroup object path
}

// Publish announces an A record for the slug pointing at the box's local IPv4
// address and returns the announced hostname.
//
// The primary name is "<slug>" + HostSuffix (e.g. "photos.local"). If Avahi
// reports a name collision (another device on the LAN already owns it), Publish
// retries once with a box-qualified fallback, "<slug>-<box>" + HostSuffix (e.g.
// "photos-malmo.local"), where <box> is this host's name. The caller (the
// brain) must use the *returned* name for the URL it shows and the Caddy route
// it writes — it may differ from the primary on collision. See DISCOVERY.md.
//
// If the slug is already published (e.g. replay on brain restart), the old
// group is freed first and a fresh one is committed — idempotent.
//
// Returns ErrCollision only if both the primary and the fallback collide.
//
// Limitation: collision is detected on the synchronous AddAddress error. Avahi
// can also report a conflict asynchronously after Commit (EntryGroup state
// COLLISION); that path is not yet watched here — see the progress doc.
func (p *DBusPublisher) Publish(slug string) (string, error) {
	if !slugRE.MatchString(slug) {
		return "", fmt.Errorf("avahipublisher: invalid slug %q (must match [a-z0-9-]+)", slug)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureConn(); err != nil {
		return "", err
	}

	localIP, err := p.ensureLocalIP()
	if err != nil {
		return "", err
	}

	// If already published, free the old group first (idempotent replay).
	if old, ok := p.groups[slug]; ok {
		_ = p.freeGroup(old)
		delete(p.groups, slug)
	}

	hostname := slug + p.HostSuffix
	groupPath, err := p.tryPublish(hostname, localIP)
	if errors.Is(err, ErrCollision) {
		fallback := slug + "-" + p.boxLabel() + p.HostSuffix
		slog.Warn("avahi name collision; retrying with box-qualified name",
			"slug", slug, "name", hostname, "fallback", fallback)
		hostname = fallback
		groupPath, err = p.tryPublish(hostname, localIP)
	}
	if err != nil {
		return "", err
	}

	if p.groups == nil {
		p.groups = make(map[string]dbus.ObjectPath)
	}
	p.groups[slug] = groupPath
	slog.Info("avahi publish", "slug", slug, "name", hostname, "ip", localIP)
	return hostname, nil
}

// tryPublish creates a fresh entry group, adds the A record, and commits it.
// On an Avahi collision it returns ErrCollision (wrapped) so Publish can retry
// with a box-qualified name. The group is freed on any error.
func (p *DBusPublisher) tryPublish(hostname, localIP string) (dbus.ObjectPath, error) {
	groupPath, err := p.newEntryGroup()
	if err != nil {
		return "", fmt.Errorf("avahipublisher: EntryGroupNew: %w", err)
	}

	if err := p.addAddress(groupPath, hostname, localIP); err != nil {
		_ = p.freeGroup(groupPath)
		if isCollision(err) {
			return "", fmt.Errorf("%w: %s", ErrCollision, hostname)
		}
		return "", fmt.Errorf("avahipublisher: AddAddress(%s, %s): %w", hostname, localIP, err)
	}

	if err := p.commitGroup(groupPath); err != nil {
		_ = p.freeGroup(groupPath)
		return "", fmt.Errorf("avahipublisher: Commit(%s): %w", hostname, err)
	}
	return groupPath, nil
}

// boxLabel returns this host's name as a single DNS label for use in the
// collision-fallback name ("<slug>-<box>.local"). Sanitization lives in
// sanitizeBoxLabel (slug.go) so it can be unit-tested without DBus.
func (p *DBusPublisher) boxLabel() string {
	h, err := os.Hostname()
	if err != nil {
		return "malmo"
	}
	return sanitizeBoxLabel(h)
}

// Unpublish withdraws the A record for the given slug.
// Idempotent: if the slug was never published (or already unpublished), returns nil.
func (p *DBusPublisher) Unpublish(slug string) error {
	if !slugRE.MatchString(slug) {
		return fmt.Errorf("avahipublisher: invalid slug %q (must match [a-z0-9-]+)", slug)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	groupPath, ok := p.groups[slug]
	if !ok {
		return nil // already gone — idempotent
	}

	if err := p.freeGroup(groupPath); err != nil {
		return fmt.Errorf("avahipublisher: Free(%s): %w", slug, err)
	}
	delete(p.groups, slug)
	slog.Info("avahi unpublish", "slug", slug)
	return nil
}

// Close frees all entry groups and closes the DBus connection.
// Should be called (via defer) when host-agent shuts down.
func (p *DBusPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for slug, groupPath := range p.groups {
		if err := p.freeGroup(groupPath); err != nil {
			slog.Warn("avahi close: free group", "slug", slug, "err", err)
		}
	}
	p.groups = nil

	if p.conn != nil {
		err := p.conn.Close()
		p.conn = nil
		return err
	}
	return nil
}

// --- internal helpers (all called with p.mu held) ---

// ensureConn connects to the system bus if not already connected.
func (p *DBusPublisher) ensureConn() error {
	if p.conn != nil {
		return nil
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("avahipublisher: connect system bus: %w", err)
	}
	p.conn = conn
	return nil
}

// ensureLocalIP returns the cached local IPv4, detecting it on first call.
func (p *DBusPublisher) ensureLocalIP() (string, error) {
	if p.localIP != "" {
		return p.localIP, nil
	}
	ip, err := detectLocalIPv4()
	if err != nil {
		return "", err
	}
	p.localIP = ip
	return ip, nil
}

// newEntryGroup calls org.freedesktop.Avahi.Server.EntryGroupNew and returns
// the resulting object path.
func (p *DBusPublisher) newEntryGroup() (dbus.ObjectPath, error) {
	server := p.conn.Object(avahiService, avahiServerPath)
	var groupPath dbus.ObjectPath
	if err := server.Call(avahiServerIface+".EntryGroupNew", 0).Store(&groupPath); err != nil {
		return "", err
	}
	return groupPath, nil
}

// addAddress calls org.freedesktop.Avahi.EntryGroup.AddAddress on the given group.
func (p *DBusPublisher) addAddress(groupPath dbus.ObjectPath, hostname, ip string) error {
	group := p.conn.Object(avahiService, groupPath)
	return group.Call(avahiEntryGroupIface+".AddAddress", 0,
		avahiInterfaceUnspec,
		avahiProtoUnspec,
		avahiPublishFlagsAlias,
		hostname,
		ip,
	).Err
}

// commitGroup calls org.freedesktop.Avahi.EntryGroup.Commit.
func (p *DBusPublisher) commitGroup(groupPath dbus.ObjectPath) error {
	group := p.conn.Object(avahiService, groupPath)
	return group.Call(avahiEntryGroupIface+".Commit", 0).Err
}

// freeGroup calls org.freedesktop.Avahi.EntryGroup.Free, withdrawing the
// announcement. Best-effort: errors are returned but not fatal on shutdown.
func (p *DBusPublisher) freeGroup(groupPath dbus.ObjectPath) error {
	group := p.conn.Object(avahiService, groupPath)
	return group.Call(avahiEntryGroupIface+".Free", 0).Err
}

// isCollision reports whether the DBus error is an Avahi name-collision error.
func isCollision(err error) bool {
	if err == nil {
		return false
	}
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) {
		return dbusErr.Name == "org.freedesktop.Avahi.CollisionFailure"
	}
	return false
}

// detectLocalIPv4 returns the first non-loopback, non-link-local IPv4 address
// found on any interface. It prefers RFC1918 addresses (10/8, 172.16-31/12,
// 192.168/16) but falls back to any non-loopback non-link-local match.
//
// Limitation: on multi-homed boxes this picks whichever interface enumerates
// first. A future tweak could prefer the interface NetworkManager reports as
// the primary LAN connection. Single-primary-interface boxes (the v1 target)
// are not affected.
func detectLocalIPv4() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("avahipublisher: list interfaces: %w", err)
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil {
			continue
		}
		ip4 := ip.To4()
		if ip4 == nil {
			continue // IPv6
		}
		if ip4.IsLoopback() {
			continue
		}
		if ip4.IsLinkLocalUnicast() { // 169.254.x.x
			continue
		}
		return ip4.String(), nil
	}
	return "", fmt.Errorf("avahipublisher: no usable local IPv4 address found (loopback and link-local excluded)")
}
