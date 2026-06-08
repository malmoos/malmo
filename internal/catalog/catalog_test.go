package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeApp drops a manifest.yml + compose.yml under root/<id>/ for a fixture.
func writeApp(t *testing.T, root, id, manifestYML string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.yml"), []byte(manifestYML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("services:\n  web:\n    image: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestListPopulatesFootprint is the #70 surface: a catalog entry carries the
// coarse footprint (summed image bytes + verbatim estimated_state) so the store
// grid renders sizes without a second fetch.
func TestListPopulatesFootprint(t *testing.T) {
	root := t.TempDir()
	writeApp(t, root, "sizer", `id: sizer
manifest_version: 1
name: Sizer
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 80
storage:
  estimated_size: 10GB
images:
  app/one:1:
    digest: sha256:a
    download_bytes: 100
    disk_bytes: 400
  app/two:2:
    digest: sha256:b
    download_bytes: 30
    disk_bytes: 70
`)

	entries, err := New(root).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	f := entries[0].Footprint
	if f.ImageDownloadBytes != 130 || f.ImageDiskBytes != 470 {
		t.Fatalf("footprint image bytes wrong: %+v", f)
	}
	if f.EstimatedState != "10GB" {
		t.Fatalf("estimated_state not carried: %q", f.EstimatedState)
	}
}

// TestListFootprintZeroWhenNoImages is the no-images manifest (whoami-style):
// the footprint is present and zeroed, never absent.
func TestListFootprintZeroWhenNoImages(t *testing.T) {
	root := t.TempDir()
	writeApp(t, root, "bare", `id: bare
manifest_version: 1
name: Bare
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 80
`)

	entries, err := New(root).List()
	if err != nil {
		t.Fatal(err)
	}
	f := entries[0].Footprint
	if f.ImageDownloadBytes != 0 || f.ImageDiskBytes != 0 || f.EstimatedState != "" {
		t.Fatalf("want zero footprint, got %+v", f)
	}
}

// richManifest is a metadata-complete fixture used by the Entry/Detail tests.
const richManifest = `id: rich
manifest_version: 1
name: Rich App
version: "2.4.1"
description:
  short: "A one-line tagline."
  long: |
    # Heading
    A longer markdown body.
icon: ./icon.png
screenshots: [./screenshots/01.png, ./screenshots/02.png]
categories: [media, photos]
author:
  name: Acme
  url: https://acme.example
license: AGPL-3.0
links:
  homepage: https://acme.example
  source: https://github.com/acme/rich
  support: https://docs.acme.example
changelog_url: https://github.com/acme/rich/releases
compose_file: compose.yml
main_service: web
main_port: 80
`

// writeAsset drops a file at root/<id>/<rel> for the asset-serving tests.
func writeAsset(t *testing.T, root, id, rel string) {
	t.Helper()
	full := filepath.Join(root, id, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestListSurfacesMetadata: the grid Entry carries the tagline, categories, and
// an icon URL derived from the manifest — rendered without a second fetch.
func TestListSurfacesMetadata(t *testing.T) {
	root := t.TempDir()
	writeApp(t, root, "rich", richManifest)

	entries, err := New(root).List()
	if err != nil {
		t.Fatal(err)
	}
	e := entries[0]
	if e.ShortDescription != "A one-line tagline." {
		t.Errorf("short description: %q", e.ShortDescription)
	}
	if len(e.Categories) != 2 || e.Categories[0] != "media" {
		t.Errorf("categories: %v", e.Categories)
	}
	if e.IconURL != "/api/v1/catalog/rich/icon" {
		t.Errorf("icon url: %q", e.IconURL)
	}
}

// TestListNoIconURLWhenAbsent: an iconless manifest yields an empty IconURL, so
// the store falls back to a glyph rather than requesting a 404 asset.
func TestListNoIconURLWhenAbsent(t *testing.T) {
	root := t.TempDir()
	writeApp(t, root, "bare", `id: bare
manifest_version: 1
name: Bare
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 80
`)
	entries, err := New(root).List()
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].IconURL != "" {
		t.Errorf("want empty icon url, got %q", entries[0].IconURL)
	}
}

// TestDetail: the detail view carries the long body, screenshot URLs in manifest
// order, and the author/license/links metadata.
func TestDetail(t *testing.T) {
	root := t.TempDir()
	writeApp(t, root, "rich", richManifest)

	d, err := New(root).Detail("rich")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "rich" || d.ShortDescription != "A one-line tagline." {
		t.Errorf("entry fields not embedded: %+v", d.Entry)
	}
	if d.LongDescription == "" {
		t.Error("long description missing")
	}
	want := []string{"/api/v1/catalog/rich/screenshots/0", "/api/v1/catalog/rich/screenshots/1"}
	if len(d.ScreenshotURLs) != 2 || d.ScreenshotURLs[0] != want[0] || d.ScreenshotURLs[1] != want[1] {
		t.Errorf("screenshot urls: %v", d.ScreenshotURLs)
	}
	if d.Author.Name != "Acme" || d.License != "AGPL-3.0" || d.Links.Source != "https://github.com/acme/rich" {
		t.Errorf("metadata: %+v", d)
	}
}

func TestDetailNotFound(t *testing.T) {
	if _, err := New(t.TempDir()).Detail("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestIconAndScreenshotPath: the asset resolvers return the on-disk path when
// the file exists.
func TestIconAndScreenshotPath(t *testing.T) {
	root := t.TempDir()
	writeApp(t, root, "rich", richManifest)
	writeAsset(t, root, "rich", "icon.png")
	writeAsset(t, root, "rich", "screenshots/01.png")
	writeAsset(t, root, "rich", "screenshots/02.png")
	c := New(root)

	icon, err := c.IconPath("rich")
	if err != nil {
		t.Fatal(err)
	}
	if icon != filepath.Join(root, "rich", "icon.png") {
		t.Errorf("icon path: %q", icon)
	}

	shot, err := c.ScreenshotPath("rich", 1)
	if err != nil {
		t.Fatal(err)
	}
	if shot != filepath.Join(root, "rich", "screenshots", "02.png") {
		t.Errorf("screenshot path: %q", shot)
	}
}

// TestAssetPathErrors: missing files, out-of-range indices, iconless manifests,
// and traversal attempts all map to ErrNotFound rather than leaking a path.
func TestAssetPathErrors(t *testing.T) {
	root := t.TempDir()
	writeApp(t, root, "rich", richManifest) // manifest declares icon, but file absent
	writeApp(t, root, "bare", `id: bare
manifest_version: 1
name: Bare
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 80
`)
	// A secret outside any app dir, the target of a traversal attempt.
	if err := os.WriteFile(filepath.Join(root, "secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New(root)

	cases := []struct {
		name string
		fn   func() (string, error)
	}{
		{"icon file missing", func() (string, error) { return c.IconPath("rich") }},
		{"no icon declared", func() (string, error) { return c.IconPath("bare") }},
		{"screenshot out of range", func() (string, error) { return c.ScreenshotPath("rich", 9) }},
		{"negative index", func() (string, error) { return c.ScreenshotPath("rich", -1) }},
		{"unknown app", func() (string, error) { return c.IconPath("ghost") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.fn(); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
		})
	}

	// Path traversal: a manifest whose icon climbs out of the app dir must not
	// resolve to the sibling secret.
	writeApp(t, root, "evil", `id: evil
manifest_version: 1
name: Evil
version: "1.0"
icon: ../secret
compose_file: compose.yml
main_service: web
main_port: 80
`)
	if _, err := c.IconPath("evil"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("traversal not blocked: %v", err)
	}
}
