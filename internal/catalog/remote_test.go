package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
func newRemote(baseURL, env, cacheDir string) *remoteSource {
	c := NewRemote(RemoteOptions{BaseURL: baseURL, Environment: env, CacheDir: cacheDir})
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

func TestRemoteLastGoodFallback(t *testing.T) {
	cp := newFakeCP(t, testApps())
	srv := cp.server()
	cache := t.TempDir()

	// First box syncs successfully and writes the last-good cache.
	r := newRemote(srv.URL, "hosted", cache)
	if err := r.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.Close() // control plane now unreachable

	// A fresh box pointed at the same cache but a dead control plane loads
	// last-good on construction and still browses.
	r2 := newRemote(srv.URL, "hosted", cache)
	if l, _ := r2.List(); len(l) != 2 {
		t.Fatalf("last-good List = %d apps, want 2", len(l))
	}
	// And a sync attempt against the dead server keeps the last-good snapshot.
	if err := r2.syncOnce(context.Background()); err == nil {
		t.Fatal("syncOnce against dead server should error")
	}
	if l, _ := r2.List(); len(l) != 2 {
		t.Fatalf("List after failed sync = %d apps, want last-good 2", len(l))
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
