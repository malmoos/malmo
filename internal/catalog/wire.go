package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/malmoos/malmo/internal/manifest"
)

// wire.go mirrors the control plane's published catalog wire format so a box can
// consume the /catalog/sync snapshot as a thin client (cloud specs/CATALOG.md #
// Consume). The shapes here are a byte-faithful mirror of the cloud
// internal/catalog CatalogFile / App (../cloud internal/catalog/published.go):
// same fields, same JSON tags, same declaration order — because the integrity
// digest is computed over json.Marshal of the app index, so the box must
// re-marshal to the identical bytes to reproduce (and verify) it. The seed
// contract aside, this is the box↔cloud catalog contract; a breaking change to it
// is coordinated across both repos like any two-side change (cloud CLAUDE.md).
//
// The four nested display shapes (Author, Links, Footprint, ImageRef) are the os
// manifest types the cloud itself mirrored, so reusing them here is what
// reproduces the cloud's marshalling exactly — not a coincidental match.

// wireSchemaVersion is the published-catalog wire format this box can read. It
// tracks the cloud's catalog.SchemaVersion; a snapshot stamped with anything else
// is refused at verify (a format the box can't project), the same staleness guard
// the cloud designed the version stamp for.
const wireSchemaVersion = 1

// catalogFile is the whole published catalog as served byte-for-byte by GET
// /catalog/sync — the versioned index the box caches last-good and projects
// locally. Mirror of cloud catalog.CatalogFile.
type catalogFile struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	StoreRef      string    `json:"store_ref,omitempty"`
	// IndexSHA256 is the hex SHA-256 over the canonical JSON of Apps; verify
	// recomputes it. It catches a truncated or corrupted snapshot; authenticity
	// (that the bytes came from the real control plane) is provided by TLS on the
	// fetch, so there is no separate signature (owner decision, cloud #62).
	IndexSHA256 string    `json:"index_sha256"`
	Apps        []wireApp `json:"apps"`
}

// wireApp is one published app: the display metadata the box store surfaces plus
// the verbatim manifest.yml / compose.yml the box install path re-parses. Mirror
// of cloud catalog.App — the field order is load-bearing (see the digest note
// above). Featured/Rank/Images are carried for wire fidelity (they participate in
// the digest) but the box's Entry/Detail do not surface them.
type wireApp struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Version          string             `json:"version"`
	ShortDescription string             `json:"short_description,omitempty"`
	LongDescription  string             `json:"long_description,omitempty"`
	Categories       []string           `json:"categories,omitempty"`
	IconGlyph        string             `json:"icon_glyph,omitempty"`
	Author           *manifest.Author   `json:"author,omitempty"`
	License          string             `json:"license,omitempty"`
	Links            *manifest.Links    `json:"links,omitempty"`
	ChangelogURL     string             `json:"changelog_url,omitempty"`
	Footprint        manifest.Footprint `json:"footprint"`

	// IconFile / Screenshots are the asset filenames under the control plane's
	// per-app assets tree (e.g. "icon.png", "screenshots/0.png"), which the box
	// proxies+caches through its own /api/v1/catalog asset routes.
	IconFile    string   `json:"icon_file,omitempty"`
	Screenshots []string `json:"screenshots,omitempty"`

	// Environments is the per-app visibility list ("appliance", "hosted"): the box
	// shows an app iff this contains the box's own environment.
	Environments []string `json:"environments"`
	Featured     bool     `json:"featured,omitempty"`
	Rank         *int     `json:"rank,omitempty"`

	// Manifest / Compose are the verbatim manifest.yml / compose.yml bytes — the
	// install payload the box re-parses with its own manifest.Parse, staying the
	// sole enforcer of the manifest contract (cloud specs/CATALOG.md # The shape).
	Manifest string                       `json:"manifest"`
	Compose  string                       `json:"compose"`
	Images   map[string]manifest.ImageRef `json:"images,omitempty"`
}

// indexDigest is the hex SHA-256 over the canonical JSON of the app index — the
// box side of the cloud's IndexDigest (../cloud internal/catalog/integrity.go).
// encoding/json is deterministic for this input (struct fields serialize in
// declaration order, map keys sort), so a byte-faithful mirror of the App shape
// reproduces the exact digest the sync tool stamped.
func indexDigest(apps []wireApp) (string, error) {
	b, err := json.Marshal(apps)
	if err != nil {
		return "", fmt.Errorf("marshal catalog index: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// verify refuses a snapshot the box can't trust: a schema version it can't read,
// or an index digest that doesn't match the stamped one (a truncated or corrupted
// fetch, or a tampered cache file). It is an integrity check; authenticity comes
// from TLS on the fetch (no separate signature — cloud #62). Called on every
// fetched and every cache-loaded snapshot before it is projected, so a bad snapshot
// never becomes the read source.
func (f catalogFile) verify() error {
	if f.SchemaVersion != wireSchemaVersion {
		return fmt.Errorf("catalog schema version %d, want %d", f.SchemaVersion, wireSchemaVersion)
	}
	got, err := indexDigest(f.Apps)
	if err != nil {
		return err
	}
	if got != f.IndexSHA256 {
		return fmt.Errorf("catalog index digest mismatch: snapshot has %q, recomputed %q", f.IndexSHA256, got)
	}
	return nil
}

// parseSnapshot unmarshals raw /catalog/sync bytes and verifies them in one step —
// the only way a snapshot enters the box, whether from the network or the
// last-good cache file.
func parseSnapshot(data []byte) (catalogFile, error) {
	var f catalogFile
	if err := json.Unmarshal(data, &f); err != nil {
		return catalogFile{}, fmt.Errorf("parse catalog snapshot: %w", err)
	}
	if err := f.verify(); err != nil {
		return catalogFile{}, err
	}
	return f, nil
}
