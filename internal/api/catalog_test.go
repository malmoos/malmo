package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/molmaos/molma/internal/catalog"
)

// richManifestYML is a metadata-complete catalog fixture: icon, screenshots, and
// the author/license/links block the detail page renders.
const richManifestYML = `id: rich
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
compose_file: compose.yml
main_service: app
main_port: 80
`

// writeAssetFixture drops raw bytes at <catalogDir>/<id>/<rel> for asset-route
// tests (writeManifestFixture writes the manifest + compose, not the images).
func writeAssetFixture(t *testing.T, catalogDir, id, rel string, body []byte) {
	t.Helper()
	full := filepath.Join(catalogDir, id, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGetCatalogApp: the detail endpoint returns the full view — embedded Entry
// fields plus long body, screenshot URLs in manifest order, and author metadata.
func TestGetCatalogApp(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "rich", richManifestYML)
	h.setupAdmin("alice", "pass1")

	resp := h.do("GET", "/api/v1/catalog/rich", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	d := decodeJSON[catalog.Detail](t, resp)
	if d.ID != "rich" || d.ShortDescription != "A one-line tagline." || d.IconURL != "/api/v1/catalog/rich/icon" {
		t.Errorf("entry fields: %+v", d.Entry)
	}
	if d.LongDescription == "" {
		t.Error("long description missing")
	}
	if len(d.ScreenshotURLs) != 2 || d.ScreenshotURLs[0] != "/api/v1/catalog/rich/screenshots/0" {
		t.Errorf("screenshot urls: %v", d.ScreenshotURLs)
	}
	if d.Author.Name != "Acme" || d.License != "AGPL-3.0" {
		t.Errorf("metadata: %+v", d)
	}
}

func TestGetCatalogApp_UnknownID(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")

	resp := h.do("GET", "/api/v1/catalog/ghost", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestGetCatalogApp_RequiresAuth(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "rich", richManifestYML)
	jar, _ := newJar()
	h.jar = jar

	resp := h.do("GET", "/api/v1/catalog/rich", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCatalogIcon_RequiresAuth(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "rich", richManifestYML)
	writeAssetFixture(t, h.catalogDir, "rich", "icon.png", []byte("x"))
	jar, _ := newJar()
	h.jar = jar

	resp := h.do("GET", "/api/v1/catalog/rich/icon", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCatalogScreenshot_RequiresAuth(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "rich", richManifestYML)
	writeAssetFixture(t, h.catalogDir, "rich", "screenshots/01.png", []byte("x"))
	jar, _ := newJar()
	h.jar = jar

	resp := h.do("GET", "/api/v1/catalog/rich/screenshots/0", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestCatalogIcon: the icon route serves the on-disk bytes.
func TestCatalogIcon(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "rich", richManifestYML)
	writeAssetFixture(t, h.catalogDir, "rich", "icon.png", []byte("ICONBYTES"))
	h.setupAdmin("alice", "pass1")

	resp := h.do("GET", "/api/v1/catalog/rich/icon", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ICONBYTES" {
		t.Errorf("icon body: %q", body)
	}
}

// TestCatalogScreenshot: the screenshot route serves the n-th image by index.
func TestCatalogScreenshot(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "rich", richManifestYML)
	writeAssetFixture(t, h.catalogDir, "rich", "screenshots/02.png", []byte("SHOT2"))
	h.setupAdmin("alice", "pass1")

	resp := h.do("GET", "/api/v1/catalog/rich/screenshots/1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "SHOT2" {
		t.Errorf("screenshot body: %q", body)
	}
}

// TestCatalogAsset_NotFound: a missing asset / bad index / unknown app is a 404,
// and a non-numeric index is rejected before any catalog lookup.
func TestCatalogAsset_NotFound(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "rich", richManifestYML) // declares icon, file absent
	h.setupAdmin("alice", "pass1")

	for _, path := range []string{
		"/api/v1/catalog/rich/icon",            // declared but file missing
		"/api/v1/catalog/ghost/icon",           // unknown app
		"/api/v1/catalog/rich/screenshots/9",   // index out of range
		"/api/v1/catalog/rich/screenshots/abc", // non-numeric index
	} {
		resp := h.do("GET", path, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}
