// Package catalog is the box's read model of the app store. It exposes a fixed
// six-method surface — List, Entry, Detail, IconPath, ScreenshotPath, Load — that
// internal/api (the box UI's catalog routes) and internal/lifecycle (install) both
// consume, and hides behind it whether the catalog is synced from the control
// plane or read from a local directory tree.
//
// In production every box — appliance and hosted alike — uses the remote client
// (NewRemote): a thin HTTP consumer of the control plane's /catalog/sync snapshot
// with a last-good on-disk cache (remote.go; cloud specs/CATALOG.md # Consume). No
// catalog is baked into the image (cloud #62). The original disk reader (New) is
// retained only as the constructor internal/api and internal/lifecycle tests build
// a controlled catalog with; it implements the same private source interface, so
// the rest of the brain holds a *Catalog and is agnostic to which is wired.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/malmoos/malmo/internal/manifest"
)

// ErrNotFound is returned by the lookup methods when no app exists for the id (on
// disk: the directory or manifest.yml is absent; remote: no such app in the synced
// snapshot). It is deliberately distinct from a manifest that exists but fails to
// parse or is missing its compose file — those are integrity errors a curated
// catalog should never ship — so the API maps ErrNotFound to 404 and every other
// error to 500. Follows the "typed errors at boundaries" rule (CLAUDE.md).
var ErrNotFound = errors.New("catalog: manifest not found")

// source is the read model behind Catalog: the six methods the brain consumes,
// implemented once per backing (disk / remote). Private because the brain always
// talks to *Catalog; the facade lets the remote client back production while the
// disk reader stays available to tests, with no change to internal/api or
// internal/lifecycle.
type source interface {
	List() ([]Entry, error)
	Entry(id string) (Entry, error)
	Detail(id string) (Detail, error)
	IconPath(id string) (string, error)
	ScreenshotPath(id string, i int) (string, error)
	Load(id string) (*manifest.Manifest, []byte, error)
	// featured returns the curated "top apps" for this box's surface, in the order
	// they should render (ascending rank). It is the only segmentation input the
	// facade can't derive from List: the remote source reads it from the synced
	// snapshot's Featured/Rank; the disk source has no curation and returns nil.
	featured() ([]Entry, error)
}

// Catalog is the brain-facing catalog handle. It is a thin facade over a source;
// New builds the disk-backed one, NewRemote the control-plane client.
type Catalog struct{ src source }

// New builds a disk-backed catalog rooted at a directory tree of
// <root>/<manifest_id>/{manifest.yml, <compose_file>}. Production no longer uses
// it (every box is a control-plane thin client — NewRemote); it is retained as the
// constructor internal/api and internal/lifecycle tests build a controlled catalog
// with, off a temp directory.
func New(root string) *Catalog { return &Catalog{src: newDiskSource(root)} }

func (c *Catalog) List() ([]Entry, error)             { return c.src.List() }
func (c *Catalog) Entry(id string) (Entry, error)     { return c.src.Entry(id) }
func (c *Catalog) Detail(id string) (Detail, error)   { return c.src.Detail(id) }
func (c *Catalog) IconPath(id string) (string, error) { return c.src.IconPath(id) }
func (c *Catalog) ScreenshotPath(id string, i int) (string, error) {
	return c.src.ScreenshotPath(id, i)
}
func (c *Catalog) Load(id string) (*manifest.Manifest, []byte, error) { return c.src.Load(id) }

// Home / Category / Search are the segmented store views the box UI browses
// through, mirroring the control plane's public catalog API (cloud
// specs/CATALOG.md # Serve) so the box never pulls the whole catalog up front. They
// are derived from the same source the flat List reads — the box already holds the
// whole snapshot locally, so these stay same-origin and work offline (the browse
// half of step 3's last-good cache) rather than re-proxying each hit to the control
// plane. They are computed on the facade (not per-source) because every input but
// featured is a projection of List; only featured differs by backing.

// Home is the store landing payload: the categories present on this box's surface
// plus the curated top apps. No per-app grid — the UI drills into a category or
// search for that. Mirror of cloud catalog.Home.
type Home struct {
	Categories []string `json:"categories"`
	Featured   []Entry  `json:"featured,omitempty"`
}

// Category is one category's apps plus the curated top apps (so a category page can
// render the same featured row as the landing). Mirror of cloud catalog.Category.
type Category struct {
	Category string  `json:"category"`
	Apps     []Entry `json:"apps"`
	Featured []Entry `json:"featured,omitempty"`
}

// Home returns the landing payload: the sorted union of every browsable app's
// categories, plus the featured row.
func (c *Catalog) Home() (Home, error) {
	apps, err := c.src.List()
	if err != nil {
		return Home{}, err
	}
	seen := map[string]bool{}
	for _, a := range apps {
		for _, cat := range a.Categories {
			seen[cat] = true
		}
	}
	cats := make([]string, 0, len(seen))
	for cat := range seen {
		cats = append(cats, cat)
	}
	sort.Strings(cats)
	feat, err := c.src.featured()
	if err != nil {
		return Home{}, err
	}
	return Home{Categories: cats, Featured: feat}, nil
}

