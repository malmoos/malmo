package lifecycle

// Folder-enforcement scenarios: writeOverride/writeEnv stamping user:, bind
// mounts from the elected source, group_add for shared sources, device
// passthrough, and MOLMA_FOLDER_* injection (APP_ISOLATION.md # User content).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/molmaos/molma/internal/hostclient"
	"github.com/molmaos/molma/internal/manifest"
	"github.com/molmaos/molma/internal/store"
	"gopkg.in/yaml.v3"
)

// foldersManifest builds a single-folder Door-1 manifest. mode/scope let a test
// exercise :ro vs :rw and pick-subfolder.
func foldersManifest(mode, scope string) string {
	man := `
id: filesapp
manifest_version: 1
name: Files App
version: "1.0"
compose_file: compose.yml
main_service: app
main_port: 8080
preferred_slugs: [filesapp]
permissions:
  internet: false
  lan: false
  folders:
    - folder: documents
      mode: ` + mode + `
      scope: ` + scope + `
`
	return man
}

const foldersCompose = `
services:
  app:
    image: traefik/whoami:v1.10.3
`

// installFolders writes the filesapp catalog entry, scripts the image digest,
// and installs it with the given scope + elected mounts. It returns the parsed
// override service entry for "app" and the raw .env contents.
func installFolders(t *testing.T, e *testEnv, scope string, owner Owner, manYAML string, mounts []FolderMount) (map[string]any, string) {
	t.Helper()
	e.writeCatalogApp(t, "filesapp", foldersCompose, manYAML)
	e.docker.digests[testImage] = testDigest

	inst, err := e.m.Install(context.Background(), "filesapp", owner, scope, mounts, nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	dir := filepath.Join(e.stateDir, "instances", inst.ID)
	ov, err := os.ReadFile(filepath.Join(dir, "compose.override.yml"))
	if err != nil {
		t.Fatalf("read override: %v", err)
	}
	var doc struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(ov, &doc); err != nil {
		t.Fatalf("parse override: %v", err)
	}
	env, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	return doc.Services["app"], string(env)
}

func TestInstallFolders_HouseholdSharedWrite(t *testing.T) {
	e := newTestEnv(t)
	owner := Owner{UserID: "u_admin", Username: "admin"}
	app, env := installFolders(t, e, store.ScopeHousehold, owner,
		foldersManifest("write", "whole"),
		[]FolderMount{{Folder: "documents", Source: sourceShared}})

	// Household instances run as the molma-app service identity (fake: 2000).
	if got := app["user"]; got != "2000:2000" {
		t.Errorf("user: want 2000:2000, got %v", got)
	}
	wantVol := "/srv/molma/shared/Documents:/molma/documents:rw" // write → :rw
	if !hasString(app["volumes"], wantVol) {
		t.Errorf("volumes: want %q, got %v", wantVol, app["volumes"])
	}
	// Shared source → group_add the molma-shared GID (fake: 2001).
	if !hasString(app["group_add"], "2001") {
		t.Errorf("group_add: want 2001, got %v", app["group_add"])
	}
	if !strings.Contains(env, "MOLMA_FOLDER_DOCUMENTS=/molma/documents") {
		t.Errorf("env missing MOLMA_FOLDER_DOCUMENTS, got:\n%s", env)
	}
}

func TestInstallFolders_PersonalSourceReadWithSubfolder(t *testing.T) {
	e := newTestEnv(t)
	owner := Owner{UserID: "u_alex", Username: "alex"}
	app, _ := installFolders(t, e, store.ScopePersonal, owner,
		foldersManifest("read", "pick-subfolder"),
		[]FolderMount{{Folder: "documents", Source: sourcePersonal, Subfolder: "Work"}})

	// Personal instance runs as the owner's UID/GID (fake ResolveHome: 3000).
	if got := app["user"]; got != "3000:3000" {
		t.Errorf("user: want 3000:3000, got %v", got)
	}
	wantVol := "/home/alex/Documents/Work:/molma/documents:ro" // read → :ro, subfolder narrows source
	if !hasString(app["volumes"], wantVol) {
		t.Errorf("volumes: want %q, got %v", wantVol, app["volumes"])
	}
	// No shared source → no group_add.
	if _, ok := app["group_add"]; ok {
		t.Errorf("group_add: want absent for personal source, got %v", app["group_add"])
	}
}

func TestInstallFolders_DeletedOwnerRollsBack(t *testing.T) {
	e := newTestEnv(t)
	e.host.resolveHomeErr = hostclient.ErrUnknownUser
	e.writeCatalogApp(t, "filesapp", foldersCompose, foldersManifest("read", "whole"))
	e.docker.digests[testImage] = testDigest

	_, err := e.m.Install(context.Background(), "filesapp",
		Owner{UserID: "u_ghost", Username: "ghost"}, store.ScopePersonal,
		[]FolderMount{{Folder: "documents", Source: sourcePersonal}}, nil)
	if err == nil {
		t.Fatal("want install error for deleted owner, got nil")
	}
	// The brain row must be rolled back (brain-commits-first invariant).
	if insts, _ := e.store.List(); len(insts) != 0 {
		t.Errorf("want no instance rows after rollback, got %d", len(insts))
	}
}

func TestInstallFolders_FolderlessRunsAsBrainIdentity(t *testing.T) {
	// An app with no declared folders still runs as a resolved user: — the
	// brain's own euid/egid, which owns the ./data bind it created — so a
	// cap_drop:ALL container can write its private state. It binds no folders and
	// makes no host identity calls (the brain identity is local).
	e := newTestEnv(t)
	e.writeCatalogApp(t, "whoami", whoamiCompose, whoamiManifest(testDigest))
	e.docker.digests[testImage] = testDigest

	inst, err := e.m.Install(context.Background(), "whoami",
		Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	ov, _ := os.ReadFile(filepath.Join(e.stateDir, "instances", inst.ID, "compose.override.yml"))
	wantUser := fmt.Sprintf("user: %d:%d", os.Geteuid(), os.Getegid())
	if !strings.Contains(string(ov), wantUser) {
		t.Errorf("folderless override must run as brain identity %q, got:\n%s", wantUser, ov)
	}
	if strings.Contains(string(ov), "volumes:") || strings.Contains(string(ov), "group_add:") {
		t.Errorf("folderless override must bind no folders, got:\n%s", ov)
	}
	if e.host.called("WellKnownIdentity") || e.host.called("ResolveHome") {
		t.Error("folderless install must not resolve host identity")
	}
}

// TestInstallCustomFolders_TargetDestination exercises the Door-2 divergence: a
// custom app's folder grant carries an explicit in-container target, so the bind
// lands there (not the fixed /molma/<folder>) and MOLMA_FOLDER_* reflects it. The
// source is scope-derived — no per-folder election — so a household install reads
// the shared tree (DASHBOARD.md # Folder grants carry an explicit destination).
func TestInstallCustomFolders_TargetDestination(t *testing.T) {
	e := newTestEnv(t)
	e.docker.digests[testImage] = testDigest

	inst, err := e.m.InstallCustom(context.Background(), CustomSpec{
		Name: "Photo App", Compose: foldersCompose, MainPort: 8080,
		Permissions: manifest.Permissions{
			Internet: true,
			Folders: []manifest.Folder{
				{Folder: "documents", Mode: "write", Target: "/photoprism/originals"},
			},
		},
	}, Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	dir := filepath.Join(e.stateDir, "instances", inst.ID)
	ov, err := os.ReadFile(filepath.Join(dir, "compose.override.yml"))
	if err != nil {
		t.Fatalf("read override: %v", err)
	}
	var doc struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(ov, &doc); err != nil {
		t.Fatalf("parse override: %v", err)
	}
	app := doc.Services["app"]

	// Household → shared source; write → :rw; bind lands at the typed target.
	wantVol := "/srv/molma/shared/Documents:/photoprism/originals:rw"
	if !hasString(app["volumes"], wantVol) {
		t.Errorf("volumes: want %q, got %v", wantVol, app["volumes"])
	}
	env, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if !strings.Contains(string(env), "MOLMA_FOLDER_DOCUMENTS=/photoprism/originals") {
		t.Errorf("env should map MOLMA_FOLDER_DOCUMENTS to the target, got:\n%s", env)
	}
}

// --- helpers ---

// hasString reports whether v is a []string (or []any of strings) containing s.
func hasString(v any, s string) bool {
	switch xs := v.(type) {
	case []string:
		for _, x := range xs {
			if x == s {
				return true
			}
		}
	case []any:
		for _, x := range xs {
			if str, ok := x.(string); ok && str == s {
				return true
			}
		}
	}
	return false
}
