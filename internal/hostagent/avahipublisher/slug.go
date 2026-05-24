// Package avahipublisher — slug validation shared across all platforms.
package avahipublisher

import "regexp"

// slugRE is the valid slug pattern — same character class the catalog/manifest
// layer enforces. Defensive: rejects injection attempts before we ever touch
// DBus or the filesystem.
var slugRE = regexp.MustCompile(`^[a-z0-9-]+$`)
