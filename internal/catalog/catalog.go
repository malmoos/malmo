// Package catalog loads app manifests from a directory tree. v1 catalog is
// hand-curated by malmo (APP_STORE.md); the signed-JSON remote catalog is a
// follow-up. Layout: <root>/<manifest_id>/{manifest.yml, <compose_file>}.
package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/malmo/malmo/internal/manifest"
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

// Entry is the store-facing summary of one available app.
type Entry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
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
		out = append(out, Entry{ID: man.ID, Name: man.Name, Version: man.Version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
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
