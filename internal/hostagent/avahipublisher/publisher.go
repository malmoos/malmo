// Package avahipublisher writes static Avahi service files that register
// per-app .local DNS-A aliases. Pure Go — no CGO, no DBus.
//
// File approach rationale (DISCOVERY.md §2 "Per-app A records"):
//   - Avahi watches /etc/avahi/services/ and announces new files automatically.
//   - Re-announcement on IP change and link-up is handled by Avahi itself —
//     one reconciler write is durable across daemon restarts (unlike avahi-publish,
//     which would require a long-lived process per name).
//   - DBus rejected: EntryGroup.StateChanged would require the brain to replay
//     registrations on host-agent restart — static files survive restarts for free.
//
// Service type rationale:
//   - Avahi's static-file schema requires at least one <service> element to
//     publish a <host-name> A record.
//   - We use _malmo-app._tcp (a project-specific dummy type) to avoid surfacing
//     a browsable _http._tcp service in Finder / iOS Files sidebars.
//     See DISCOVERY.md §3 ("For individual apps: not in v1") and
//     docs/progress/0012-host-agent-avahi-files.md for full rationale.
package avahipublisher

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
)

// slugRE is the valid slug pattern — same character class the catalog/manifest
// layer enforces. Defensive: rejects path-traversal attempts before we ever
// touch the filesystem.
var slugRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// FilePublisher writes /etc/avahi/services/app-<slug>.service XML files.
type FilePublisher struct {
	// Dir is the Avahi service-file directory (production: "/etc/avahi/services").
	Dir string
	// HostSuffix is appended to the slug to form the .local hostname
	// (production: ".malmo.local").
	HostSuffix string
}

// Publish writes the Avahi service file for the given slug and returns the
// published hostname (e.g. "myapp.malmo.local").
//
// The file is written with mode 0644 (Avahi reads it as root but we don't need
// tighter permissions; group-readable aids diagnostic inspection).
//
// Slug validation: rejects anything not matching [a-z0-9-]+ to prevent
// path-traversal injection into the service-file name.
//
// Note: file-write success implies "established" in the BRAIN_HOST_PROTOCOL.md
// sense — Avahi will pick up the file and multicast within <1 s on a healthy
// LAN. We do not subscribe to DBus EntryGroup.StateChanged; see known gaps in
// docs/progress/0012-host-agent-avahi-files.md.
func (p *FilePublisher) Publish(slug string) (string, error) {
	if !slugRE.MatchString(slug) {
		return "", fmt.Errorf("avahipublisher: invalid slug %q (must match [a-z0-9-]+)", slug)
	}

	name := slug + p.HostSuffix
	xml := buildXML(slug, name)
	path := p.filePath(slug)

	if err := os.WriteFile(path, []byte(xml), 0o644); err != nil {
		return "", fmt.Errorf("avahipublisher: write %s: %w", path, err)
	}

	slog.Info("avahi publish", "slug", slug, "name", name, "file", path)
	return name, nil
}

// Unpublish removes the Avahi service file for the given slug.
// Idempotent: if the file is already gone, returns nil.
func (p *FilePublisher) Unpublish(slug string) error {
	if !slugRE.MatchString(slug) {
		return fmt.Errorf("avahipublisher: invalid slug %q (must match [a-z0-9-]+)", slug)
	}

	path := p.filePath(slug)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("avahipublisher: remove %s: %w", path, err)
	}

	slog.Info("avahi unpublish", "slug", slug, "file", path)
	return nil
}

// filePath returns the canonical path for the slug's service file.
func (p *FilePublisher) filePath(slug string) string {
	return filepath.Join(p.Dir, "app-"+slug+".service")
}

// buildXML renders the Avahi service-file XML for the given slug and hostname.
func buildXML(slug, hostName string) string {
	return fmt.Sprintf(`<?xml version="1.0" standalone='no'?>
<!DOCTYPE service-group SYSTEM "avahi-service.dtd">
<service-group>
  <name replace-wildcards="no">app-%s</name>
  <host-name>%s</host-name>
  <service>
    <!-- Dummy required by Avahi's schema; project-specific type so it
         doesn't surface in Finder / iOS Files browser. See DISCOVERY.md §3
         and docs/progress/0012-host-agent-avahi-files.md for rationale. -->
    <type>_malmo-app._tcp</type>
    <port>0</port>
  </service>
</service-group>
`, slug, hostName)
}
