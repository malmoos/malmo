package catalog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/malmoos/malmo/internal/manifest"
)

// remote.go is the box's thin-client catalog: it consumes the control plane's
// public-read catalog API (cloud specs/CATALOG.md) instead of reading a baked
// directory. The box fetches the whole snapshot (GET /catalog/sync) in one shot,
// verifies its integrity digest, caches it last-good on disk, and projects the
// six-method surface locally — so the UI never blocks on the network and an
// offline box still browses its last-good catalog. A never-synced box shows an
// empty store. It stays the box↔cloud install contract: Load re-parses the
// verbatim manifest with the box's own manifest.Parse, so the box remains the sole
// enforcer of the manifest contract.

const (
	// defaultRefreshInterval is how often the box re-syncs the snapshot. The
	// catalog changes rarely (a store publish), and every fetch is a cheap 304
	// when nothing moved (If-None-Match against the index digest), so this is
	// loose by design. Overridable via RemoteOptions.
	defaultRefreshInterval = 15 * time.Minute
	// httpTimeout bounds a single snapshot or asset fetch. The snapshot is a small
	// JSON blob and assets are icons/screenshots, so this is generous.
	httpTimeout = 30 * time.Second
	// cacheFileName is the last-good snapshot on disk under CacheDir. Byte-for-byte
	// the /catalog/sync body, so the integrity digest re-verifies over exactly the
	// bytes the sync tool stamped.
	cacheFileName = "catalog.json"
	// assetsDir is the CacheDir subtree the proxied icon/screenshot files land in,
	// mirroring the control plane's per-app asset layout (<id>/<file>).
	assetsDir = "assets"
)

// RemoteOptions configures the control-plane catalog client. BaseURL is the
// control plane's origin serving the catalog API (the client appends /catalog/…);
// Environment is the box's own surface ("appliance"|"hosted") used to filter the
// snapshot; CacheDir holds the last-good snapshot and proxied assets.
type RemoteOptions struct {
	BaseURL         string
	Environment     string
	CacheDir        string
	RefreshInterval time.Duration
	HTTPClient      *http.Client
}

// remoteSource implements source against the control-plane catalog API with a
// last-good on-disk cache. Reads project from the in-memory snapshot under an
// RLock; the background sync loop swaps a freshly fetched-and-verified snapshot in
// under the write lock, so a read never blocks on the network and never sees a
// half-applied snapshot.
type remoteSource struct {
	baseURL  string
	env      string
	cacheDir string
	interval time.Duration
	http     *http.Client

	mu   sync.RWMutex
	snap *snapshot // nil until the first successful sync or cache load
	etag string    // last snapshot's ETag (quoted), for a cheap If-None-Match 304
}

// snapshot is the immutable, indexed projection of one verified catalog file: the
// apps sorted by name (stable grid order) plus an id lookup. Swapped wholesale on
// each successful sync, so readers holding a *snapshot see a consistent view.
type snapshot struct {
	apps []wireApp
	byID map[string]*wireApp
}

func newSnapshot(f catalogFile) *snapshot {
	s := &snapshot{
		apps: append([]wireApp(nil), f.Apps...),
		byID: make(map[string]*wireApp, len(f.Apps)),
	}
	sort.Slice(s.apps, func(i, j int) bool { return s.apps[i].Name < s.apps[j].Name })
	for i := range s.apps {
		s.byID[s.apps[i].ID] = &s.apps[i]
	}
	return s
}

// NewRemote builds a control-plane-backed catalog. It loads the last-good cache
// synchronously (so an offline or slow-to-sync box browses immediately) but does
// not touch the network — call StartRefresh to begin syncing. A missing or
// unreadable cache is not an error: the box simply starts with an empty store
// until the first sync lands.
func NewRemote(opts RemoteOptions) *Catalog {
	interval := opts.RefreshInterval
	if interval <= 0 {
		interval = defaultRefreshInterval
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	rs := &remoteSource{
		baseURL:  strings.TrimRight(opts.BaseURL, "/"),
		env:      opts.Environment,
		cacheDir: opts.CacheDir,
		interval: interval,
		http:     client,
	}
	if err := os.MkdirAll(opts.CacheDir, 0o755); err != nil {
		slog.Error("catalog: create cache dir; last-good cache disabled", "src", opts.CacheDir, "err", err)
	}
	rs.loadCache()
	return &Catalog{src: rs}
}

// loadCache seeds the in-memory snapshot from the last-good cache file at startup.
// Best-effort: an absent file is the never-synced case (empty store), and a
// corrupt or tampered file fails verify and is ignored (the next sync overwrites
// it) rather than crashing the box. On success it also primes the ETag so the very
// first sync can 304 when the control plane still holds the same snapshot.
func (r *remoteSource) loadCache() {
	data, err := os.ReadFile(r.cachePath())
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("catalog: read last-good cache failed; starting with empty store", "src", r.cachePath(), "err", err)
		}
		return
	}
	f, err := parseSnapshot(data)
	if err != nil {
		slog.Warn("catalog: last-good cache invalid; ignoring", "src", r.cachePath(), "err", err)
		return
	}
	r.mu.Lock()
	r.snap = newSnapshot(f)
	r.etag = `"` + f.IndexSHA256 + `"`
	r.mu.Unlock()
	slog.Info("catalog: loaded last-good cache", "apps", len(f.Apps), "index_sha256", f.IndexSHA256)
}

