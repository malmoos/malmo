// Package manifest parses and validates manifest.yml (APP_MANIFEST.md).
// v1 skeleton: the required fields plus the permission flags the override
// generator currently consumes. The compose file itself is held verbatim and
// validated separately by the admission policy (APP_LIFECYCLE.md).
package manifest

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Manifest struct {
	ID              string      `yaml:"id"`
	ManifestVersion int         `yaml:"manifest_version"`
	Name            string      `yaml:"name"`
	Version         string      `yaml:"version"`
	ComposeFile     string      `yaml:"compose_file"`
	MainService     string      `yaml:"main_service"`
	MainPort        int         `yaml:"main_port"`
	PreferredSlugs  []string    `yaml:"preferred_slugs"`
	Permissions     Permissions `yaml:"permissions"`

	// Images is the optional catalog-promised per-image metadata
	// (APP_STORE.md # Catalog schema). Keyed by the exact `image:` reference
	// used in the compose (e.g. `traefik/whoami:v1.10.3`). CI resolves all three
	// fields at catalog-build time; the brain verifies Digest at install time
	// and uses DownloadBytes / DiskBytes for the on-disk footprint display.
	// Absent ⇒ TOFU at install (Door-2 always, Door-1 until the catalog
	// publishes a digest). Accepts both the legacy bare-digest string and the
	// full object form (see ImageEntry.UnmarshalYAML).
	Images map[string]ImageEntry `yaml:"images,omitempty"`

	// HealthProbe is the optional "up but not responding" probe config
	// (APP_MANIFEST.md # B). nil ⇒ the app is never probed and the
	// app-unresponsive health issue is never raised for it. Door-2 synthetic
	// manifests omit it.
	HealthProbe *HealthProbe `yaml:"health_probe,omitempty"`
}

// ImageEntry is the catalog-promised metadata for one container image
// (APP_STORE.md # Catalog schema). All three fields are CI-resolved at
// catalog-build time; only Digest gates the pull (Trust model); the byte counts
// are display-only and advisory — a drifted size is cosmetic, not an integrity
// failure.
type ImageEntry struct {
	Digest        string `yaml:"digest"`
	DownloadBytes int64  `yaml:"download_bytes,omitempty"` // compressed layer sum (bandwidth cost)
	DiskBytes     int64  `yaml:"disk_bytes,omitempty"`     // uncompressed layer sum (on-disk cost)
}

// UnmarshalYAML accepts the legacy bare-digest string ("sha256:…") as well as
// the full object form, so old manifests and test fixtures keep working.
func (e *ImageEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		e.Digest = node.Value
		return nil
	}
	type wire ImageEntry
	return node.Decode((*wire)(e))
}

// HealthProbe declares the HTTP probe that backs the app-unresponsive detector
// (APP_MANIFEST.md # B, HEALTH.md # app-unresponsive). It is not a Docker
// HEALTHCHECK: the brain executes it through the app's Caddy route on the
// health-poll tick. It accepts a shorthand string (`health_probe: /healthz`,
// expanding to {path: /healthz}) or the full mapping.
type HealthProbe struct {
	// Path is the HTTP path the brain GETs (required; must be absolute).
	Path string
	// HealthyStatus is the set of status codes treated as healthy. Empty ⇒ the
	// default "any status < 500" (the server answered coherently); 401/403/404
	// still count as responding.
	HealthyStatus []int
	// StartPeriod is the grace after the container starts before a failing probe
	// counts, so a warming-up app doesn't flap the banner on install/update.
	// Defaults to DefaultStartPeriod when omitted.
	StartPeriod time.Duration
}

// DefaultStartPeriod is the health_probe.start_period default (APP_MANIFEST.md
// # B): the warm-up grace before a probe failure counts.
const DefaultStartPeriod = 60 * time.Second

// healthProbeWire is the YAML mapping shape. start_period is a Go duration
// string ("60s") on the wire, parsed to a time.Duration in HealthProbe.
type healthProbeWire struct {
	Path          string `yaml:"path"`
	HealthyStatus []int  `yaml:"healthy_status,omitempty"`
	StartPeriod   string `yaml:"start_period,omitempty"`
}

