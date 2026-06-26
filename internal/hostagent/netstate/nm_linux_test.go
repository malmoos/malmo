//go:build linux && nmtest && !hosted

// Package netstate — integration tests against a real NetworkManager.
//
// Gated behind the "nmtest" build tag for the same reason avahipublisher's
// DBus tests are behind "avahitest": there is no honest way to fake the
// NM server side — a DBus stub implementing the manager, active-connection,
// device, and IP4Config objects would be more code than the provider and
// wrong in different ways. Run by hand on a Linux box with NetworkManager:
//
//	go test -tags nmtest ./internal/hostagent/netstate/
//
// A box running Docker is the interesting case: NM lists the docker0/br-*
// bridges as active "bridge"-type connections, which is exactly what the
// type filter must exclude (#129's failure mode, made structural in #130).
package netstate

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// TestNMProvider_LANInterfaces exercises the full DBus read path: every
// returned interface must be a real kernel interface of LAN type, carry a
// usable IPv4, and no bridge/veth/loopback may leak through.
func TestNMProvider_LANInterfaces(t *testing.T) {
	p := &NMProvider{}
	defer p.Close()

	ifaces, err := p.LANInterfaces()
	if err != nil {
		t.Skipf("LANInterfaces failed (NetworkManager not running?): %v", err)
	}
	if len(ifaces) == 0 {
		t.Skip("no active ethernet/WiFi connections on this box")
	}

	for _, li := range ifaces {
		t.Logf("LAN interface: %+v", li)
		for _, bad := range []string{"docker", "br-", "veth", "lo"} {
			if strings.HasPrefix(li.Name, bad) {
				t.Errorf("non-LAN interface %q leaked through the type filter", li.Name)
			}
		}
		ip := net.ParseIP(li.IPv4)
		if ip == nil || ip.To4() == nil {
			t.Errorf("%s: unparseable IPv4 %q", li.Name, li.IPv4)
		} else if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			t.Errorf("%s: non-LAN address %s", li.Name, li.IPv4)
		}
		kern, err := net.InterfaceByName(li.Name)
		if err != nil {
			t.Errorf("%s: not a kernel interface: %v", li.Name, err)
		} else if kern.Index != li.Index {
			t.Errorf("%s: index %d, kernel says %d", li.Name, li.Index, kern.Index)
		}
	}
}

// TestNMProvider_WatchSubscribes verifies Watch establishes its subscription
// and honors context cancellation. It does not force an NM state change —
// flipping links on a developer box is not acceptable test behavior — so the
// debounce path is covered by the QEMU lane, where links are ours to flip.
func TestNMProvider_WatchSubscribes(t *testing.T) {
	p := &NMProvider{Debounce: 50 * time.Millisecond}
	defer p.Close()

	if _, err := p.LANInterfaces(); err != nil {
		t.Skipf("NetworkManager not reachable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Watch(ctx, func() {}) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return after context cancellation")
	}
}
