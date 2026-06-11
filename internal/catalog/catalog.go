// Package catalog loads app manifests from a directory tree. v1 catalog is
// hand-curated by molma (APP_STORE.md); the signed-JSON remote catalog is a
// follow-up. Layout: <root>/<manifest_id>/{manifest.yml, <compose_file>}.
package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/molmaos/molma/internal/manifest"
)

// ErrNotFound is returned by Load when no manifest exists for the id (the
// directory or manifest.yml is absent). It is deliberately distinct from a
// manifest that exists but fails to parse or is missing its compose file:
// those are integrity errors a curated catalog should never ship, so the API
// maps ErrNotFound to 404 and every other Load error to 500. Follows the
// "typed errors at boundaries" rule (CLAUDE.md) — same shape as store.ErrNotFound.
var ErrNotFound = errors.New("catalog: manifest not found")

type Catalog struct{ root string }

func New(root string) *Catalog { return &Catalog{root: root} }

// Entry is the store-facing summary of one available app. It carries exactly
// what the browse grid needs to render a card without a second fetch
// (APP_STORE.md # Catalog schema): the identity, the one-liner, the categories,
// and an icon URL. The detail page fetches Detail for the rest.
type Entry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`

	// ShortDescription is the one-line tagline (manifest description.short).
	ShortDescription string `json:"short_description,omitempty"`
	// Categories group the app in the store (manifest categories).
	Categories []string `json:"categories,omitempty"`
	// IconURL points at the brain's icon asset route for this app, set only when
	// the manifest declares an icon. Empty ⇒ the store falls back to a glyph.
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

// entryFor builds the grid Entry from a parsed manifest.
func entryFor(man *manifest.Manifest) Entry {
	e := Entry{
		ID:               man.ID,
		Name:             man.Name,
		Version:          man.Version,
		ShortDescription: man.Description.Short,
		Categories:       man.Categories,
		IconGlyph:        man.IconGlyph,
		Footprint:        man.Footprint(),
	}
	if man.Icon != "" {
		e.IconURL = iconURL(man.ID)
	}
	return e
}

// iconURL / screenshotURL are the brain-served asset routes the store loads
// directly in <img> tags (APP_STORE.md # Catalog schema). Kept here so the URL
// shape lives next to the path-resolution helpers that serve them.
func iconURL(id string) string { return "/api/v1/catalog/" + id + "/icon" }
func screenshotURL(id string, i int) string {
	return fmt.Sprintf("/api/v1/catalog/%s/screenshots/%d", id, i)
}

func (c *Catalog) List() ([]Entry, error) {
	dirs, err := os.ReadDir(c.root)
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	var out []Entry
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		man, _, err := c.Load(d.Name())
		if err != nil {
			continue // skip malformed entries
		}
		out = append(out, entryFor(man))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Detail returns the full detail-page view of one app. ErrNotFound (mapped to
// 404 by the API) when the manifest doesn't exist; other Load errors are
// integrity failures a curated catalog shouldn't ship and map to 500.
func (c *Catalog) Detail(manifestID string) (Detail, error) {
	man, _, err := c.Load(manifestID)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{
		Entry:           entryFor(man),
		LongDescription: man.Description.Long,
		License:         man.License,
		ChangelogURL:    man.ChangelogURL,
	}
	if man.Author != (manifest.Author{}) {
		d.Author = &man.Author
	}
	if man.Links != (manifest.Links{}) {
		d.Links = &man.Links
	}
	for i := range man.Screenshots {
		d.ScreenshotURLs = append(d.ScreenshotURLs, screenshotURL(man.ID, i))
	}
	return d, nil
}

// IconPath resolves the on-disk path of an app's icon for serving. ErrNotFound
// when the manifest declares no icon (or the app is unknown).
func (c *Catalog) IconPath(manifestID string) (string, error) {
	man, _, err := c.Load(manifestID)
	if err != nil {
		return "", err
	}
	if man.Icon == "" {
		return "", fmt.Errorf("%w: %q has no icon", ErrNotFound, manifestID)
	}
	return c.assetPath(manifestID, man.Icon)
}

// ScreenshotPath resolves the on-disk path of the i-th screenshot (manifest
// order, 0-based). ErrNotFound when the index is out of range or the app is
// unknown.
func (c *Catalog) ScreenshotPath(manifestID string, i int) (string, error) {
	man, _, err := c.Load(manifestID)
	if err != nil {
		return "", err
	}
	if i < 0 || i >= len(man.Screenshots) {
		return "", fmt.Errorf("%w: %q screenshot %d", ErrNotFound, manifestID, i)
	}
	return c.assetPath(manifestID, man.Screenshots[i])
}

// assetPath resolves a manifest-declared package-relative path (e.g.
// "./icon.png") to an absolute path inside the app's catalog directory,
// rejecting anything that would escape it (path traversal). The asset must
// exist on disk.
func (c *Catalog) assetPath(manifestID, rel string) (string, error) {
	dir := filepath.Join(c.root, manifestID)
	// Treat rel as rooted at the app dir: Clean("/"+rel) collapses any ".."
	// against that root, then Join re-bases it under dir. The prefix check is a
	// belt-and-braces guard against symlink-free escapes.
	full := filepath.Join(dir, filepath.Clean("/"+rel))
	if full != dir && !strings.HasPrefix(full, dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q asset escapes catalog dir", ErrNotFound, manifestID)
	}
	info, err := os.Stat(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %q asset %q", ErrNotFound, manifestID, rel)
		}
		return "", fmt.Errorf("catalog: stat asset %q for %q: %w", rel, manifestID, err)
	}
	// Regular files only: os.Stat succeeds on a directory too, and http.ServeFile
	// would then serve a listing — leaking the catalog dir structure.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q asset %q is not a regular file", ErrNotFound, manifestID, rel)
	}
	return full, nil
}

// Load returns the parsed manifest and the verbatim compose bytes.
func (c *Catalog) Load(manifestID string) (*manifest.Manifest, []byte, error) {
	dir := filepath.Join(c.root, manifestID)
	manBytes, err := os.ReadFile(filepath.Join(dir, "manifest.yml"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("%w: %q", ErrNotFound, manifestID)
		}
		return nil, nil, fmt.Errorf("catalog: read manifest for %q: %w", manifestID, err)
	}
	man, err := manifest.Parse(manBytes)
	if err != nil {
		return nil, nil, err
	}
	composeBytes, err := os.ReadFile(filepath.Join(dir, man.ComposeFile))
	if err != nil {
		return nil, nil, fmt.Errorf("catalog: compose %q for %q: %w", man.ComposeFile, manifestID, err)
	}
	return man, composeBytes, nil
}