// UnmarshalYAML accepts the shorthand string form or the full mapping
// (APP_MANIFEST.md # B). The string form sets only Path.
func (h *HealthProbe) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		*h = HealthProbe{Path: s}
		return nil
	}
	var w healthProbeWire
	if err := node.Decode(&w); err != nil {
		return fmt.Errorf("health_probe: %w", err)
	}
	h.Path = w.Path
	h.HealthyStatus = w.HealthyStatus
	if w.StartPeriod != "" {
		d, err := time.ParseDuration(w.StartPeriod)
		if err != nil {
			return fmt.Errorf("health_probe.start_period %q: %w", w.StartPeriod, err)
		}
		h.StartPeriod = d
	}
	return nil
}

// MarshalYAML emits the mapping form so a parsed manifest round-trips through
// the per-instance manifest.yml the installer writes (start_period back to a
// duration string).
func (h HealthProbe) MarshalYAML() (any, error) {
	w := healthProbeWire{Path: h.Path, HealthyStatus: h.HealthyStatus}
	if h.StartPeriod != 0 {
		w.StartPeriod = h.StartPeriod.String()
	}
	return w, nil
}

type Permissions struct {
	Internet bool `yaml:"internet"`
	LAN      bool `yaml:"lan"`

	// Folders is the app's declared access to use-case content folders
	// (APP_MANIFEST.md # folders). The manifest declares *what* content the app
	// touches (folder + mode + subfolder granularity); it does NOT declare the
	// source. Source — the owner's personal `~/<Folder>/` vs the household
	// `/srv/molma/shared/<Folder>/` — is the installer's per-folder election at
	// install time, because the author can't know whether a given household
	// wants "my own Jellyfin on my movies" or "on the family library"
	// (DECISIONS.md 2026-05-30 — folder source is installer-elected). Supersedes
	// the earlier `user_folders` / `shared_folders` split.
	Folders []Folder `yaml:"folders"`

	// Devices lists explicit /dev/... paths to pass through (Zigbee dongles,
	// webcams). The brain validates each exists before start (APP_ISOLATION.md #
	// Devices). Separate from GPU.
	Devices []string `yaml:"devices"`

	// GPU requests the platform-appropriate GPU runtime (NVIDIA / Intel / AMD),
	// its own field because driver wiring is platform-specific and not a plain
	// device passthrough (APP_MANIFEST.md # gpu). No-GPU box fails at the
	// capacity check.
	GPU bool `yaml:"gpu"`
}

// Folder is one declared use-case content folder. Folder names come from the
// fixed v1 taxonomy; Mode defaults to read; Scope defaults to whole.
// pick-subfolder may carry a Default subpath the user can override at install.
type Folder struct {
	Folder  string `yaml:"folder"`            // photos|documents|movies|music|notes|downloads
	Mode    string `yaml:"mode"`              // read|write (default read)
	Scope   string `yaml:"scope"`             // whole|pick-subfolder (default whole)
	Default string `yaml:"default,omitempty"` // default subpath for pick-subfolder

	// Target is the explicit in-container destination path, set ONLY by Door-2
	// synthetic manifests (DASHBOARD.md # Folder grants carry an explicit
	// destination path). A pasted third-party compose has no author to map
	// MOLMA_FOLDER_<NAME>, so the admin types where the app reads its data and
	// the brain binds the elected source straight there. Store manifests omit it
	// and keep the fixed `/molma/<folder>` + env-var convention.
	Target string `yaml:"target,omitempty"`
}

// folderTaxonomy is the fixed v1 use-case folder set (APP_ISOLATION.md # User
// content). User-defined folders are deferred.
var folderTaxonomy = map[string]bool{
	"photos": true, "documents": true, "movies": true,
	"music": true, "notes": true, "downloads": true,
}

