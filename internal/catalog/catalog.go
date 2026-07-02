// Package catalog is the box's read model of the app store. It exposes a fixed
// six-method surface — List, Entry, Detail, IconPath, ScreenshotPath, Load — that
// internal/api (the box UI's catalog routes) and internal/lifecycle (install) both
// consume, and hides behind it whether the catalog is read from a baked directory
// tree or synced from the control plane.
//
// Two sources implement that surface (the private source interface): the original
// disk reader (New, backing the appliance's baked catalog/ and every test), and
// the remote client (NewRemote), a thin HTTP consumer of the control plane's
// /catalog/sync snapshot with a last-good on-disk cache (remote.go; cloud
// specs/CATALOG.md # Consume). The rest of the brain holds a *Catalog and is
// agnostic to which is wired.
package catalog

import (
	"context"
	"errors"
	"fmt"

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
// talks to *Catalog; the facade exists so a remote client can replace the baked
// directory with no change to internal/api or internal/lifecycle.
type source interface {
	List() ([]Entry, error)
	Entry(id string) (Entry, error)
	Detail(id string) (Detail, error)
	IconPath(id string) (string, error)
	ScreenshotPath(id string, i int) (string, error)
	Load(id string) (*manifest.Manifest, []byte, error)
}

// Catalog is the brain-facing catalog handle. It is a thin facade over a source;
// New builds the disk-backed one, NewRemote the control-plane client.
type Catalog struct{ src source }

// New builds a disk-backed catalog rooted at a directory tree of
// <root>/<manifest_id>/{manifest.yml, <compose_file>}. This is the appliance's
// baked catalog and the source every test constructs. The remote (hosted) client
// is NewRemote.
func New(root string) *Catalog { return &Catalog{src: newDiskSource(root)} }

func (c *Catalog) List() ([]Entry, error)             { return c.src.List() }
func (c *Catalog) Entry(id string) (Entry, error)     { return c.src.Entry(id) }
func (c *Catalog) Detail(id string) (Detail, error)   { return c.src.Detail(id) }
func (c *Catalog) IconPath(id string) (string, error) { return c.src.IconPath(id) }
func (c *Catalog) ScreenshotPath(id string, i int) (string, error) {
	return c.src.ScreenshotPath(id, i)
}
func (c *Catalog) Load(id string) (*manifest.Manifest, []byte, error) { return c.src.Load(id) }

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
