// Package manifest parses and validates manifest.yml (APP_MANIFEST.md).
// v1 skeleton: the required fields plus the permission flags the override
// generator currently consumes. The compose file itself is held verbatim and
// validated separately by the admission policy (APP_LIFECYCLE.md).
package manifest

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Manifest struct {
	ID              string      `yaml:"id"`
	ManifestVersion int         `yaml:"manifest_version"`
	Name            string      `yaml:"name"`
	Version         string      `yaml:"version"`
	Description     Description `yaml:"description,omitempty"`

	// Identity/metadata for the store UI (APP_MANIFEST.md # A). All optional —
	// the brain doesn't act on any of them; they're surfaced verbatim to the
	// store grid and detail page. Icon and Screenshots are package-relative paths
	// (`./icon.png`); the catalog turns them into asset URLs (APP_STORE.md #
	// Catalog schema). Absent ⇒ the store falls back to a generic glyph and an
	// empty gallery.
	Icon string `yaml:"icon,omitempty"`
	// IconGlyph names a Lucide icon (kebab-case, e.g. `notebook-pen`; the set at
	// https://lucide.dev/icons) the store renders as the card/header glyph when no
	// raster Icon is declared. Author-chosen fallback for apps that ship no logo;
	// the brain doesn't act on it and can't verify the name exists (the icon set
	// lives in the UI), so an unknown-but-well-formed name degrades to the generic
	// glyph client-side. Ignored when Icon is set (APP_STORE.md # Catalog schema).
	IconGlyph    string   `yaml:"icon_glyph,omitempty"`
	Screenshots  []string `yaml:"screenshots,omitempty"`
	Categories   []string `yaml:"categories,omitempty"`
	Author       Author   `yaml:"author,omitempty"`
	License      string   `yaml:"license,omitempty"`
	Links        Links    `yaml:"links,omitempty"`
	ChangelogURL string   `yaml:"changelog_url,omitempty"`

	// Listed controls store visibility (APP_MANIFEST.md # A). A *bool so absent is
	// distinguishable from explicit false: nil or true ⇒ the app shows in the store
	// grid/detail page and can be installed; explicit `listed: false` ⇒ the manifest
	// stays in the catalog (parses, lints, serves icons) but is hidden from browse
	// and uninstallable through the store — the way a Blocked/Rejected app
	// (docs/dev/catalog-status.md) is pulled without throwing away its adaptation.
	// Read it through IsListed(), never the raw pointer. Default-true, so every
	// existing manifest stays listed with no change (back-compatible field add).
	Listed *bool `yaml:"listed,omitempty"`

	ComposeFile    string      `yaml:"compose_file"`
	MainService    string      `yaml:"main_service"`
	MainPort       int         `yaml:"main_port"`
	PreferredSlugs []string    `yaml:"preferred_slugs"`
	Permissions    Permissions `yaml:"permissions"`

	// Images is the optional catalog-promised image map (APP_STORE.md # Catalog
	// schema — the `images` object). Keyed by the exact `image:` reference used
	// in the compose (e.g. `traefik/whoami:v1.10.3`); the value carries the
	// pinned `sha256:…` digest plus the display-only download/disk sizes the
	// store renders before install. CI (`molma manifest resolve`) resolves all
	// three from the registry at catalog-build time. Absent ⇒ TOFU at install
	// (Door-2 always, Door-1 until the catalog publishes a digest).
	Images map[string]ImageRef `yaml:"images,omitempty"`

	// Storage holds the author's on-disk hints (APP_MANIFEST.md # Storage). v1
	// reads only estimated_size — hoisted verbatim into Footprint; any other
	// keys (e.g. data_volumes) live in the compose, not parsed here.
	Storage Storage `yaml:"storage,omitempty"`

	// HealthProbe is the optional "up but not responding" probe config
	// (APP_MANIFEST.md # B). nil ⇒ the app is never probed and the
	// app-unresponsive health issue is never raised for it. Door-2 synthetic
	// manifests omit it.
	HealthProbe *HealthProbe `yaml:"health_probe,omitempty"`

	// ServiceUser opts a folderless app into a dedicated, molma-allocated
	// non-root runtime identity instead of the folderless default (the brain's
	// euid — root in production). Boolean intent only: no UID is namable in a
	// manifest (APP_MANIFEST.md # B, APP_ISOLATION.md # Runtime identity & data
	// ownership). Meaningless with a folders grant — admission rejects the
	// combination. Door-2 synthetic manifests never set it.
	ServiceUser bool `yaml:"service_user,omitempty"`

	// Secrets declares per-app random secrets the brain generates once at install
	// and injects as `MOLMA_SECRET_<NAME>` env vars (APP_MANIFEST.md # secrets,
	// SERVICE_PROVISIONING.md # Env-var injection). Each name maps in the compose
	// to whatever the app actually expects (e.g. BETTER_AUTH_SECRET) — same
	// app-defined mapping convention as MOLMA_SERVICE_*. The value is generated
	// from a CSPRNG, persisted, and re-emitted stably across restarts so
	// token-signing secrets don't rotate underneath live sessions.
	Secrets []Secret `yaml:"secrets,omitempty"`

	// Services declares the managed data services the app consumes (APP_MANIFEST.md
	// # D, SERVICE_PROVISIONING.md # Tier 1). The map key is the app's logical name
	// for the dependency (`database`, `cache`); the brain provisions a per-app
	// database+role in the shared instance of that type+version and injects the
	// credentials as `MOLMA_SERVICE_<KEY>_*` (uppercased key). The app's compose
	// maps those to whatever it expects — same app-defined convention as
	// MOLMA_SECRET_*. Absent ⇒ the app brings its own datastore in its compose.
	Services map[string]ServiceDep `yaml:"services,omitempty"`
}