// Category returns the apps tagged with cat (case-insensitive), plus the featured
// row. ErrNotFound when no browsable app carries the category, so an unknown or
// emptied category is a clean 404 rather than an empty page.
func (c *Catalog) Category(cat string) (Category, error) {
	apps, err := c.src.List()
	if err != nil {
		return Category{}, err
	}
	var out []Entry
	for _, a := range apps {
		if containsFold(a.Categories, cat) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return Category{}, fmt.Errorf("%w: category %q", ErrNotFound, cat)
	}
	feat, err := c.src.featured()
	if err != nil {
		return Category{}, err
	}
	return Category{Category: cat, Apps: out, Featured: feat}, nil
}

// Search returns the browsable apps whose name, short description, or categories
// contain q (case-insensitive substring). A blank query returns nothing rather than
// the whole catalog — search narrows, browse is for everything.
func (c *Catalog) Search(q string) ([]Entry, error) {
	q = strings.TrimSpace(strings.ToLower(q))
	if q == "" {
		return nil, nil
	}
	apps, err := c.src.List()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, a := range apps {
		hay := strings.ToLower(strings.Join(append([]string{a.Name, a.ShortDescription}, a.Categories...), "\n"))
		if strings.Contains(hay, q) {
			out = append(out, a)
		}
	}
	return out, nil
}

// containsFold reports whether needle equals any of haystack, case-insensitively —
// the category match, so "Media" and "media" are the same store category.
func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

// StartRefresh starts the background sync loop of a remote catalog, bound to ctx
// (it stops when ctx is cancelled). It is a no-op for a disk-backed catalog, so
// cmd/brain can call it unconditionally. The first sync also runs immediately so a
// freshly provisioned box populates its store without waiting a full interval.
func (c *Catalog) StartRefresh(ctx context.Context) {
	if r, ok := c.src.(*remoteSource); ok {
		r.startRefresh(ctx)
	}
}

// Entry is the store-facing summary of one available app. It carries exactly what
// the browse grid needs to render a card without a second fetch (APP_STORE.md #
// Catalog schema): the identity, the one-liner, the categories, and an icon URL.
// The detail page fetches Detail for the rest.
type Entry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`

	// ShortDescription is the one-line tagline (manifest description.short).
	ShortDescription string `json:"short_description,omitempty"`
	// Categories group the app in the store (manifest categories).
	Categories []string `json:"categories,omitempty"`
	// IconURL points at the brain's icon asset route for this app, set only when
	// the app declares an icon. Empty ⇒ the store falls back to a glyph. The route
	// is always the brain's own /api/v1/catalog/{id}/icon — never the control
	// plane directly — so the box UI stays on the box origin (AUTH_AND_ACCESS.md;
	// the brain proxies the asset for a remote catalog).
	IconURL string `json:"icon_url,omitempty"`
	// IconGlyph is the manifest's Lucide-icon fallback name (kebab-case) the store
	// renders when IconURL is empty. Empty (and no IconURL) ⇒ the generic glyph.
	IconGlyph string `json:"icon_glyph,omitempty"`

	// Footprint is the on-disk summary the store grid renders without a second
	// fetch (APP_STORE.md # Catalog schema). The image totals are an upper bound
	// — nothing is assumed cached locally, so the install dialog shows a sharper,
	// box-specific figure (BRAIN_UI_PROTOCOL.md # install-plan). The app-state
	// figure (estimated_size) is the manifest's measured baseline at install, not
	// a usage projection (APP_MANIFEST.md # Storage, DECISIONS.md 2026-06-09).
	Footprint manifest.Footprint `json:"footprint"`
}

// Detail is the full store detail-page view of one app (APP_STORE.md # Catalog
// schema): everything in Entry plus the long markdown body, screenshots, and the
// author/license/links metadata. Rendered by the app detail page; the brain acts
// on none of it.
type Detail struct {
	Entry

	// LongDescription is the markdown body shown on the detail page
	// (manifest description.long).
	LongDescription string `json:"long_description,omitempty"`
	// ScreenshotURLs point at the brain's screenshot asset route, in manifest
	// order. Empty ⇒ no gallery.
	ScreenshotURLs []string `json:"screenshot_urls,omitempty"`

	// Author and Links are pointers so a manifest that declares neither omits the
	// keys entirely rather than serializing `{}` — `omitempty` is a no-op on a
	// struct value (only pointers/slices/maps/scalars count as empty), which
	// would otherwise hand the UI an empty-but-present block to render.
	Author       *manifest.Author `json:"author,omitempty"`
	License      string           `json:"license,omitempty"`
	Links        *manifest.Links  `json:"links,omitempty"`
	ChangelogURL string           `json:"changelog_url,omitempty"`
}

// iconURL / screenshotURL are the brain-served asset routes the store loads
// directly in <img> tags (APP_STORE.md # Catalog schema). Both catalog sources
// project these same brain-origin URLs (the remote source proxies the underlying
// control-plane asset behind them), so the UI's hard-coded route shapes never
// change. Kept here so the URL shape lives next to the types that carry it.
func iconURL(id string) string { return "/api/v1/catalog/" + id + "/icon" }
func screenshotURL(id string, i int) string {
	return fmt.Sprintf("/api/v1/catalog/%s/screenshots/%d", id, i)
}
