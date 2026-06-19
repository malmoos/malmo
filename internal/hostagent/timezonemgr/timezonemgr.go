// Package timezonemgr implements hostagent.TimezoneManager backed by the Linux
// `timedatectl set-timezone` command (TIME.md # System TZ). It sets the host's
// /etc/localtime symlink, which journald, cron, and anything reading time.Local
// observe. The internal/hostagent package has no timedatectl dependency; only
// cmd/host-agent-real wires this concrete type.
package timezonemgr

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// LinuxTimezoneManager sets the system timezone via timedatectl.
type LinuxTimezoneManager struct{}

// SetTimezone runs `timedatectl set-timezone <tz>`. tz is an IANA zone name
// already shape-validated by the host-agent handler; timedatectl itself rejects
// an unknown zone, surfaced here as an error. Argument form (not a shell) makes
// command injection structurally impossible regardless of the caller's checks.
func (m *LinuxTimezoneManager) SetTimezone(tz string) error {
	cmd := exec.Command("timedatectl", "set-timezone", tz)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("timedatectl set-timezone %s: %w: %s", tz, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