// startRefresh runs the background sync loop bound to ctx: one immediate sync (so
// a freshly provisioned box populates its store promptly) then one per interval.
// Each attempt is independent and best-effort — a failure keeps the last-good
// snapshot and the loop retries next tick — so a control plane blip never empties
// the store.
func (r *remoteSource) startRefresh(ctx context.Context) {
	go func() {
		if err := r.syncOnce(ctx); err != nil {
			slog.Warn("catalog: initial sync failed; serving last-good cache", "err", err)
		}
		t := time.NewTicker(r.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := r.syncOnce(ctx); err != nil {
					slog.Warn("catalog: sync failed; serving last-good cache", "err", err)
				}
			}
		}
	}()
}

// syncOnce fetches the snapshot once, verifies it, and swaps it in as the new read
// source, writing it through to the last-good cache. A 304 (the control plane
// still holds the snapshot the box last saw) is the common no-op path. A transport
// error, a non-200/304 status, or a failed verify returns an error and leaves the
// current snapshot untouched.
func (r *remoteSource) syncOnce(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/catalog/sync", nil)
	if err != nil {
		return err
	}
	r.mu.RLock()
	etag := r.etag
	r.mu.RUnlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("catalog: fetch snapshot: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNotModified {
		return nil // nothing changed since the last sync
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("catalog: fetch snapshot: unexpected status %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("catalog: read snapshot body: %w", err)
	}
	f, err := parseSnapshot(data)
	if err != nil {
		return err // integrity failure: don't cache or swap a bad snapshot
	}
	newETag := resp.Header.Get("ETag")
	if newETag == "" {
		newETag = `"` + f.IndexSHA256 + `"`
	}
	r.mu.Lock()
	prev := r.etag
	r.snap = newSnapshot(f)
	r.etag = newETag
	r.mu.Unlock()
	// Persist last-good only when the content actually changed, to avoid rewriting
	// the same bytes every interval. Verified bytes, so the cache re-verifies on
	// next boot.
	if newETag != prev {
		if err := writeFileAtomic(r.cachePath(), data); err != nil {
			slog.Warn("catalog: persist last-good cache failed; in-memory snapshot still updated", "src", r.cachePath(), "err", err)
		}
		slog.Info("catalog: synced snapshot", "apps", len(f.Apps), "index_sha256", f.IndexSHA256)
	}
	return nil
}

func (r *remoteSource) cachePath() string { return filepath.Join(r.cacheDir, cacheFileName) }

// current returns the live snapshot under the read lock, or nil when the box has
// never synced and has no cache (empty store).
func (r *remoteSource) current() *snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snap
}

// visibleIn reports whether the app is advertised on the box's environment. A box
// shows an app iff its environments list contains that surface (cloud
// specs/CATALOG.md # Visibility); the snapshot carries every app, so this is the
// box's only browse-time filter.
func (a *wireApp) visibleIn(env string) bool {
	for _, e := range a.Environments {
		if e == env {
			return true
		}
	}
	return false
}

// entryOfApp / detailOfApp project a published app into the box's grid / detail
// shapes. The icon and screenshot URLs are the brain's own asset routes (the
// remote source proxies the underlying control-plane asset behind them), so the UI
// contract is identical to the disk source. Featured/Rank are not surfaced — the
// box store has no curated rows.
func entryOfApp(a *wireApp) Entry {
	e := Entry{
		ID:               a.ID,
		Name:             a.Name,
		Version:          a.Version,
		ShortDescription: a.ShortDescription,
		Categories:       a.Categories,
		IconGlyph:        a.IconGlyph,
		Footprint:        a.Footprint,
	}
	if a.IconFile != "" {
		e.IconURL = iconURL(a.ID)
	}
	return e
}

func detailOfApp(a *wireApp) Detail {
	d := Detail{
		Entry:           entryOfApp(a),
		LongDescription: a.LongDescription,
		Author:          a.Author,
		License:         a.License,
		Links:           a.Links,
		ChangelogURL:    a.ChangelogURL,
	}
	for i := range a.Screenshots {
		d.ScreenshotURLs = append(d.ScreenshotURLs, screenshotURL(a.ID, i))
	}
	return d
}

// List returns the browse grid: one Entry per app visible in the box's
// environment, in stable by-name order. An empty (never-synced) store returns no
// entries, not an error.
func (r *remoteSource) List() ([]Entry, error) {
	snap := r.current()
	if snap == nil {
		return nil, nil
	}
	var out []Entry
	for i := range snap.apps {
		if snap.apps[i].visibleIn(r.env) {
			out = append(out, entryOfApp(&snap.apps[i]))
		}
	}
	return out, nil
}

