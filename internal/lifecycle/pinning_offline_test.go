package lifecycle

// Offline (air-gapped) install path — resolveImages' fallback when a baked box
// has no registry to pull from and docker-loaded its images from the offline
// bundle (CONTROL_PLANE.md # First-boot brain bootstrap; APP_LIFECYCLE.md #
// image digest pinning). The fallback is gated on Manager.offlineInstall and
// trusts the catalog-promised digest of a locally-present image.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/malmoos/malmo/internal/store"
	"gopkg.in/yaml.v3"
)

// overridePin reads the image pin compose wrote for the instance's main service.
func overridePin(t *testing.T, stateDir, id, service string) string {
	t.Helper()
	ov, err := os.ReadFile(filepath.Join(stateDir, "instances", id, "compose.override.yml"))
	if err != nil {
		t.Fatalf("read override: %v", err)
	}
	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(ov, &doc); err != nil {
		t.Fatalf("parse override: %v", err)
	}
	return doc.Services[service].Image
}

// Offline + image present locally (docker-loaded, no RepoDigest) + a catalog
// promise → install succeeds and pins the promised digest. This is the M2
// air-gapped whoami install: the pull can't reach a registry, but the bundle
// already loaded the image and the catalog vouches for the digest.
func TestInstallOfflineTrustsPromisedDigest(t *testing.T) {
	e := newTestEnv(t)
	e.m.offlineInstall = true
	e.writeCatalogApp(t, "whoami", whoamiCompose, whoamiManifest(testDigest))
	// No registry: pull fails. The bundle loaded the image (no RepoDigest).
	e.docker.pullErr[testImage] = fmt.Errorf("dial tcp: registry unreachable")
	e.docker.loaded[testImage] = true

	inst, err := e.m.Install(context.Background(), "whoami", Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, "", nil)
	if err != nil {
		t.Fatalf("offline install: %v", err)
	}
	if inst.State != "running" {
		t.Fatalf("end state = %q, want running", inst.State)
	}
	if got, want := overridePin(t, e.stateDir, inst.ID, "whoami"), "traefik/whoami@"+testDigest; got != want {
		t.Fatalf("override pin = %q, want %q (promised digest)", got, want)
	}
	// The pull was attempted (and failed) before the local presence probe.
	want := []string{"Pull", "ImageInspect", "ComposeUp"}
	if !containsInOrder(e.docker.methods(), want) {
		t.Fatalf("driver call order missing %v in %v", want, e.docker.methods())
	}
}

// Offline but the image is NOT present locally → the bundle is incomplete; the
// install hard-fails (the whole point of the air-gapped lane — a missing
// bundled image must fail, not silently pull). Rolls back clean.
func TestInstallOfflineMissingImageFails(t *testing.T) {
	e := newTestEnv(t)
	e.m.offlineInstall = true
	e.writeCatalogApp(t, "whoami", whoamiCompose, whoamiManifest(testDigest))
	e.docker.pullErr[testImage] = fmt.Errorf("dial tcp: registry unreachable")
	// loaded NOT set → ImageInspect errors → genuinely absent.

	_, err := e.m.Install(context.Background(), "whoami", Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, "", nil)
	if err == nil {
		t.Fatalf("want hard-fail when image neither pullable nor present locally")
	}
	if e.docker.called("ComposeUp") {
		t.Fatalf("ComposeUp must not run when the image is absent")
	}
	if list, _ := e.store.List(); len(list) != 0 {
		t.Fatalf("instance row must roll back, got %v", list)
	}
}

// Offline with no catalog promise (Door-2 / empty Images) → there is no trusted
// digest to fall back on, so a pull failure stays fatal even if the image is
// present locally. A loaded image carries no RepoDigest to pin from either.
func TestInstallOfflineNoPromiseFails(t *testing.T) {
	e := newTestEnv(t)
	e.m.offlineInstall = true
	e.writeCatalogApp(t, "whoami", whoamiCompose, whoamiManifest("")) // no promised digest
	e.docker.pullErr[testImage] = fmt.Errorf("dial tcp: registry unreachable")
	e.docker.loaded[testImage] = true

	_, err := e.m.Install(context.Background(), "whoami", Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, "", nil)
	if err == nil {
		t.Fatalf("want failure: offline install cannot pin an image with no catalog-promised digest")
	}
	if e.docker.called("ComposeUp") {
		t.Fatalf("ComposeUp must not run")
	}
}

// The fallback is gated: with offlineInstall OFF (the default — a box with a
// registry), a pull failure is fatal even when the image is present locally.
// Guards against the offline path silently masking a real registry outage.
func TestInstallPullFailureFatalWhenOnline(t *testing.T) {
	e := newTestEnv(t)
	// offlineInstall left false (default).
	e.writeCatalogApp(t, "whoami", whoamiCompose, whoamiManifest(testDigest))
	e.docker.pullErr[testImage] = fmt.Errorf("dial tcp: registry unreachable")
	e.docker.loaded[testImage] = true // present locally, but it must not matter

	_, err := e.m.Install(context.Background(), "whoami", Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, "", nil)
	if err == nil {
		t.Fatalf("online pull failure must be fatal regardless of local presence")
	}
	if e.docker.called("ComposeUp") {
		t.Fatalf("ComposeUp must not run on a fatal pull failure")
	}
}

// Offline mode does not alter the happy path: when the pull succeeds, the digest
// is still resolved from the registry RepoDigest, not the catalog promise.
func TestInstallOfflineModePullSucceedsUsesRegistry(t *testing.T) {
	e := newTestEnv(t)
	e.m.offlineInstall = true
	e.writeCatalogApp(t, "whoami", whoamiCompose, whoamiManifest(testDigest))
	e.docker.digests[testImage] = testDigest // pull succeeds, registry resolves

	inst, err := e.m.Install(context.Background(), "whoami", Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, "", nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if got, want := overridePin(t, e.stateDir, inst.ID, "whoami"), "traefik/whoami@"+testDigest; got != want {
		t.Fatalf("override pin = %q, want %q", got, want)
	}
}
