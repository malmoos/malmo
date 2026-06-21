package timezone

import (
	"strings"
	"testing"
)

// These tests stay side-effect-free: they exercise the zone-shape guard and the
// regex directly, never the valid-zone path — that shells out to
// `timedatectl set-timezone`, which would mutate the host's clock config and
// needs root + a booted system (covered in the QEMU outer loop, not here).

func TestSetTimezone_RejectsInvalidBeforeExec(t *testing.T) {
	invalid := []string{
		"",               // empty
		"Not A Zone!",    // spaces + punctuation
		"../etc/passwd",  // path traversal
		"/UTC",           // leading slash
		"Europe/",        // trailing slash, empty component
		"Europe//Berlin", // empty middle component
		"$(reboot)",      // shell-ish
		"Europe/Stockholm; rm -rf /",
	}
	for _, zone := range invalid {
		err := New().SetTimezone(zone)
		if err == nil {
			t.Errorf("SetTimezone(%q) = nil; want validation error", zone)
			continue
		}
		if !strings.Contains(err.Error(), "invalid timezone") {
			t.Errorf("SetTimezone(%q) error = %q; want a validation rejection (exec must not run)", zone, err)
		}
	}
}

func TestZoneRegex_AcceptsValidShapes(t *testing.T) {
	valid := []string{
		"UTC",
		"Europe/Stockholm",
		"America/Argentina/Buenos_Aires",
		"Etc/GMT+5",
		"America/Port-au-Prince",
	}
	for _, zone := range valid {
		if !zoneRe.MatchString(zone) {
			t.Errorf("zoneRe rejected valid zone %q", zone)
		}
	}
}

func TestZoneRegex_RejectsInvalidShapes(t *testing.T) {
	invalid := []string{"", "lower case", "Europe/", "/UTC", "Europe//Berlin", "a/b/"}
	for _, zone := range invalid {
		if zoneRe.MatchString(zone) {
			t.Errorf("zoneRe accepted invalid zone %q", zone)
		}
	}
}
