package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// DefaultSeedPath is the well-known, root-owned path the hosted cloud image's
// first-boot unit materializes the provisioning seed to (ENVIRONMENT.md #
// Provisioning & first-boot). The brain makes it overridable via
// MALMO_SEED_PATH for tests; in the appliance profile no seed exists and the
// path is never read.
const DefaultSeedPath = "/var/lib/malmo/seed.json"

// ErrSeedAbsent is returned by ReadSeed when no seed file exists at the path.
// On a hosted box this is the "provisioned without a seed" case: the brain logs
// it and stays pre-setup rather than crashing (a hosted box must never silently
// fall back to the appliance's open-/setup trust). It is distinct from a
// malformed/incomplete seed, which is a hard error worth surfacing.
var ErrSeedAbsent = errors.New("seed absent")

// Seed is the hosted-profile first-boot provisioning data (ENVIRONMENT.md #
// Provisioning). It replaces the appliance's "whoever is physically at the box
// during first boot" trust: the control plane allocates the box-id and a
// one-time admin-bootstrap secret at provision time and injects them
// cloud-init-style, so only the holder of the secret can create the first admin.
//
// Delivered as JSON at DefaultSeedPath. The test lane materializes it from a
// systemd credential over SMBIOS; a real cloud's metadata / config-drive maps
// onto the same first-boot materialization.
type Seed struct {
	// BoxID is the box's permanent identity in `base-suffix` form (e.g.
	// "cindy-fox"), allocated at provision and frozen for the life of the
	// install (MALMO_NETWORK.md). The brain persists and surfaces it.
	BoxID string `json:"box_id"`
	// AdminBootstrapSecret is the one-time secret that gates first-admin
	// creation on /setup. The brain stores only its hash, never the plaintext.
	AdminBootstrapSecret string `json:"admin_bootstrap_secret"`
	// Enrollment carries the `*.<box-id>.malmo.network` DNS-01 credentials.
	// Reserved for C3b (seeded enrollment + wildcard cert), which depends on the
	// unbuilt cloud enrollment API — captured here so the seed contract is whole,
	// but deliberately unconsumed by C3a.
	Enrollment json.RawMessage `json:"enrollment,omitempty"`
}

// ReadSeed reads and validates the provisioning seed at path. A missing file
// returns ErrSeedAbsent (the expected hosted "no seed yet" case). A present but
// unreadable, malformed, or incomplete seed (missing box-id or bootstrap
// secret) returns a descriptive error — a mis-provisioned hosted box should be
// loud, not silently degraded. Surrounding whitespace on the string fields is
// trimmed.
func ReadSeed(path string) (Seed, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Seed{}, ErrSeedAbsent
		}
		return Seed{}, fmt.Errorf("read seed: %w", err)
	}
	var s Seed
	if err := json.Unmarshal(b, &s); err != nil {
		return Seed{}, fmt.Errorf("parse seed: %w", err)
	}
	s.BoxID = strings.TrimSpace(s.BoxID)
	s.AdminBootstrapSecret = strings.TrimSpace(s.AdminBootstrapSecret)
	if s.BoxID == "" {
		return Seed{}, errors.New("seed missing box_id")
	}
	if s.AdminBootstrapSecret == "" {
		return Seed{}, errors.New("seed missing admin_bootstrap_secret")
	}
	return s, nil
}
