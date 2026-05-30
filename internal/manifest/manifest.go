// Package manifest parses and validates manifest.yml (APP_MANIFEST.md).
// v1 skeleton: the required fields plus the permission flags the override
// generator currently consumes. The compose file itself is held verbatim and
// validated separately by the admission policy (APP_LIFECYCLE.md).
package manifest

import (
	"fmt"
	"regexp"
	"strings"

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

	// Images is the optional catalog-promised image→sha256 map
	// (APP_STORE.md # Trust model — catalog's `images` map). Keyed by the exact
	// `image:` reference used in the compose (e.g. `traefik/whoami:v1.10.3`),
	// value is the `sha256:…` digest CI resolved at catalog-build time.
	// Absent ⇒ TOFU at install (Door-2 always, Door-1 until the catalog
	// publishes a digest).
	Images map[string]string `yaml:"images,omitempty"`
}

type Permissions struct {
	Internet bool `yaml:"internet"`
	LAN      bool `yaml:"lan"`

	// Folders is the app's declared access to use-case content folders
	// (APP_MANIFEST.md # folders). The manifest declares *what* content the app
	// touches (folder + mode + subfolder granularity); it does NOT declare the
	// source. Source — the owner's personal `~/<Folder>/` vs the household
	// `/srv/malmo/shared/<Folder>/` — is the installer's per-folder election at
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
	return m.validatePermissions()
}

// validatePermissions normalizes folder defaults (mode=read, scope=whole) in
// place and rejects unknown folder names, modes, or scopes. It deliberately
// validates nothing about *source* — personal vs shared is resolved by the
// installer at install time, not declared here (APP_MANIFEST.md # folders).
func (m *Manifest) validatePermissions() error {
	for i := range m.Permissions.Folders {
		f := &m.Permissions.Folders[i]
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
	}
	return nil
}

// kebabSlug matches lowercase alphanumeric labels joined by single hyphens:
// `home-assistant` ok; `whoami-`, `-x`, `a--b`, `xn--y`, `Foo` rejected.
var kebabSlug = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
