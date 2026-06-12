package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/malmoos/malmo/internal/manifest"
)

// composeChecker is the admission seam. Production passes admission.Check
// (syntax via `docker compose config -q` + the structural rejection rules);
// tests pass admission.CheckStructure so they stay hermetic — shelling out to
// the Docker daemon would turn unit tests into flaky integration tests. Mirrors
// resolve's imageSizer seam.
type composeChecker func(ctx context.Context, composeBytes []byte) error

// check is the author's one-shot "would this install?" command: it runs the
// schema lint (lint) and then the compose admission policy (admission.Check) on
// the sibling compose, so a single green `manifest check` proves BOTH the
// schema and the structural compose rules — the two checks the admission policy
// (APP_LIFECYCLE.md) and the manifest schema (APP_MANIFEST.md) split between
// them. lint alone is non-strict and does NOT run admission; this closes that
// gap so authors (and the authoring agent) never hand-eyeball admission.go.
func check(ctx context.Context, admit composeChecker, manifestPath string) error {
	if err := lint(manifestPath); err != nil {
		return err // lint errors already name the field/slug/compose problem
	}

	// lint proved the manifest parses and the compose resolves + parses; re-read
	// the verbatim compose bytes and run them through admission.
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	man, err := manifest.Parse(data)
	if err != nil {
		return err
	}
	composePath := filepath.Join(filepath.Dir(manifestPath), man.ComposeFile)
	composeData, err := os.ReadFile(composePath)
	if err != nil {
		return fmt.Errorf("compose_file %q: %w", man.ComposeFile, err)
	}
	if err := admit(ctx, composeData); err != nil {
		return err // admission.Error messages already name the offending service + field
	}
	return nil
}
