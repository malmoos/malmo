// Package avahipublisher — local-IP detection tests (no build tag, no DBus).
package avahipublisher

import (
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
)

// --- detectIPv4 composition (stubbed, environment-independent) ---------------

func TestDetectIPv4_ProbeWins(t *testing.T) {
	probe := func() (string, error) { return "192.168.1.10", nil }
	enumerate := func() (string, error) {
		t.Error("enumerate called although the probe succeeded")
		return "", nil
	}
	ip, err := detectIPv4(probe, enumerate)
	if err != nil {
		t.Fatalf("detectIPv4: %v", err)
	}
	if ip != "192.168.1.10" {
		t.Errorf("ip: want 192.168.1.10, got %q", ip)
	}
}

func TestDetectIPv4_FallsBackToEnumeration(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	probe := func() (string, error) { return "", errors.New("network is unreachable") }
	enumerate := func() (string, error) { return "10.0.0.5", nil }
	ip, err := detectIPv4(probe, enumerate)
	if err != nil {
		t.Fatalf("detectIPv4: want fallback success, got %v", err)
	}
	if ip != "10.0.0.5" {
		t.Errorf("ip: want 10.0.0.5, got %q", ip)
	}
	if !strings.Contains(buf.String(), "route probe failed") {
		t.Error("detectIPv4: want slog.Warn about route probe failure, got none")
	}
}

func TestDetectIPv4_BothFail(t *testing.T) {
	probe := func() (string, error) { return "", errors.New("probe boom") }
	enumerate := func() (string, error) { return "", errors.New("enum boom") }
	_, err := detectIPv4(probe, enumerate)
	if err == nil {
		t.Fatal("detectIPv4: want error when both paths fail, got nil")
	}
	for _, want := range []string{"probe boom", "enum boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

// --- probeLANIPv4 against the real routing table -----------------------------

func TestProbeLANIPv4(t *testing.T) {
	ip, err := probeLANIPv4()
	if err != nil {
		t.Skipf("route probe failed (box without a default route?): %v", err)
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		t.Fatalf("probe returned unparseable IPv4 %q", ip)
	}
	if parsed.IsLoopback() || parsed.IsLinkLocalUnicast() {
		t.Errorf("probe returned non-LAN address %s", ip)
	}
}
