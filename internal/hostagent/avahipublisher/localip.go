// Local-IPv4 detection for the announced A records. No build tag: pure net
// code, shared so the non-Linux build compiles and the tests run everywhere.
package avahipublisher

import (
	"fmt"
	"log/slog"
	"net"
)

// detectLocalIPv4 returns the box's LAN IPv4: the kernel's route-chosen source
// address, falling back to interface enumeration when the probe fails.
func detectLocalIPv4() (string, error) {
	return detectIPv4(probeLANIPv4, enumerateLocalIPv4)
}

// detectIPv4 implements the probe-then-enumerate composition; split out so the
// fallback branches are unit-testable without manipulating the routing table.
func detectIPv4(probe, enumerate func() (string, error)) (string, error) {
	ip, err := probe()
	if err == nil {
		return ip, nil
	}
	ip, enumErr := enumerate()
	if enumErr != nil {
		return "", fmt.Errorf("avahipublisher: no usable local IPv4: route probe: %v; interface enumeration: %w", err, enumErr)
	}
	slog.Warn("avahipublisher: route probe failed (no default route?); announced address picked by interface enumeration and may be wrong",
		"err", err, "ip", ip)
	return ip, nil
}

// probeLANIPv4 asks the kernel's routing table which source address it would
// use to reach an outside host. Connecting a UDP socket performs only the
// route lookup — no packet is sent — so the target (192.0.2.1, TEST-NET-1)
// never sees anything and doesn't need to exist. The route lookup structurally
// excludes Docker bridges and other virtual interfaces: they are never the
// route toward a non-local address (#129).
func probeLANIPv4() (string, error) {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP.To4() == nil {
		return "", fmt.Errorf("unexpected local address %v", conn.LocalAddr())
	}
	return addr.IP.To4().String(), nil
}

// enumerateLocalIPv4 returns the first non-loopback, non-link-local IPv4 found
// on any interface. Fallback only: enumeration order is arbitrary, so on a box
// running Docker a bridge address can win (#129) — the route probe is the
// production path. Kept for boxes where the probe fails (no default route,
// e.g. a static IP with no gateway), where failing would block app installs.
func enumerateLocalIPv4() (string, error) {
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