// Description holds the app's catalog-facing text (APP_MANIFEST.md # A).
// Both fields are optional; the store surfaces Short as the one-liner and Long
// as the markdown body on the app detail page.
type Description struct {
	Short string `yaml:"short,omitempty"`
	// Long is a markdown string rendered on the app-store detail page. Multi-line
	// literal blocks (`|`) are idiomatic in manifests.
	Long string `yaml:"long,omitempty"`
}

// Author is the app's publisher, shown on the detail page (APP_MANIFEST.md # A).
type Author struct {
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	URL  string `yaml:"url,omitempty" json:"url,omitempty"`
}

// Links are the author's outbound links, surfaced in the detail page's info
// panel (APP_MANIFEST.md # A). All optional.
type Links struct {
	Homepage string `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	Source   string `yaml:"source,omitempty" json:"source,omitempty"`
	Support  string `yaml:"support,omitempty" json:"support,omitempty"`
}

// ImageRef is one entry in the catalog's `images` map (APP_STORE.md # Catalog
// schema): the pinned digest plus the display-only on-disk footprint of a
// single `image:tag`. Digest is the binding the brain enforces at install
// (# Trust model); DownloadBytes (sum of the image's compressed layer sizes)
// and DiskBytes (sum of its uncompressed layer sizes, deduped within the app's
// own image set) are advisory and gate nothing — a size that drifts from
// reality is a cosmetic bug, not an integrity failure.
type ImageRef struct {
	Digest        string `yaml:"digest" json:"digest"`
	DownloadBytes int64  `yaml:"download_bytes,omitempty" json:"download_bytes,omitempty"`
	DiskBytes     int64  `yaml:"disk_bytes,omitempty" json:"disk_bytes,omitempty"`
}

// UnmarshalYAML accepts both the object form ({digest, download_bytes,
// disk_bytes}) and the legacy scalar shorthand (`image:tag: sha256:…`, digest
// only) so manifests written before sizes were resolved still parse. Mirrors
// the HealthProbe string-or-mapping pattern.
func (r *ImageRef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&r.Digest)
	}
	type raw ImageRef // shed the method set to avoid recursing into this func
	var w raw
	if err := node.Decode(&w); err != nil {
		return fmt.Errorf("images: %w", err)
	}
	*r = ImageRef(w)
	return nil
}

// Storage is the author's on-disk hint block (APP_MANIFEST.md # Storage). v1
// parses only estimated_size — a human-readable size string ("10GB") hoisted
// verbatim into the catalog footprint; the brain does no unit math on it there.
type Storage struct {
	EstimatedSize string `yaml:"estimated_size,omitempty" json:"estimated_size,omitempty"`
}

// EstimatedSizeBytes parses storage.estimated_size into a byte count for the
// box-specific install-plan footprint (BRAIN_UI_PROTOCOL.md # install-plan).
// The catalog grid keeps the verbatim string (Footprint.EstimatedState); this
// is the numeric form the install dialog adds to the image bytes.
//
// The three returns disambiguate cases the caller treats differently:
//   - unset ("")        → (0, false, nil)  — author gave no estimate; omit it
//   - valid ("10GB")    → (n, true,  nil)
//   - malformed ("big") → (0, false, err)  — surfaced, never silently zeroed
//
// Units are binary (GB = 2³⁰, matching the spec example where "10GB" is
// 10737418240), case-insensitive, with an optional fractional mantissa
// ("1.5GB"); the result truncates to whole bytes.
func (s Storage) EstimatedSizeBytes() (int64, bool, error) {
	raw := strings.TrimSpace(s.EstimatedSize)
	if raw == "" {
		return 0, false, nil
	}
	n, err := parseBinarySize(raw)
	if err != nil {
		return 0, false, err
	}
	return n, true, nil
}

// sizeUnits maps a case-folded size suffix to its binary multiplier. Empty and
// "b" are bytes; the k/m/g/t families are all powers of 1024 — the bare,
// two-letter, and explicit -ib spellings are accepted as the same value because
// authors write them interchangeably and the figure is advisory either way.
var sizeUnits = map[string]int64{
	"": 1, "b": 1,
	"k": 1 << 10, "kb": 1 << 10, "kib": 1 << 10,
	"m": 1 << 20, "mb": 1 << 20, "mib": 1 << 20,
	"g": 1 << 30, "gb": 1 << 30, "gib": 1 << 30,
	"t": 1 << 40, "tb": 1 << 40, "tib": 1 << 40,
}

// sizeRe splits a size string into a numeric mantissa and an optional unit
// suffix, tolerating whitespace between them ("10 GB").
var sizeRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z]*)$`)

