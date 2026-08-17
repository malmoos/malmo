package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// validManifest is a minimal manifest.yml the box's manifest.Parse accepts, so
// Load exercises the real re-parse path a remote install depends on.
func validManifest(id, name string) string {
	return "id: " + id + `
manifest_version: 1
name: ` + name + `
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 80
`
}

// testApps is the fixture catalog: one appliance+hosted app with an icon, one
// hosted-only app, so env filtering and the projections both have something to
// bite on.
func testApps() []wireApp {
	return []wireApp{
		{
			ID:               "alpha",
			Name:             "Alpha",
			Version:          "1.0",
			ShortDescription: "the first app",
			LongDescription:  "# Alpha\nlong body",
			Categories:       []string{"tools"},
			IconGlyph:        "box",
			IconFile:         "icon.png",
			Screenshots:      []string{"screenshots/0.png"},
			Environments:     []string{"appliance", "hosted"},
			Manifest:         validManifest("alpha", "Alpha"),
			Compose:          "services:\n  web:\n    image: alpha:1\n",
		},
		{
			ID:           "beta",
			Name:         "Beta",
			Version:      "2.0",
			Environments: []string{"hosted"},
			Manifest:     validManifest("beta", "Beta"),
			Compose:      "services:\n  web:\n    image: beta:2\n",
		},
	}
}

// makeSnapshot marshals apps into a served /catalog/sync body with the correct
// stamped digest, and returns the body plus its ETag.
func makeSnapshot(t *testing.T, apps []wireApp) (body []byte, etag string) {
	t.Helper()
	digest, err := indexDigest(apps)
	if err != nil {
		t.Fatal(err)
	}
	f := catalogFile{SchemaVersion: wireSchemaVersion, IndexSHA256: digest, Apps: apps}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return b, `"` + digest + `"`
}

// fakeCP is a controllable control-plane catalog fake: it serves the snapshot
// (honouring If-None-Match), serves per-app assets, and counts hits so tests can
// assert caching and 304 behaviour.
type fakeCP struct {
	mu        sync.Mutex
	body      []byte
	etag      string
	asset     []byte
	syncHits  int
	assetHits int
	failSync  bool // 500 every /catalog/sync
	failAsset bool // 500 every asset fetch
}

func newFakeCP(t *testing.T, apps []wireApp) *fakeCP {
	body, etag := makeSnapshot(t, apps)
	return &fakeCP{body: body, etag: etag, asset: []byte("\x89PNG-fake-bytes")}
}

func (f *fakeCP) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/catalog/sync":
			f.syncHits++
			if f.failSync {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.Header().Set("ETag", f.etag)
			if r.Header.Get("If-None-Match") == f.etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Write(f.body)
		case strings.HasPrefix(r.URL.Path, "/catalog/") && strings.Contains(r.URL.Path, "/icon"),
			strings.Contains(r.URL.Path, "/screenshots/"):
			f.assetHits++
			if f.failAsset {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.Write(f.asset)
		default:
			http.NotFound(w, r)
		}
	}))
}

// newRemote builds a remoteSource (not the Catalog facade) so tests can drive
// syncOnce and the projections directly, without a background loop racing them.
// cacheDir is the ASSET cache — the snapshot is never written anywhere.
func newRemote(baseURL, env, cacheDir string) *remoteSource {
	c := NewRemote(RemoteOptions{BaseURL: baseURL, Environment: env, AssetCacheDir: cacheDir})
	return c.src.(*remoteSource)
}

// newRemoteFromFile builds a remoteSource seeded from a local snapshot file, the
// dev/test seam (MALMO_CATALOG_FILE).
func newRemoteFromFile(baseURL, env, cacheDir, snapshotFile string) *remoteSource {
	c := NewRemote(RemoteOptions{
		BaseURL: baseURL, Environment: env,
		AssetCacheDir: cacheDir, SnapshotFile: snapshotFile,
	})
	return c.src.(*remoteSource)
}

