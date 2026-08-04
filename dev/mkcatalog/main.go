// Command mkcatalog generates a control-plane catalog snapshot (the /catalog/sync
// wire format) from one or more on-disk app packages, optionally with a curated
// landing page. It pre-seeds the brain's last-good cache with that snapshot: the
// brain's remote catalog client loads it at boot exactly as it would a
// synced-then-offline snapshot, and installs an app from it
// (internal/catalog/remote.go # loadCache). This exercises the real remote read
// path (verify → project → Load) with no catalog/ directory in the image.
//
// Two callers:
//
//   - `make dev-app APP=<id>` (single app) / `make seed-catalog APPS="<id> ..."
//     [HOMEFILE=<path/to/home.yml>]` (several apps, plus an optional curated
//     landing): the native dev inner loop for authoring/curating store apps
//     against the brain post-catalog-cutover (cloud #62; store #22). It reads
//     apps/<id>/manifest.yml + compose straight from a store checkout, so it
//     needs no verdict on the app (unlike the cloud publish tool, catalog-sync,
//     which serves only listed: true records) — you boot the app to *decide*
//     its verdict. The Makefile points MALMO_CATALOG_URL at an inert address so
//     the background sync can't overwrite the seed with the real published
//     catalog.
//
//   - dev/test-qemu/bootstrap.sh: the air-gapped QEMU full-stack lane, which
//     can't reach a control plane (restrict=on), seeds a whoami snapshot at
//     image-build time (single -pkg, no -home — unchanged).
//
// Display-only fidelity: it fills the inline card fields (name, descriptions,
// author, license, links, categories, footprint) but not icon_file/screenshots —
// those are proxied from the control plane, and the seed path has no asset server,
// so an authored icon renders as its glyph fallback. For full visual QA run a
// local control plane instead (dev/cloud).
//
// The App / CatalogFile / Home shapes here mirror internal/catalog/wire.go (which
// itself mirrors the cloud published shape) byte-for-byte: same fields, order, and
// JSON tags, reusing the internal/manifest display types. That is what makes the
// integrity digest reproduce — the brain recomputes SHA-256 over json.Marshal of
// the parsed app index and checks it against IndexSHA256 (the home block plays no
// part in that digest, matching wire.go). Keep this in sync with wire.go; a drift
// is caught when the brain rejects the snapshot at boot (and by internal/catalog's
// TestVerifyRealSnapshot-style guards).
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
	"gopkg.in/yaml.v3"
)

type catalogFile struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   time.Time    `json:"generated_at"`
	StoreRef      string       `json:"store_ref,omitempty"`
	IndexSHA256   string       `json:"index_sha256"`
	Apps          []app        `json:"apps"`
	Home          wireHomePage `json:"home"`
}

// wireHomePage / wireHomeGroup mirror internal/catalog/wire.go's wireHomePage /
// wireHomeGroup (themselves a mirror of cloud's HomePage / HomeGroup), field for
// field and tag for tag.
type wireHomePage struct {
	Spotlight string          `json:"spotlight"`
	Groups    []wireHomeGroup `json:"groups"`
}

type wireHomeGroup struct {
	Category string   `json:"category"`
	Apps     []string `json:"apps"`
}

// homeYAML is the shape of the store repo's home.yml (store/home.yml): a
// spotlight app id plus ordered category groups. Parsed straight into
// wireHomePage via the same field names — home.yml IS the wire shape here (the
// control plane's sync tool carries it verbatim too).
type homeYAML struct {
	Spotlight string `yaml:"spotlight"`
	Groups    []struct {
		Category string   `yaml:"category"`
		Apps     []string `yaml:"apps"`
	} `yaml:"groups"`
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

// pkgList collects one or more -pkg flags, in the order given — flag.Value's Set
// is called once per occurrence, so a single -pkg (every call site today) behaves
// exactly as before, and a curated multi-app seed just passes it more than once.
type pkgList []string

func (p *pkgList) String() string { return strings.Join(*p, ",") }
func (p *pkgList) Set(v string) error {
	*p = append(*p, v)
	return nil
}

func main() {
	var (
		pkgs    pkgList
		envList = flag.String("environments", "appliance,hosted", "comma-separated environments the app(s) are visible in")
		out     = flag.String("out", "", "output snapshot path (default: stdout)")
		homeArg = flag.String("home", "", "path to a store home.yml to carry as the snapshot's curated landing page (optional)")
	)
	flag.Var(&pkgs, "pkg", "app package directory (contains manifest.yml + compose file); repeatable")
	flag.Parse()
	if len(pkgs) == 0 {
		fatal("mkcatalog: at least one -pkg is required")
	}

	environments := splitEnvs(*envList)
	apps := make([]app, 0, len(pkgs))
	for _, pkgDir := range pkgs {
		apps = append(apps, loadApp(pkgDir, environments))
	}

	var home wireHomePage
	if *homeArg != "" {
		home = loadHome(*homeArg, apps)
	}

	digest, err := indexDigest(apps)
	if err != nil {
		fatal("digest: %v", err)
	}
	file := catalogFile{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().UTC(),
		IndexSHA256:   digest,
		Apps:          apps,
		Home:          home,
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

// loadApp parses one package directory's manifest + compose into the wire app
// shape, carrying the author/links display metadata too, so the store card an
// author eyeballs during a curation boot is the real one. Assets (icon_file /
// screenshots) are deliberately omitted: the box proxies those from the control
// plane, and the seed path has no asset server behind the inert URL, so a
// filename here would only 404 (docs/dev/authoring-apps-with-an-agent.md).
func loadApp(pkgDir string, environments []string) app {
	manBytes, err := os.ReadFile(filepath.Join(pkgDir, "manifest.yml"))
	if err != nil {
		fatal("read manifest: %v", err)
	}
	man, err := manifest.Parse(manBytes)
	if err != nil {
		fatal("parse manifest: %v", err)
	}
	composeBytes, err := os.ReadFile(filepath.Join(pkgDir, man.ComposeFile))
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
		Environments:     environments,
		Manifest:         string(manBytes),
		Compose:          string(composeBytes),
	}
	if man.Author != (manifest.Author{}) {
		a.Author = &man.Author
	}
	if man.Links != (manifest.Links{}) {
		a.Links = &man.Links
	}
	return a
}

// loadHome parses a store home.yml and validates it against the seeded apps: any
// spotlight or group app id that isn't among the packages just seeded is a fatal
// error, not a silently-dropped slot — the whole point of exercising this locally
// is to catch that before a real store publish does (a real publish's admission
// check catches it there; this is the equivalent local guard).
func loadHome(path string, apps []app) wireHomePage {
	data, err := os.ReadFile(path)
	if err != nil {
		fatal("read home: %v", err)
	}
	var y homeYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		fatal("parse home %q: %v", path, err)
	}

	seeded := make(map[string]bool, len(apps))
	for _, a := range apps {
		seeded[a.ID] = true
	}
	requireSeeded := func(id string) {
		if !seeded[id] {
			fatal("home %q names app %q, which was not seeded with -pkg (seed it too, or drop it from home.yml)", path, id)
		}
	}

	home := wireHomePage{Spotlight: y.Spotlight}
	if y.Spotlight != "" {
		requireSeeded(y.Spotlight)
	}
	for _, g := range y.Groups {
		for _, id := range g.Apps {
			requireSeeded(id)
		}
		home.Groups = append(home.Groups, wireHomeGroup{Category: g.Category, Apps: g.Apps})
	}
	return home
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