// parseBinarySize parses a human size like "10GB" or "1.5 GiB" into bytes using
// the binary multipliers in sizeUnits. The caller passes a non-empty, trimmed
// string.
func parseBinarySize(s string) (int64, error) {
	m := sizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("estimated_size %q is not a valid size (e.g. 10GB, 512MB)", s)
	}
	mult, ok := sizeUnits[strings.ToLower(m[2])]
	if !ok {
		return 0, fmt.Errorf("estimated_size %q has an unknown unit %q", s, m[2])
	}
	mant, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("estimated_size %q: %w", s, err)
	}
	return int64(mant * float64(mult)), nil
}

// Footprint is the per-app on-disk summary the store grid renders without
// fetching the full manifest (APP_STORE.md # Catalog schema — `footprint`). The
// image totals are an upper bound (nothing assumed cached locally); the install
// dialog shows a sharper, box-specific figure (BRAIN_UI_PROTOCOL.md #
// GET /api/v1/catalog/:id/install-plan). EstimatedState is the manifest's
// measured app-state baseline at install, not a usage projection (DECISIONS.md
// 2026-06-09).
type Footprint struct {
	ImageDownloadBytes int64  `json:"image_download_bytes" yaml:"image_download_bytes"`
	ImageDiskBytes     int64  `json:"image_disk_bytes" yaml:"image_disk_bytes"`
	EstimatedState     string `json:"estimated_state,omitempty" yaml:"estimated_state,omitempty"`
}

