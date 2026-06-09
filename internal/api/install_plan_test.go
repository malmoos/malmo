package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// writeManifestFixture writes manifest.yml + a minimal compose.yml into
// <catalogDir>/<id>/ so the catalog can load the app.
func writeManifestFixture(t *testing.T, catalogDir, id, yml string) {
	t.Helper()
	dir := filepath.Join(catalogDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	// Tests reference compose_file: compose.yml in every fixture manifest.
	compose := []byte("services:\n  app:\n    image: test\n")
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), compose, 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.yml"), []byte(yml), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

const minimalManifestYML = `id: whoami
manifest_version: 1
name: Whoami
version: "1.0"
compose_file: compose.yml
main_service: app
main_port: 80
`

const foldersManifestYML = `id: jellyfin
manifest_version: 1
name: Jellyfin
version: "10.9.6"
compose_file: compose.yml
main_service: jellyfin
main_port: 8096
permissions:
  internet: true
  lan: true
  gpu: true
  devices:
    - /dev/dri/renderD128
  folders:
    - folder: movies
      mode: write
      scope: pick-subfolder
      default: Movies/Family
    - folder: music
      mode: read
      scope: whole
`

const footprintManifestYML = `id: sizer
manifest_version: 1
name: Sizer
version: "2.0"
compose_file: compose.yml
main_service: app
main_port: 80
storage:
  estimated_size: 10GB
`

// --- tests ---------------------------------------------------------------

// TestInstallPlan_Footprint asserts the box-specific footprint block: the parsed
// estimated_size and the host's free figure flow through. The fixture declares no
// images (the harness wires a nil Docker driver), so the incremental image bytes
// are zero — image subtraction is covered by the lifecycle unit tests.
func TestInstallPlan_Footprint(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "sizer", footprintManifestYML)
	h.setupAdmin("alice", "pass1")

	resp := h.do("GET", "/api/v1/catalog/sizer/install-plan", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	fp := decodeJSON[InstallPlanDTO](t, resp).Footprint

	if fp.DownloadBytes != 0 || fp.ImageDiskBytes != 0 {
		t.Errorf("no-image manifest: want zero image bytes, got %d/%d", fp.DownloadBytes, fp.ImageDiskBytes)
	}
	if fp.EstimatedStateBytes == nil || *fp.EstimatedStateBytes != 10<<30 {
		t.Errorf("estimated_state_bytes: want %d, got %v", int64(10<<30), fp.EstimatedStateBytes)
	}
	if fp.FreeBytes != harnessFreeBytes {
		t.Errorf("free_bytes: want %d, got %d", int64(harnessFreeBytes), fp.FreeBytes)
	}
}

// TestInstallPlan_FootprintOmitsUnsetEstimate: a manifest with no
// storage.estimated_size omits estimated_state_bytes (nil pointer) rather than
// reporting a misleading 0.
func TestInstallPlan_FootprintOmitsUnsetEstimate(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "whoami", minimalManifestYML)
	h.setupAdmin("alice", "pass1")

	resp := h.do("GET", "/api/v1/catalog/whoami/install-plan", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	fp := decodeJSON[InstallPlanDTO](t, resp).Footprint
	if fp.EstimatedStateBytes != nil {
		t.Errorf("want estimated_state_bytes omitted, got %d", *fp.EstimatedStateBytes)
	}
	if fp.FreeBytes != harnessFreeBytes {
		t.Errorf("free_bytes: want %d, got %d", int64(harnessFreeBytes), fp.FreeBytes)
	}
}

func TestInstallPlan_RequiresAuth(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "whoami", minimalManifestYML)
	// No session — fresh jar.
	jar, _ := newJar()
	h.jar = jar

	resp := h.do("GET", "/api/v1/catalog/whoami/install-plan", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestInstallPlan_UnknownID(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")

	resp := h.do("GET", "/api/v1/catalog/does-not-exist/install-plan", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown id: want 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// A catalog entry that exists but fails to parse is an integrity problem, not
// a missing app — it must surface as 500, not collapse into the 404 "no such
// app" path. Guards the catalog.ErrNotFound discrimination in the handler.
func TestInstallPlan_MalformedManifest(t *testing.T) {
	h := newHarness(t)
	// manifest.yml present but missing the required main_port → Parse fails with
	// a non-ErrNotFound error.
	const brokenYML = `id: broken
manifest_version: 1
name: Broken
version: "1.0"
compose_file: compose.yml
main_service: app
`
	writeManifestFixture(t, h.catalogDir, "broken", brokenYML)
	h.setupAdmin("alice", "pass1")

	resp := h.do("GET", "/api/v1/catalog/broken/install-plan", nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("malformed manifest: want 500, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestInstallPlan_AdminScopeMenu(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "whoami", minimalManifestYML)
	h.setupAdmin("alice", "pass1")

	resp := h.do("GET", "/api/v1/catalog/whoami/install-plan", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin: want 200, got %d", resp.StatusCode)
	}
	body := decodeJSON[InstallPlanDTO](t, resp)

	if len(body.ScopeOptions) != 2 || body.ScopeOptions[0] != "household" || body.ScopeOptions[1] != "personal" {
		t.Errorf("admin scope_options: want [household personal], got %v", body.ScopeOptions)
	}
	if body.ScopeDefault != "household" {
		t.Errorf("admin scope_default: want household, got %q", body.ScopeDefault)
	}
}

func TestInstallPlan_MemberScopeMenu(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "whoami", minimalManifestYML)
	h.setupAdmin("alice", "pass1")
	h.addMember("u_bob", "bob", "bobpass")
	h.loginAs("bob", "bobpass")

	resp := h.do("GET", "/api/v1/catalog/whoami/install-plan", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member: want 200, got %d", resp.StatusCode)
	}
	body := decodeJSON[InstallPlanDTO](t, resp)

	if len(body.ScopeOptions) != 1 || body.ScopeOptions[0] != "personal" {
		t.Errorf("member scope_options: want [personal], got %v", body.ScopeOptions)
	}
	if body.ScopeDefault != "personal" {
		t.Errorf("member scope_default: want personal, got %q", body.ScopeDefault)
	}
}

func TestInstallPlan_NoFolders(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "whoami", minimalManifestYML)
	h.setupAdmin("alice", "pass1")

	resp := h.do("GET", "/api/v1/catalog/whoami/install-plan", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	body := decodeJSON[InstallPlanDTO](t, resp)

	if body.ManifestID != "whoami" {
		t.Errorf("manifest_id: want whoami, got %q", body.ManifestID)
	}
	if body.Permissions.Internet || body.Permissions.LAN || body.Permissions.GPU {
		t.Errorf("flags: want all false, got internet=%v lan=%v gpu=%v",
			body.Permissions.Internet, body.Permissions.LAN, body.Permissions.GPU)
	}
	if len(body.Permissions.Devices) != 0 {
		t.Errorf("devices: want empty, got %v", body.Permissions.Devices)
	}
	if len(body.Permissions.Folders) != 0 {
		t.Errorf("folders: want empty, got %v", body.Permissions.Folders)
	}
}

func TestInstallPlan_FoldersAndPermissions(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "jellyfin", foldersManifestYML)
	h.setupAdmin("alice", "pass1")

	resp := h.do("GET", "/api/v1/catalog/jellyfin/install-plan", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	body := decodeJSON[InstallPlanDTO](t, resp)

	if body.ManifestID != "jellyfin" || body.Name != "Jellyfin" || body.Version != "10.9.6" {
		t.Errorf("identity fields: %+v", body)
	}

	p := body.Permissions
	if !p.Internet {
		t.Error("internet: want true")
	}
	if !p.LAN {
		t.Error("lan: want true")
	}
	if !p.GPU {
		t.Error("gpu: want true")
	}
	if len(p.Devices) != 1 || p.Devices[0] != "/dev/dri/renderD128" {
		t.Errorf("devices: want [/dev/dri/renderD128], got %v", p.Devices)
	}

	if len(p.Folders) != 2 {
		t.Fatalf("folders: want 2, got %d", len(p.Folders))
	}

	// First folder: movies, write, pick-subfolder, default Movies/Family.
	movies := p.Folders[0]
	if movies.Folder != "movies" {
		t.Errorf("folder[0].folder: want movies, got %q", movies.Folder)
	}
	if movies.Mode != "write" {
		t.Errorf("folder[0].mode: want write, got %q", movies.Mode)
	}
	if movies.Scope != "pick-subfolder" {
		t.Errorf("folder[0].scope: want pick-subfolder, got %q", movies.Scope)
	}
	if movies.SubfolderDefault != "Movies/Family" {
		t.Errorf("folder[0].subfolder_default: want Movies/Family, got %q", movies.SubfolderDefault)
	}

	// Household source: options=[shared], default=shared.
	hh := movies.Sources.Household
	if len(hh.Options) != 1 || hh.Options[0] != "shared" {
		t.Errorf("movies.sources.household.options: want [shared], got %v", hh.Options)
	}
	if hh.Default != "shared" {
		t.Errorf("movies.sources.household.default: want shared, got %q", hh.Default)
	}

	// Personal source: options=[personal, shared], default=personal.
	pe := movies.Sources.Personal
	if len(pe.Options) != 2 || pe.Options[0] != "personal" || pe.Options[1] != "shared" {
		t.Errorf("movies.sources.personal.options: want [personal shared], got %v", pe.Options)
	}
	if pe.Default != "personal" {
		t.Errorf("movies.sources.personal.default: want personal, got %q", pe.Default)
	}

	// Second folder: music, read, whole, no subfolder_default.
	music := p.Folders[1]
	if music.Folder != "music" {
		t.Errorf("folder[1].folder: want music, got %q", music.Folder)
	}
	if music.Mode != "read" {
		t.Errorf("folder[1].mode: want read, got %q", music.Mode)
	}
	if music.Scope != "whole" {
		t.Errorf("folder[1].scope: want whole, got %q", music.Scope)
	}
	if music.SubfolderDefault != "" {
		t.Errorf("folder[1].subfolder_default: want empty, got %q", music.SubfolderDefault)
	}
}

// TestInstallPlan_MemberFolderSourcesStillPresent checks that even a member
// gets the household source menu populated (unreachable from the scope picker
// but keeps the folder shape uniform regardless of caller role).
func TestInstallPlan_MemberFolderSourcesStillPresent(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "jellyfin", foldersManifestYML)
	h.setupAdmin("alice", "pass1")
	h.addMember("u_bob", "bob", "bobpass")
	h.loginAs("bob", "bobpass")

	resp := h.do("GET", "/api/v1/catalog/jellyfin/install-plan", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	body := decodeJSON[InstallPlanDTO](t, resp)

	if len(body.Permissions.Folders) == 0 {
		t.Fatal("want folders, got none")
	}
	hh := body.Permissions.Folders[0].Sources.Household
	if len(hh.Options) == 0 {
		t.Error("member: household source menu must still be populated")
	}
	if hh.Options[0] != "shared" || hh.Default != "shared" {
		t.Errorf("member household source: want {[shared] shared}, got %+v", hh)
	}
}
