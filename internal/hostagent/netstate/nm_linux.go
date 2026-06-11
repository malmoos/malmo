//go:build linux

// NetworkManager LAN-set provider. NM is the source of truth for which
// interfaces face the LAN (BOOT.md # NetworkManager owns the network stack):
// the LAN set is its active connections of type ethernet or WiFi — enumerated
// by device type, not the primary-connection pin, because a box with ethernet
// AND WiFi up has two LAN faces and apps must announce on both (#130).
package netstate

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// NetworkManager DBus names (networkmanager.dev/docs/api/latest/spec.html).
const (
	nmService        = "org.freedesktop.NetworkManager"
	nmManagerPath    = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	nmManagerIface   = "org.freedesktop.NetworkManager"
	nmActiveIface    = "org.freedesktop.NetworkManager.Connection.Active"
	nmDeviceIface    = "org.freedesktop.NetworkManager.Device"
	nmIP4ConfigIface = "org.freedesktop.NetworkManager.IP4Config"
)

// nmActiveStateActivated is NM_ACTIVE_CONNECTION_STATE_ACTIVATED: the
// connection is up and addressed. Connections still activating are excluded —
// they reappear via Watch once they carry an address.
const nmActiveStateActivated uint32 = 2

// lanConnectionTypes are the NM connection types that face the LAN. Everything
// else — bridge (Docker), tun (mesh), veth, loopback — is excluded by absence,
// which is what keeps app names off non-LAN interfaces (DISCOVERY.md
// # Interface scoping).
var lanConnectionTypes = map[string]bool{
	"802-3-ethernet":  true,
	"802-11-wireless": true,
}

// defaultDebounce is the quiet period Watch waits after the last relevant NM
// signal before firing: activation emits a burst of property changes (state,
// devices, IP config) that must coalesce into one recompute.
const defaultDebounce = 2 * time.Second

// NMProvider computes the LAN set from NetworkManager over the system DBus.
// Zero value is usable — the connection is established lazily. Safe for
// concurrent use.
type NMProvider struct {
	// Debounce overrides the Watch quiet period (default 2s). Exposed for
	// tests; production wiring leaves it zero.
	Debounce time.Duration

	mu   sync.Mutex
	conn *dbus.Conn
}

// LANInterfaces returns the current LAN set, sorted by interface name:
// one entry per active ethernet/WiFi connection that carries an IPv4 address.
// An empty set is not an error — all links down is a state, and the caller
// decides what to do with it (publishers fail, the watcher retries on the
// next NM event).
func (p *NMProvider) LANInterfaces() ([]LANInterface, error) {
	conn, err := p.ensureConn()
	if err != nil {
		return nil, err
	}

	mgr := conn.Object(nmService, nmManagerPath)
	v, err := mgr.GetProperty(nmManagerIface + ".ActiveConnections")
	if err != nil {
		return nil, fmt.Errorf("netstate: NetworkManager ActiveConnections: %w (is NetworkManager running?)", err)
	}
	paths, ok := v.Value().([]dbus.ObjectPath)
	if !ok {
		return nil, fmt.Errorf("netstate: unexpected ActiveConnections type %T", v.Value())
	}

	var out []LANInterface
	seen := map[string]bool{}
	for _, acPath := range paths {
		lan, ok := lanFromActiveConnection(conn, acPath)
		if !ok || seen[lan.Name] {
			continue
		}
		seen[lan.Name] = true
		out = append(out, lan)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// lanFromActiveConnection reads one active connection and returns its LAN
// interface, or ok=false when it isn't one: wrong type, not yet ACTIVATED, no
// IPv4 address, or it vanished mid-read — connections deactivate between the
// list call and the property reads, and those races are normal, not errors.
func lanFromActiveConnection(conn *dbus.Conn, acPath dbus.ObjectPath) (LANInterface, bool) {
	ac := conn.Object(nmService, acPath)

	typ, err := getString(ac, nmActiveIface+".Type")
	if err != nil || !lanConnectionTypes[typ] {
		return LANInterface{}, false
	}
	state, err := getUint32(ac, nmActiveIface+".State")
	if err != nil || state != nmActiveStateActivated {
		return LANInterface{}, false
	}

	devPaths, err := getPaths(ac, nmActiveIface+".Devices")
	if err != nil || len(devPaths) == 0 {
		return LANInterface{}, false
	}
	// Ethernet/WiFi active connections have exactly one device; its Interface
	// is the kernel name carrying the address for these types.
	name, err := getString(conn.Object(nmService, devPaths[0]), nmDeviceIface+".Interface")
	if err != nil || name == "" {
		return LANInterface{}, false
	}

	cfgPath, err := getPath(ac, nmActiveIface+".Ip4Config")
	if err != nil || cfgPath == "/" { // "/" = no IPv4 configuration yet
		return LANInterface{}, false
	}
	ip, ok := firstIPv4Address(conn.Object(nmService, cfgPath))
	if !ok {
		return LANInterface{}, false
	}

	// Kernel ifindex via stdlib — Avahi's AddAddress wants the index, and NM
	// doesn't expose it directly.
	iface, err := net.InterfaceByName(name)
	if err != nil {
		slog.Warn("netstate: NM device has no kernel interface; skipped", "host", name, "err", err)
		return LANInterface{}, false
	}

	return LANInterface{Name: name, Index: iface.Index, IPv4: ip}, true
}

// firstIPv4Address reads IP4Config.AddressData and returns the first address.
//
// Assumes one IPv4 per LAN interface — the single-DHCP-lease case that is the
// only target-hardware config. NM orders the lease/primary address first, so
// on a multi-address interface (not a config we ship for) this announces only
// AddressData[0] and the rest go unannounced; see the known gap in
// docs/progress/network-state-discovery.md. The robust fix, if that ever
// becomes real, is to announce every address on the interface, not to pick a
// "primary" — NM exposes no such property to pick by.
func firstIPv4Address(cfg dbus.BusObject) (string, bool) {
	v, err := cfg.GetProperty(nmIP4ConfigIface + ".AddressData")
	if err != nil {
		return "", false
	}
	entries, ok := v.Value().([]map[string]dbus.Variant)
	if !ok {
		return "", false
	}
	for _, e := range entries {
		if addr, ok := e["address"].Value().(string); ok && addr != "" {
			return addr, true
		}
	}
	return "", false
}

// Watch blocks, invoking onChange after a debounce whenever NetworkManager
// state changes — connections activating/deactivating, IPv4 leases moving. It
// computes no diff itself: the callback re-reads LANInterfaces and decides
// whether anything it cares about moved. Returns nil when ctx ends, an error
// if the signal subscription cannot be established.
func (p *NMProvider) Watch(ctx context.Context, onChange func()) error {
	conn, err := p.ensureConn()
	if err != nil {
		return err
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
		dbus.WithMatchMember("PropertiesChanged"),
		dbus.WithMatchPathNamespace(nmManagerPath),
	); err != nil {
		return fmt.Errorf("netstate: subscribe to NetworkManager signals: %w", err)
	}

	ch := make(chan *dbus.Signal, 64)
	conn.Signal(ch)
	defer conn.RemoveSignal(ch)

	debounce := p.Debounce
	if debounce <= 0 {
		debounce = defaultDebounce
	}
	timer := time.NewTimer(debounce)
	timer.Stop() // armed by the first relevant signal
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case sig, ok := <-ch:
			if !ok {
				return fmt.Errorf("netstate: DBus signal channel closed")
			}
			if relevantSignal(sig) {
				timer.Reset(debounce)
			}
		case <-timer.C:
			onChange()
		}
	}
}