func TestRemoteSyncAndProject(t *testing.T) {
	cp := newFakeCP(t, testApps())
	srv := cp.server()
	defer srv.Close()

	r := newRemote(srv.URL, "appliance", t.TempDir())
	if err := r.syncOnce(context.Background()); err != nil {
		t.Fatalf("syncOnce: %v", err)
	}

	// List is env-filtered: appliance sees only alpha (beta is hosted-only).
	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "alpha" {
		t.Fatalf("appliance List = %+v, want just alpha", list)
	}
	if list[0].IconURL != "/api/v1/catalog/alpha/icon" {
		t.Fatalf("icon URL must be the brain route, got %q", list[0].IconURL)
	}

	// Entry is unfiltered by env: beta resolves even though it isn't in the
	// appliance browse grid (installed-instance enrichment).
	if _, err := r.Entry("beta"); err != nil {
		t.Fatalf("Entry(beta) should resolve regardless of env: %v", err)
	}

	// Detail is env-gated: beta is unreachable on appliance.
	if _, err := r.Detail("beta"); err == nil {
		t.Fatal("Detail(beta) on appliance should be ErrNotFound")
	}
	d, err := r.Detail("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if d.LongDescription == "" || len(d.ScreenshotURLs) != 1 {
		t.Fatalf("alpha detail incomplete: %+v", d)
	}

	// Load re-parses the verbatim manifest with the box's own parser.
	man, compose, err := r.Load("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if man.ID != "alpha" || !strings.Contains(string(compose), "alpha:1") {
		t.Fatalf("Load(alpha) wrong: id=%q compose=%q", man.ID, compose)
	}

	// A hosted box sees both apps.
	rh := newRemote(srv.URL, "hosted", t.TempDir())
	if err := rh.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l, _ := rh.List(); len(l) != 2 {
		t.Fatalf("hosted List = %d apps, want 2", len(l))
	}
}

// TestRemoteKeepsNoSnapshotOnDisk pins the rule that the box holds the catalog in
// memory only. A synced source survives a later failed sync (the snapshot it
// already has stays), but nothing about it outlives the process: a fresh source
// over the very same directory starts empty, because there is no file to read.
//
// The store being empty rather than stale is the intended behavior, not a
// degradation to work around — see APP_STORE.md # Failure modes.
func TestRemoteKeepsNoSnapshotOnDisk(t *testing.T) {
	cp := newFakeCP(t, testApps())
	srv := cp.server()
	dir := t.TempDir()

	r := newRemote(srv.URL, "hosted", dir)
	if err := r.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l, _ := r.List(); len(l) != 2 {
		t.Fatalf("List after sync = %d apps, want 2", len(l))
	}
	srv.Close() // control plane now unreachable

	// A failed sync leaves the snapshot this source already holds untouched.
	if err := r.syncOnce(context.Background()); err == nil {
		t.Fatal("syncOnce against dead server should error")
	}
	if l, _ := r.List(); len(l) != 2 {
		t.Fatalf("List after failed sync = %d apps, want the 2 already in memory", len(l))
	}

	// A new source over the same directory — the restart case — has nothing to
	// read back, so it browses empty until a sync lands.
	r2 := newRemote(srv.URL, "hosted", dir)
	if l, _ := r2.List(); len(l) != 0 {
		t.Fatalf("a restarted box must start empty, got %d apps: the snapshot was persisted somewhere", len(l))
	}

	// And no snapshot-shaped file was written under the asset cache dir either.
	found, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Errorf("snapshot written to disk: %v", found)
	}
}

// TestRemoteSnapshotFileSeed covers the dev/test seam (MALMO_CATALOG_FILE): a
// local snapshot seeds the store at construction, and the brain never writes to
// it — the file belongs to whoever staged it.
func TestRemoteSnapshotFileSeed(t *testing.T) {
	body, _ := makeSnapshot(t, testApps())
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// No control plane at all: the seed is the whole store.
	r := newRemoteFromFile("http://127.0.0.1:1", "hosted", t.TempDir(), path)
	if l, _ := r.List(); len(l) != 2 {
		t.Fatalf("seeded List = %d apps, want 2", len(l))
	}
	if err := r.syncOnce(context.Background()); err == nil {
		t.Fatal("syncOnce against no control plane should error")
	}
	if l, _ := r.List(); len(l) != 2 {
		t.Fatalf("List after failed sync = %d apps, want the seeded 2", len(l))
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Error("the seed file was written to; it is an input, not a cache")
	}
}

