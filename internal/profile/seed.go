package profile

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// Enrollment carries the per-box acme-dns account the box's Caddy uses to
	// obtain and renew its `*.<box-id>.malmo.network` wildcard cert via ACME
	// DNS-01 (C3b; ENVIRONMENT.md # Networking & discovery). The JSON shape
	// mirrors the cloud producer's wire contract byte-for-byte (cloud
	// internal/seed.EnrollmentCredentials) — the two repos meet at this format,
	// not a shared Go type. It is optional at the seed-parse layer: a hosted box
	// seeded without it simply can't get a cert (the cert pass logs and skips);
	// the box-id + bootstrap gate still work. omitempty so an appliance/un-enrolled
	// seed round-trips without an empty block.
	Enrollment EnrollmentCredentials `json:"enrollment,omitempty"`
}

// EnrollmentCredentials is a per-box joohoi/acme-dns account (cloud
// specs/ARCHITECTURE.md Contract 2). The credential can set only this box's own
// `_acme-challenge` TXT, so Caddy renews the wildcard cert directly against
// acme-dns with no per-renewal cloud call. The acme-dns server's API endpoint is
// a box-side constant (the same for every box), not part of this seeded payload.
type EnrollmentCredentials struct {
	// Subdomain is the acme-dns account subdomain the box's
	// `_acme-challenge.<box-id>` CNAME points at (the CNAME is set cloud-side in
	// Route53 at provision).
	Subdomain string `json:"subdomain"`
	// Username and Password authenticate the box to acme-dns for TXT updates.
	Username string `json:"username"`
	Password string `json:"password"`
}

// Complete reports whether all three acme-dns fields are present. An incomplete
// (or absent) block means the box cannot configure DNS-01, so the hosted cert
// pass logs and skips rather than handing Caddy a half-configured issuer.
func (e EnrollmentCredentials) Complete() bool {
	return e.Subdomain != "" && e.Username != "" && e.Password != ""
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
		if os.IsNotExist(err) {
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
