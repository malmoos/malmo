// Command mkcatalog generates a control-plane catalog snapshot (the /catalog/sync
// wire format) from one or more on-disk app packages, for the air-gapped QEMU
// full-stack lane (dev/test-qemu). The lane can't reach the real control plane
// (restrict=on), so instead of running a stub catalog server it pre-seeds the
// brain's last-good cache with this snapshot: the brain's remote catalog client
// loads it at boot exactly as it would a synced-then-offline snapshot, and installs
// the app from it (internal/catalog/remote.go # loadCache). This keeps the e2e
// meaningful — the real remote read path (verify → project → Load) is exercised —
// without a catalog/ directory in the image.
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
		Categories:       man.Categories,
		IconGlyph:        man.IconGlyph,
		Footprint:        man.Footprint(),
		Environments:     splitEnvs(*envList),
		Manifest:         string(manBytes),
		Compose:          string(composeBytes),
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