// TestRemoteSnapshotFileInvalid: a missing or corrupt seed leaves the store empty
// and the source usable, rather than failing construction. A dev seed must not be
// able to stop a box from booting.
func TestRemoteSnapshotFileInvalid(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{bad, filepath.Join(dir, "absent.json")} {
		r := newRemoteFromFile("http://127.0.0.1:1", "hosted", t.TempDir(), path)
		if l, _ := r.List(); len(l) != 0 {
			t.Errorf("seed %q: List = %d apps, want empty", path, len(l))
		}
	}
}

func TestRemoteNeverSyncedIsEmpty(t *testing.T) {
	cp := newFakeCP(t, testApps())
	cp.failSync = true
	srv := cp.server()
	defer srv.Close()

	r := newRemote(srv.URL, "appliance", t.TempDir())
	if err := r.syncOnce(context.Background()); err == nil {
		t.Fatal("want sync error from failing control plane")
	}
	if l, _ := r.List(); len(l) != 0 {
		t.Fatalf("never-synced store must be empty, got %d", len(l))
	}
	if _, err := r.Entry("alpha"); err == nil {
		t.Fatal("Entry on empty store should be ErrNotFound")
	}
}

func TestRemoteIntegrityRefusesTampered(t *testing.T) {
	cp := newFakeCP(t, testApps())
	// Corrupt the served body without restamping the digest.
	cp.body = append([]byte(nil), cp.body...)
	cp.body = []byte(strings.Replace(string(cp.body), "Alpha", "Xlpha", 1))
	srv := cp.server()
	defer srv.Close()

	r := newRemote(srv.URL, "appliance", t.TempDir())
	if err := r.syncOnce(context.Background()); err == nil {
		t.Fatal("tampered snapshot must fail verify")
	}
	if l, _ := r.List(); len(l) != 0 {
		t.Fatal("tampered snapshot must not become the read source")
	}
}

func TestRemoteAssetProxyAndCache(t *testing.T) {
	cp := newFakeCP(t, testApps())
	srv := cp.server()
	defer srv.Close()

	r := newRemote(srv.URL, "appliance", t.TempDir())
	if err := r.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	p1, err := r.IconPath("alpha")
	if err != nil {
		t.Fatalf("IconPath: %v", err)
	}
	p2, err := r.IconPath("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("icon path not stable: %q vs %q", p1, p2)
	}
	cp.mu.Lock()
	hits := cp.assetHits
	cp.mu.Unlock()
	if hits != 1 {
		t.Fatalf("asset fetched %d times, want 1 (second served from cache)", hits)
	}

	// Unknown app / no-icon app / out-of-range screenshot are ErrNotFound.
	if _, err := r.IconPath("beta"); err == nil {
		t.Fatal("beta has no icon; want ErrNotFound")
	}
	if _, err := r.ScreenshotPath("alpha", 5); err == nil {
		t.Fatal("out-of-range screenshot; want ErrNotFound")
	}
}

