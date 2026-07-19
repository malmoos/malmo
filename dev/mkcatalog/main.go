// Command mkcatalog generates a control-plane catalog snapshot (the /catalog/sync
// wire format) from a single on-disk app package. It pre-seeds the brain's
// last-good cache with that snapshot: the brain's remote catalog client loads it
// at boot exactly as it would a synced-then-offline snapshot, and installs the app
// from it (internal/catalog/remote.go # loadCache). This exercises the real remote
// read path (verify → project → Load) with no catalog/ directory in the image.
//
// Two callers:
//   - `make dev-app APP=<id>` — the native dev inner loop for authoring/curating a
//     store app against the brain post-catalog-cutover (cloud #62; store #22). It
//     reads apps/<id>/manifest.yml + compose straight from a store checkout, so it
//     needs no verdict on the app (unlike the cloud publish tool, catalog-sync,
//     which serves only listed: true records) — you boot the app to *decide* its
//     verdict. The Makefile points MALMO_CATALOG_URL at an inert address so the
//     background sync can't overwrite the seed with the real published catalog.
//   - dev/test-qemu/bootstrap.sh — the air-gapped QEMU full-stack lane, which
//     can't reach a control plane (restrict=on), seeds a whoami snapshot at
//     image-build time.
//
// Display-only fidelity: it fills the inline card fields (name, descriptions,
// author, license, links, categories, footprint) but not icon_file/screenshots —
// those are proxied from the control plane, and the seed path has no asset server,
// so an authored icon renders as its glyph fallback. For full visual QA run a
// local control plane instead (dev/cloud).
//
// The App / CatalogFile shapes here mirror internal/catalog/wire.go (which itself
// mirrors the cloud published shape) byte-for-byte: same fields, order, and JSON
// tags, reusing the internal/manifest display types. That is what makes the
// integrity digest reproduce — the brain recomputes SHA-256 over json.Marshal of
// the parsed app index and checks it against IndexSHA256. Keep this in sync with
// wire.go; a drift is caught when the brain rejects the snapshot at boot (and by
// internal/catalog's TestVerifyRealSnapshot-style guards).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/malmoos/malmo/internal/manifest"
)

type catalogFile struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	StoreRef      string    `json:"store_ref,omitempty"`
	IndexSHA256   string    `json:"index_sha256"`
	Apps          []app     `json:"apps"`
}

type app struct {
	ID               string                       `json:"id"`
	Name             string                       `json:"name"`
	Version          string                       `json:"version"`
	ShortDescription string                       `json:"short_description,omitempty"`
	LongDescription  string                       `json:"long_description,omitempty"`
	Categories       []string                     `json:"categories,omitempty"`
	IconGlyph        string                       `json:"icon_glyph,omitempty"`
	Author           *manifest.Author             `json:"author,omitempty"`
	License          string                       `json:"license,omitempty"`
	Links            *manifest.Links              `json:"links,omitempty"`
	ChangelogURL     string                       `json:"changelog_url,omitempty"`
	Footprint        manifest.Footprint           `json:"footprint"`
	IconFile         string                       `json:"icon_file,omitempty"`
	Screenshots      []string                     `json:"screenshots,omitempty"`
	Environments     []string                     `json:"environments"`
	Featured         bool                         `json:"featured,omitempty"`
	Rank             *int                         `json:"rank,omitempty"`
	Manifest         string                       `json:"manifest"`
	Compose          string                       `json:"compose"`
	Images           map[string]manifest.ImageRef `json:"images,omitempty"`
}

func main() {
	var (
		pkgDir  = flag.String("pkg", "", "app package directory (contains manifest.yml + compose file)")
		envList = flag.String("environments", "appliance,hosted", "comma-separated environments the app is visible in")
		out     = flag.String("out", "", "output snapshot path (default: stdout)")
	)
	flag.Parse()
	if *pkgDir == "" {
		fatal("mkcatalog: -pkg is required")
	}

	manBytes, err := os.ReadFile(filepath.Join(*pkgDir, "manifest.yml"))
	if err != nil {
		fatal("read manifest: %v", err)
	}
	man, err := manifest.Parse(manBytes)
	if err != nil {
		fatal("parse manifest: %v", err)
	}
	composeBytes, err := os.ReadFile(filepath.Join(*pkgDir, man.ComposeFile))
	if err != nil {
		fatal("read compose %q: %v", man.ComposeFile, err)
	}

	a := app{
		ID:               man.ID,
		Name:             man.Name,
		Version:          man.Version,
		ShortDescription: man.Description.Short,
		LongDescription:  man.Description.Long,
		Categories:       man.Categories,
		IconGlyph:        man.IconGlyph,
		License:          man.License,
		ChangelogURL:     man.ChangelogURL,
		Footprint:        man.Footprint(),
		Environments:     splitEnvs(*envList),
		Manifest:         string(manBytes),
		Compose:          string(composeBytes),
	}
	// Carry the author/links display metadata too, so the store card an author
	// eyeballs during a curation boot is the real one. Assets (icon_file /
	// screenshots) are deliberately omitted: the box proxies those from the
	// control plane, and the seed path has no asset server behind the inert URL,
	// so a filename here would only 404 (docs/dev/authoring-apps-with-an-agent.md).
	if man.Author != (manifest.Author{}) {
		a.Author = &man.Author
	}
	if man.Links != (manifest.Links{}) {
		a.Links = &man.Links
	}

	apps := []app{a}
	digest, err := indexDigest(apps)
	if err != nil {
		fatal("digest: %v", err)
	}
	file := catalogFile{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().UTC(),
		IndexSHA256:   digest,
		Apps:          apps,
	}
	b, err := json.Marshal(file)
	if err != nil {
		fatal("marshal snapshot: %v", err)
	}
	if *out == "" || *out == "-" {
		os.Stdout.Write(b)
		return
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fatal("write %q: %v", *out, err)
	}
}

func indexDigest(apps []app) (string, error) {
	b, err := json.Marshal(apps)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func splitEnvs(s string) []string {
	var out []string
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