func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	missing := func(field string) error {
		return fmt.Errorf("manifest missing required field: %s", field)
	}
	switch {
	case m.ID == "":
		return missing("id")
	case m.ManifestVersion == 0:
		return missing("manifest_version")
	case m.Name == "":
		return missing("name")
	case m.Version == "":
		return missing("version")
	case m.ComposeFile == "":
		return missing("compose_file")
	case m.MainService == "":
		return missing("main_service")
	case m.MainPort == 0:
		return missing("main_port")
	}
	if m.ManifestVersion != 1 {
		return fmt.Errorf("unsupported manifest_version %d (this build supports 1)", m.ManifestVersion)
	}
	// Slugs must be strict kebab-case so they stay parseable inside the
	// `<slug>--<user>` personal-instance scheme (DASHBOARD.md # instance
	// naming): single internal hyphens only — no leading/trailing hyphen and no
	// `--` run (which would collide with the owner separator and also covers the
	// reserved `xn--` prefix). The id is the fallback slug when preferred_slugs
	// is empty, so it's checked too.
	for _, slug := range append([]string{m.ID}, m.PreferredSlugs...) {
		if !kebabSlug.MatchString(slug) {
			return fmt.Errorf("slug %q must be kebab-case (lowercase alphanumerics, single internal hyphens)", slug)
		}
	}
	if err := m.validateHealthProbe(); err != nil {
		return err
	}
	return ValidatePermissions(&m.Permissions)
}

// validateHealthProbe checks the optional probe block and normalizes its
// start_period default in place (APP_MANIFEST.md # B). Absent ⇒ no-op. The path
// must be a non-empty absolute path (the brain GETs it through the app's Caddy
// route); healthy_status entries must be plausible HTTP codes.
func (m *Manifest) validateHealthProbe() error {
	if m.HealthProbe == nil {
		return nil
	}
	p := m.HealthProbe
	if p.Path == "" || !strings.HasPrefix(p.Path, "/") {
		return fmt.Errorf("health_probe.path must be a non-empty absolute path (e.g. /healthz), got %q", p.Path)
	}
	if p.StartPeriod < 0 {
		return fmt.Errorf("health_probe.start_period must not be negative, got %s", p.StartPeriod)
	}
	if p.StartPeriod == 0 {
		p.StartPeriod = DefaultStartPeriod
	}
	for _, s := range p.HealthyStatus {
		if s < 100 || s > 599 {
			return fmt.Errorf("health_probe.healthy_status: %d is not a valid HTTP status code", s)
		}
	}
	return nil
}

// ValidatePermissions normalizes folder defaults (mode=read, scope=whole) in
// place and rejects unknown folder names, modes, scopes, or malformed Door-2
// targets. It deliberately validates nothing about *source* — personal vs shared
// is resolved by the installer at install time, not declared here (APP_MANIFEST.md
// # folders). Exported because the Door-2 "Edit as YAML" overlay parses an
// admin-authored permissions block through the same gate (DASHBOARD.md # Form is
// a projection of the synthetic manifest).
func ValidatePermissions(p *Permissions) error {
	for i := range p.Folders {
		f := &p.Folders[i]
		if !folderTaxonomy[f.Folder] {
			return fmt.Errorf("permissions.folders: unknown folder %q (allowed: photos, documents, movies, music, notes, downloads)", f.Folder)
		}
		if f.Mode == "" {
			f.Mode = "read"
		}
		if f.Mode != "read" && f.Mode != "write" {
			return fmt.Errorf("permissions.folders[%s]: mode must be read or write, got %q", f.Folder, f.Mode)
		}
		if f.Scope == "" {
			f.Scope = "whole"
		}
		if f.Scope != "whole" && f.Scope != "pick-subfolder" {
			return fmt.Errorf("permissions.folders[%s]: scope must be whole or pick-subfolder, got %q", f.Folder, f.Scope)
		}
		if f.Default != "" {
			if f.Scope != "pick-subfolder" {
				return fmt.Errorf("permissions.folders[%s]: default is only valid with scope: pick-subfolder", f.Folder)
			}
			if strings.HasPrefix(f.Default, "/") || strings.Contains(f.Default, "..") {
				return fmt.Errorf("permissions.folders[%s]: default must be a relative subpath under the folder", f.Folder)
			}
		}
		// Door-2-only: the explicit in-container destination must be an absolute
		// path with no traversal (it's a container path the admin typed, not a
		// host path — host binds are an admission concern). Store grants omit it.
		if f.Target != "" && (!strings.HasPrefix(f.Target, "/") || strings.Contains(f.Target, "..")) {
			return fmt.Errorf("permissions.folders[%s]: target must be an absolute in-container path with no '..', got %q", f.Folder, f.Target)
		}
	}
	return nil
}

// kebabSlug matches lowercase alphanumeric labels joined by single hyphens:
// `home-assistant` ok; `whoami-`, `-x`, `a--b`, `xn--y`, `Foo` rejected.
var kebabSlug = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
