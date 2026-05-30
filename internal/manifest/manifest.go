// Package manifest parses and validates manifest.yml (APP_MANIFEST.md).
// v1 skeleton: the required fields plus the permission flags the override
// generator currently consumes. The compose file itself is held verbatim and
// validated separately by the admission policy (APP_LIFECYCLE.md).
package manifest

import (
	"fmt"
	"regexp"

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
	return nil
}

// kebabSlug matches lowercase alphanumeric labels joined by single hyphens:
// `home-assistant` ok; `whoami-`, `-x`, `a--b`, `xn--y`, `Foo` rejected.
var kebabSlug = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
