// Package timezone implements hostagent.TimezoneSetter backed by the Linux
// shell-out `timedatectl set-timezone <zone>` (TIME.md # System TZ). It is
// intentionally isolated so the shared internal/hostagent package carries no
// timedatectl dependency; only cmd/host-agent-real imports it, in both build
// profiles (a hosted VM and an appliance both run in a timezone).
package timezone

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
)

// Setter implements hostagent.TimezoneSetter against the local system via
// timedatectl. The zero value is ready to use.
type Setter struct{}

// New returns a Setter.
func New() *Setter { return &Setter{} }

// zoneRe bounds the accepted IANA tz database name to area[/location...]
// components of safe characters (the shape timedatectl ships, e.g.
// "Europe/Stockholm", "America/Argentina/Buenos_Aires", "UTC"). It is a guard
// at the privileged boundary — the brain validates the same shape before the
// call — to keep anything path-traversal-y or otherwise unexpected from
// reaching timedatectl's argv, even though the exec is shell-free.
var zoneRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+)*$`)

// SetTimezone applies zone as the system timezone. It validates the zone shape,
// then runs `timedatectl set-timezone <zone>`, which itself rejects a name not
// in the tz database. A non-zero exit surfaces timedatectl's stderr.
func (s *Setter) SetTimezone(zone string) error {
	if !zoneRe.MatchString(zone) {
		return fmt.Errorf("invalid timezone %q", zone)
	}
	var stderr bytes.Buffer
	cmd := exec.Command("timedatectl", "set-timezone", zone)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("timedatectl set-timezone %q: %w: %s", zone, err, stderr.String())
	}
	return nil
}