// TestRemoteAssetExpires covers the asset TTL. Asset filenames are stable per app
// ("icon.png"), so without an expiry the first icon a box fetched would be the
// icon it served forever, and republished artwork would never reach the fleet.
// Aging the cached file past assetTTL must produce a refetch.
func TestRemoteAssetExpires(t *testing.T) {
	cp := newFakeCP(t, testApps())
	srv := cp.server()
	defer srv.Close()

	r := newRemote(srv.URL, "appliance", t.TempDir())
	if err := r.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err := r.IconPath("alpha")
	if err != nil {
		t.Fatal(err)
	}

	// A second request inside the TTL is served from disk.
	if _, err := r.IconPath("alpha"); err != nil {
		t.Fatal(err)
	}
	cp.mu.Lock()
	hits := cp.assetHits
	cp.mu.Unlock()
	if hits != 1 {
		t.Fatalf("asset fetched %d times inside the TTL, want 1", hits)
	}

	// Age the cached file past the TTL: the next request refetches.
	old := time.Now().Add(-assetTTL - time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := r.IconPath("alpha"); err != nil {
		t.Fatal(err)
	}
	cp.mu.Lock()
	hits = cp.assetHits
	cp.mu.Unlock()
	if hits != 2 {
		t.Fatalf("asset fetched %d times after expiry, want 2 (the expired copy must be refreshed)", hits)
	}
}

// TestRemoteExpiredAssetSurvivesFailedRefresh: once an asset is expired but the
// control plane is unreachable, the box serves the stale file rather than a
// broken image. The snapshot has no such fallback; artwork does.
func TestRemoteExpiredAssetSurvivesFailedRefresh(t *testing.T) {
	cp := newFakeCP(t, testApps())
	srv := cp.server()
	defer srv.Close()

	r := newRemote(srv.URL, "appliance", t.TempDir())
	if err := r.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err := r.IconPath("alpha")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-assetTTL - time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}

	cp.mu.Lock()
	cp.failAsset = true
	cp.mu.Unlock()

	got, err := r.IconPath("alpha")
	if err != nil {
		t.Fatalf("an expired asset with a failing refresh must still be served: %v", err)
	}
	if got != p {
		t.Fatalf("IconPath = %q, want the expired copy %q", got, p)
	}
}

func TestRemoteSnapshotSizeCapRejects(t *testing.T) {
	cp := newFakeCP(t, testApps())
	// Serve a body far over the snapshot cap; parse must never be reached.
	cp.body = make([]byte, maxSnapshotBytes+1)
	cp.etag = `"oversize"`
	srv := cp.server()
	defer srv.Close()

	r := newRemote(srv.URL, "appliance", t.TempDir())
	if err := r.syncOnce(context.Background()); err == nil {
		t.Fatal("oversize snapshot must fail (size cap)")
	}
	if l, _ := r.List(); len(l) != 0 {
		t.Fatal("oversize snapshot must not become the read source")
	}
}

func TestRemoteAssetFetchCollapsesConcurrent(t *testing.T) {
	cp := newFakeCP(t, testApps())
	srv := cp.server()
	defer srv.Close()

	r := newRemote(srv.URL, "appliance", t.TempDir())
	if err := r.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Fire many concurrent first-time icon requests: the per-asset lock must
	// collapse them into a single control-plane fetch.
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.IconPath("alpha"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("IconPath: %v", err)
	}
	cp.mu.Lock()
	hits := cp.assetHits
	cp.mu.Unlock()
	if hits != 1 {
		t.Fatalf("asset fetched %d times under %d concurrent requests, want 1", hits, n)
	}
}

func TestRemoteStartRefreshIsIdempotent(t *testing.T) {
	cp := newFakeCP(t, testApps())
	srv := cp.server()
	defer srv.Close()

	r := newRemote(srv.URL, "appliance", t.TempDir())
	// The guard is an atomic CAS, independent of the loop; assert it directly so
	// the test doesn't race the background goroutine's first sync.
	if r.started.Load() {
		t.Fatal("started should be false before startRefresh")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.startRefresh(ctx)
	if !r.started.Load() {
		t.Fatal("started must be true after first startRefresh")
	}
	// A second call must be a no-op (no second goroutine); CAS already false→true,
	// so a repeat returns immediately.
	r.startRefresh(ctx) // must not panic or spawn a second loop
}

func TestRemote304KeepsSnapshot(t *testing.T) {
	cp := newFakeCP(t, testApps())
	srv := cp.server()
	defer srv.Close()

	r := newRemote(srv.URL, "hosted", t.TempDir())
	if err := r.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Second sync sends If-None-Match and gets a 304; snapshot stays intact.
	if err := r.syncOnce(context.Background()); err != nil {
		t.Fatalf("304 path should not error: %v", err)
	}
	if l, _ := r.List(); len(l) != 2 {
		t.Fatalf("List after 304 = %d apps, want 2", len(l))
	}
	cp.mu.Lock()
	hits := cp.syncHits
	cp.mu.Unlock()
	if hits != 2 {
		t.Fatalf("sync hit control plane %d times, want 2", hits)
	}
}