// Entry returns the grid summary for one app by id, unfiltered by environment — so
// an installed app that isn't advertised on this box's surface still resolves its
// card metadata (mirrors the disk source's honesty). ErrNotFound when the app is
// absent from the snapshot.
func (r *remoteSource) Entry(id string) (Entry, error) {
	snap := r.current()
	if snap == nil {
		return Entry{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	a, ok := snap.byID[id]
	if !ok {
		return Entry{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return entryOfApp(a), nil
}

// Detail returns the full detail-page view of one app, store-facing: an app not
// visible on this box's surface is ErrNotFound, so its detail page is unreachable
// from a store that doesn't advertise it (mirrors the disk source's unlisted gate).
func (r *remoteSource) Detail(id string) (Detail, error) {
	snap := r.current()
	if snap != nil {
		if a, ok := snap.byID[id]; ok && a.visibleIn(r.env) {
			return detailOfApp(a), nil
		}
	}
	return Detail{}, fmt.Errorf("%w: %q", ErrNotFound, id)
}

// Load returns the parsed manifest and verbatim compose bytes for install,
// unfiltered by environment (a box installs by an id it already learned). The
// manifest is re-parsed with the box's own manifest.Parse — the box, not the
// cloud, enforces the manifest contract. ErrNotFound when the app is absent.
func (r *remoteSource) Load(id string) (*manifest.Manifest, []byte, error) {
	snap := r.current()
	if snap == nil {
		return nil, nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	a, ok := snap.byID[id]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	man, err := manifest.Parse([]byte(a.Manifest))
	if err != nil {
		return nil, nil, err
	}
	return man, []byte(a.Compose), nil
}

// IconPath returns a local file path to the app's icon, proxying it from the
// control plane on first request and caching it under CacheDir/assets so later
// requests (and offline browsing) are served locally. ErrNotFound when the app is
// unknown or declares no icon.
func (r *remoteSource) IconPath(id string) (string, error) {
	snap := r.current()
	if snap == nil {
		return "", fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	a, ok := snap.byID[id]
	if !ok || a.IconFile == "" {
		return "", fmt.Errorf("%w: %q has no icon", ErrNotFound, id)
	}
	return r.cachedAsset(id, a.IconFile, "/catalog/"+id+"/icon")
}

// ScreenshotPath returns a local file path to the i-th screenshot (manifest order,
// 0-based), proxied and cached like the icon. ErrNotFound when the app is unknown
// or the index is out of range.
func (r *remoteSource) ScreenshotPath(id string, i int) (string, error) {
	snap := r.current()
	if snap == nil {
		return "", fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	a, ok := snap.byID[id]
	if !ok || i < 0 || i >= len(a.Screenshots) {
		return "", fmt.Errorf("%w: %q screenshot %d", ErrNotFound, id, i)
	}
	return r.cachedAsset(id, a.Screenshots[i], fmt.Sprintf("/catalog/%s/screenshots/%d", id, i))
}

// cachedAsset resolves an asset to a local file, fetching it from the control
// plane once and caching it under CacheDir/assets/<id>/<assetFile>. The asset name
// comes from the verified snapshot, but it is still contained under the assets
// root defensively (a corrupt snapshot must never write outside the cache). A
// fetch failure with no cached copy returns a non-ErrNotFound error (500) — the
// app genuinely has this asset; the box just can't reach it right now.
func (r *remoteSource) cachedAsset(id, assetFile, cpPath string) (string, error) {
	local, ok := safeJoin(filepath.Join(r.cacheDir, assetsDir, id), assetFile)
	if !ok {
		return "", fmt.Errorf("%w: %q asset %q escapes cache dir", ErrNotFound, id, assetFile)
	}
	if _, err := os.Stat(local); err == nil {
		return local, nil // cache hit
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+cpPath, nil)
	if err != nil {
		return "", err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("catalog: fetch asset %q: %w", cpPath, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("catalog: fetch asset %q: unexpected status %s", cpPath, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("catalog: read asset %q: %w", cpPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return "", fmt.Errorf("catalog: cache asset dir: %w", err)
	}
	if err := writeFileAtomic(local, data); err != nil {
		return "", fmt.Errorf("catalog: cache asset %q: %w", cpPath, err)
	}
	return local, nil
}

// safeJoin joins rel under base, rejecting any rel that would escape base (a
// corrupt snapshot carrying "../.."). rel is a forward-slash asset path from the
// snapshot; it is cleaned against a virtual root before being re-based under base.
func safeJoin(base, rel string) (string, bool) {
	full := filepath.Join(base, filepath.FromSlash(filepath.Clean("/"+rel)))
	if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", false
	}
	return full, true
}

// writeFileAtomic writes data to a temp file in the destination directory and
// renames it into place, so a concurrent reader (or a crash mid-write) never sees
// a half-written file — the last-good cache and each proxied asset must be all or
// nothing.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