// relevantSignal filters the PropertiesChanged stream down to the objects the
// LAN set is computed from. WiFi access-point objects in particular emit
// signal-strength updates every few seconds; without the filter every one
// would arm the debounce timer.
func relevantSignal(sig *dbus.Signal) bool {
	if sig.Name != "org.freedesktop.DBus.Properties.PropertiesChanged" || len(sig.Body) == 0 {
		return false
	}
	iface, ok := sig.Body[0].(string)
	if !ok {
		return false
	}
	switch iface {
	case nmManagerIface, nmActiveIface, nmDeviceIface, nmIP4ConfigIface:
		return true
	}
	return false
}

// Close closes the DBus connection. Safe to call when never connected.
func (p *NMProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return nil
	}
	err := p.conn.Close()
	p.conn = nil
	return err
}

func (p *NMProvider) ensureConn() (*dbus.Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		return p.conn, nil
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("netstate: connect system bus: %w", err)
	}
	p.conn = conn
	return conn, nil
}

// --- typed property getters ---------------------------------------------------

func getString(obj dbus.BusObject, prop string) (string, error) {
	v, err := obj.GetProperty(prop)
	if err != nil {
		return "", err
	}
	s, ok := v.Value().(string)
	if !ok {
		return "", fmt.Errorf("netstate: %s: unexpected type %T", prop, v.Value())
	}
	return s, nil
}

func getUint32(obj dbus.BusObject, prop string) (uint32, error) {
	v, err := obj.GetProperty(prop)
	if err != nil {
		return 0, err
	}
	u, ok := v.Value().(uint32)
	if !ok {
		return 0, fmt.Errorf("netstate: %s: unexpected type %T", prop, v.Value())
	}
	return u, nil
}

func getPath(obj dbus.BusObject, prop string) (dbus.ObjectPath, error) {
	v, err := obj.GetProperty(prop)
	if err != nil {
		return "", err
	}
	p, ok := v.Value().(dbus.ObjectPath)
	if !ok {
		return "", fmt.Errorf("netstate: %s: unexpected type %T", prop, v.Value())
	}
	return p, nil
}

func getPaths(obj dbus.BusObject, prop string) ([]dbus.ObjectPath, error) {
	v, err := obj.GetProperty(prop)
	if err != nil {
		return nil, err
	}
	ps, ok := v.Value().([]dbus.ObjectPath)
	if !ok {
		return nil, fmt.Errorf("netstate: %s: unexpected type %T", prop, v.Value())
	}
	return ps, nil
}