// Footprint derives the per-app footprint (APP_STORE.md # Catalog schema): it
// sums the resolved Images entries and hoists storage.estimated_size verbatim.
// Derived, never hand-authored. Summing per-image DiskBytes trusts the
// resolver's within-app layer dedup when it filled those numbers.
func (m *Manifest) Footprint() Footprint {
	f := Footprint{EstimatedState: m.Storage.EstimatedSize}
	for _, ref := range m.Images {
		f.ImageDownloadBytes += ref.DownloadBytes
		f.ImageDiskBytes += ref.DiskBytes
	}
	return f
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

// Secret is one declared per-app generated secret (APP_MANIFEST.md # secrets).
// Name is lowercase snake_case; the injected env var is `MOLMA_SECRET_` + the
// uppercased name (so `auth` → `MOLMA_SECRET_AUTH`). Bytes is the entropy drawn
// from the CSPRNG before base64url encoding; it defaults to DefaultSecretBytes
// and is floored at MinSecretBytes so a manifest can't request a weak secret.
type Secret struct {
	Name  string `yaml:"name"`
	Bytes int    `yaml:"bytes,omitempty"`
}

// ServiceDep is one managed-service dependency (APP_MANIFEST.md # D). Type is
// the service kind (`postgres`, `mysql`, `mariadb`, `redis`); Version is the
// version pin the brain runs a shared instance of (a major for postgres/redis,
// an upstream LTS series for the MySQL family). Name is the author's advisory
// logical name for the resource; v1 ignores it (the brain generates the real
// database name) — parsed for forward-compat, not used.
type ServiceDep struct {
	Type    string `yaml:"type"`
	Version string `yaml:"version"`
	Name    string `yaml:"name,omitempty"`
}

// serviceVersions is the allowlist of versions per managed-service type
// (SERVICE_PROVISIONING.md # Catalog (v1)). A manifest declaring a type/version
// outside this set is rejected at parse time. Note: schema-valid is not the
// same as provisioned — v1 provisions postgres and the MySQL family; a redis
// declaration parses but install fails until Redis provisioning lands
// (NEXT.md). The MySQL-family entries are the upstream LTS series; mysql 8.0
// is past Oracle EOL but kept because Ghost pins it specifically.
var serviceVersions = map[string]map[string]bool{
	"postgres": {"15": true, "16": true},
	"redis":    {"7": true},
	"mysql":    {"8.0": true, "8.4": true},
	"mariadb":  {"10.11": true, "11.4": true},
}

// DefaultSecretBytes is the entropy drawn for a declared secret when `bytes` is
// omitted: 32 raw bytes → 43 base64url chars, comfortably past the "32+ char"
// floor most token-signing libraries (Better Auth, Rails secret_key_base) want.
const DefaultSecretBytes = 32

// MinSecretBytes is the floor on a declared secret's entropy. 16 bytes (128
// bits) is the minimum we'll generate even if a manifest asks for less.
const MinSecretBytes = 16

// secretName matches lowercase snake_case so the uppercased form is a valid,
// unambiguous environment-variable suffix: `auth`, `session_key` ok; `Auth`,
// `2fa`, `a-b` rejected.
var secretName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// folderTaxonomy is the fixed v1 use-case folder set (APP_ISOLATION.md # User
// content). User-defined folders are deferred.
var folderTaxonomy = map[string]bool{
	"photos": true, "documents": true, "movies": true,
	"music": true, "notes": true, "downloads": true,
}

// IsListed reports whether the app is visible/installable through the store.
// Absent (`listed:` omitted) defaults to true, so the catalog hides an app only
// when a manifest explicitly sets `listed: false` (APP_MANIFEST.md # A).
func (m *Manifest) IsListed() bool { return m.Listed == nil || *m.Listed }

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
	// icon_glyph is a Lucide name; same kebab-case shape as a slug. We can only
	// check the shape (the 1700+ name set lives in the UI), so a typo'd-but-valid
	// name passes here and falls back to the generic glyph client-side.
	if m.IconGlyph != "" && !kebabSlug.MatchString(m.IconGlyph) {
		return fmt.Errorf("icon_glyph %q must be a lucide icon name in kebab-case", m.IconGlyph)
	}
	if err := m.validateHealthProbe(); err != nil {
		return err
	}
	if err := m.validateSecrets(); err != nil {
		return err
	}
	if err := m.validateServices(); err != nil {
		return err
	}
	return ValidatePermissions(&m.Permissions)
}

// validateSecrets checks declared secret names and normalizes the byte length
// in place (APP_MANIFEST.md # secrets). Names must be snake_case and unique; a
// requested length below MinSecretBytes is raised to it, and an omitted length
// defaults to DefaultSecretBytes. Absent ⇒ no-op.
func (m *Manifest) validateSecrets() error {
	seen := make(map[string]bool, len(m.Secrets))
	for i := range m.Secrets {
		s := &m.Secrets[i]
		if !secretName.MatchString(s.Name) {
			return fmt.Errorf("secrets: name %q must be snake_case (lowercase, starting with a letter)", s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("secrets: duplicate name %q", s.Name)
		}
		seen[s.Name] = true
		if s.Bytes == 0 {
			s.Bytes = DefaultSecretBytes
		} else if s.Bytes < MinSecretBytes {
			s.Bytes = MinSecretBytes
		}
	}
	return nil
}

// validateServices checks each managed-service declaration (APP_MANIFEST.md #
// D). The map key must be snake_case (it becomes the uppercased env-var suffix
// `MOLMA_SERVICE_<KEY>_*`, so the same rule as a secret name); the type must be
// a known managed kind and the version one this build runs for that kind. Absent
// ⇒ no-op.
func (m *Manifest) validateServices() error {
	for key, dep := range m.Services {
		if !secretName.MatchString(key) {
			return fmt.Errorf("services: key %q must be snake_case (lowercase, starting with a letter)", key)
		}
		versions, ok := serviceVersions[dep.Type]
		if !ok {
			return fmt.Errorf("services[%s]: unknown type %q (allowed: postgres, redis, mysql, mariadb)", key, dep.Type)
		}
		if dep.Version == "" {
			return fmt.Errorf("services[%s]: version is required", key)
		}
		if !versions[dep.Version] {
			return fmt.Errorf("services[%s]: unsupported %s version %q", key, dep.Type, dep.Version)
		}
	}
	return nil
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
