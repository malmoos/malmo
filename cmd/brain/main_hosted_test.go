package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/malmoos/malmo/internal/profile"
	"github.com/malmoos/malmo/internal/store"
)

// fakeBoxMeta is an in-memory boxMetaStore for the hosted-seed ingestion tests.
// sets records the key write order so the commit-marker ordering (hash before
// box-id) can be asserted.
type fakeBoxMeta struct {
	m      map[string]string
	getErr map[string]error
	setErr map[string]error
	sets   []string
}

func newFakeBoxMeta() *fakeBoxMeta {
	return &fakeBoxMeta{m: map[string]string{}, getErr: map[string]error{}, setErr: map[string]error{}}
}

func (f *fakeBoxMeta) GetBoxMeta(key string) (string, error) {
	if err := f.getErr[key]; err != nil {
		return "", err
	}
	v, ok := f.m[key]
	if !ok {
		return "", store.ErrNotFound
	}
	return v, nil
}

func (f *fakeBoxMeta) SetBoxMeta(key, value string) error {
	if err := f.setErr[key]; err != nil {
		return err
	}
	f.m[key] = value
	f.sets = append(f.sets, key)
	return nil
}

func writeSeedFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	return p
}

const validSeedJSON = `{"box_id":"cindy-fox","admin_bootstrap_secret":"s3cr3t"}`

func TestLoadHostedEnvironment_ApplianceIsNoop(t *testing.T) {
	bm := newFakeBoxMeta()
	// A seed path that would error if read proves appliance never touches it.
	boxID, hash, _ := loadHostedEnvironment(profile.Appliance, bm, "/nonexistent/seed.json")
	if boxID != "" || hash != "" {
		t.Fatalf("appliance = (%q,%q); want empty", boxID, hash)
	}
	if len(bm.sets) != 0 {
		t.Errorf("appliance wrote box_meta: %v", bm.sets)
	}
}

func TestLoadHostedEnvironment_FirstBootIngestsSeed(t *testing.T) {
	bm := newFakeBoxMeta()
	seedPath := writeSeedFile(t, validSeedJSON)

	boxID, hash, _ := loadHostedEnvironment(profile.Hosted, bm, seedPath)
	if boxID != "cindy-fox" {
		t.Errorf("box_id = %q; want cindy-fox", boxID)
	}
	if want := sha256Hex("s3cr3t"); hash != want {
		t.Errorf("hash = %q; want %q", hash, want)
	}
	// Persisted, and never the plaintext secret.
	if bm.m[store.BoxMetaBoxID] != "cindy-fox" {
		t.Errorf("persisted box_id = %q", bm.m[store.BoxMetaBoxID])
	}
	if bm.m[store.BoxMetaBootstrapSecretHash] != sha256Hex("s3cr3t") {
		t.Errorf("persisted hash = %q", bm.m[store.BoxMetaBootstrapSecretHash])
	}
	if bm.m[store.BoxMetaBootstrapSecretHash] == "s3cr3t" {
		t.Error("plaintext secret persisted")
	}
	// Commit-marker ordering: hash must land before box-id.
	if len(bm.sets) != 2 || bm.sets[0] != store.BoxMetaBootstrapSecretHash || bm.sets[1] != store.BoxMetaBoxID {
		t.Errorf("write order = %v; want [hash, box_id]", bm.sets)
	}
}

// A box-id already persisted is the install's frozen identity: subsequent boots
// load it (and the stored hash) and ignore the seed entirely.
func TestLoadHostedEnvironment_FrozenIdentityIgnoresSeed(t *testing.T) {
	bm := newFakeBoxMeta()
	bm.m[store.BoxMetaBoxID] = "cindy-fox"
	bm.m[store.BoxMetaBootstrapSecretHash] = "storedhash"
	// A different seed on disk must NOT override the frozen identity.
	seedPath := writeSeedFile(t, `{"box_id":"rocky-owl","admin_bootstrap_secret":"other"}`)

	boxID, hash, _ := loadHostedEnvironment(profile.Hosted, bm, seedPath)
	if boxID != "cindy-fox" || hash != "storedhash" {
		t.Fatalf("frozen identity = (%q,%q); want (cindy-fox, storedhash)", boxID, hash)
	}
	if len(bm.sets) != 0 {
		t.Errorf("frozen-identity boot wrote box_meta: %v", bm.sets)
	}
}

func TestLoadHostedEnvironment_AbsentSeedStaysClosed(t *testing.T) {
	bm := newFakeBoxMeta()
	boxID, hash, _ := loadHostedEnvironment(profile.Hosted, bm, filepath.Join(t.TempDir(), "missing.json"))
	if boxID != "" || hash != "" {
		t.Fatalf("absent seed = (%q,%q); want empty (gate stays closed)", boxID, hash)
	}
	if len(bm.sets) != 0 {
		t.Errorf("absent seed wrote box_meta: %v", bm.sets)
	}
}

func TestLoadHostedEnvironment_MalformedSeedStaysClosed(t *testing.T) {
	bm := newFakeBoxMeta()
	seedPath := writeSeedFile(t, `{not valid json`)
	boxID, hash, _ := loadHostedEnvironment(profile.Hosted, bm, seedPath)
	if boxID != "" || hash != "" {
		t.Fatalf("malformed seed = (%q,%q); want empty", boxID, hash)
	}
	if len(bm.sets) != 0 {
		t.Errorf("malformed seed wrote box_meta: %v", bm.sets)
	}
}

// Defensive: the hash-before-box-id ordering makes a persisted box-id with no
// hash unreachable, but if it ever happens (the hash row gone, or a read error)
// the gate stays closed (empty hash ⇒ 503) rather than opening — and never
// loads a usable identity without its secret.
func TestLoadHostedEnvironment_FrozenIdentityMissingHashStaysClosed(t *testing.T) {
	bm := newFakeBoxMeta()
	bm.m[store.BoxMetaBoxID] = "cindy-fox" // box-id present, hash row absent
	boxID, hash, _ := loadHostedEnvironment(profile.Hosted, bm, "/nonexistent/seed.json")
	if boxID != "cindy-fox" {
		t.Errorf("box_id = %q; want cindy-fox (identity still frozen)", boxID)
	}
	if hash != "" {
		t.Errorf("hash = %q; want empty so the gate stays closed", hash)
	}
}

// A persist failure on the hash leaves the gate closed and never writes box-id —
// so the next boot re-ingests cleanly rather than seeing a box-id with no secret.
func TestLoadHostedEnvironment_HashPersistFailureStaysClosed(t *testing.T) {
	bm := newFakeBoxMeta()
	bm.setErr[store.BoxMetaBootstrapSecretHash] = errors.New("disk full")
	seedPath := writeSeedFile(t, validSeedJSON)

	boxID, hash, _ := loadHostedEnvironment(profile.Hosted, bm, seedPath)
	if boxID != "" || hash != "" {
		t.Fatalf("hash-persist failure = (%q,%q); want empty", boxID, hash)
	}
	if _, ok := bm.m[store.BoxMetaBoxID]; ok {
		t.Error("box-id persisted despite hash-persist failure")
	}
}

const seedWithEnrollmentJSON = `{"box_id":"cindy-fox","admin_bootstrap_secret":"s3cr3t","enrollment":{"subdomain":"abc-123","username":"u","password":"p"}}`

// First boot with a complete enrollment block: it is returned for the cert pass,
// persisted as JSON, and written *before* the box-id commit marker (so a crash
// mid-write re-ingests rather than stranding a box-id with no enrollment).
func TestLoadHostedEnvironment_FirstBootIngestsEnrollment(t *testing.T) {
	bm := newFakeBoxMeta()
	seedPath := writeSeedFile(t, seedWithEnrollmentJSON)

	boxID, _, enr := loadHostedEnvironment(profile.Hosted, bm, seedPath)
	if boxID != "cindy-fox" {
		t.Fatalf("box_id = %q; want cindy-fox", boxID)
	}
	if !enr.Complete() || enr.Subdomain != "abc-123" || enr.Username != "u" || enr.Password != "p" {
		t.Errorf("enrollment = %+v; want {abc-123 u p}", enr)
	}
	// Persisted, and ordered hash → enrollment → box-id (box-id is the marker).
	if len(bm.sets) != 3 || bm.sets[0] != store.BoxMetaBootstrapSecretHash ||
		bm.sets[1] != store.BoxMetaEnrollment || bm.sets[2] != store.BoxMetaBoxID {
		t.Errorf("write order = %v; want [hash, enrollment, box_id]", bm.sets)
	}
}

// A seed with no enrollment still provisions the gate; the cert pass is skipped
// (incomplete enrollment) and no enrollment row is written.
func TestLoadHostedEnvironment_FirstBootNoEnrollmentSkips(t *testing.T) {
	bm := newFakeBoxMeta()
	seedPath := writeSeedFile(t, validSeedJSON) // no enrollment block

	boxID, _, enr := loadHostedEnvironment(profile.Hosted, bm, seedPath)
	if boxID != "cindy-fox" {
		t.Fatalf("box_id = %q; want cindy-fox", boxID)
	}
	if enr.Complete() {
		t.Errorf("enrollment = %+v; want incomplete", enr)
	}
	if _, ok := bm.m[store.BoxMetaEnrollment]; ok {
		t.Error("enrollment row written for a seed with no enrollment")
	}
	if len(bm.sets) != 2 { // hash, box_id only
		t.Errorf("write order = %v; want [hash, box_id]", bm.sets)
	}
}

// Frozen-identity boot: the persisted enrollment is reloaded (the seed is
// ignored) so the cert pass can reconfigure Caddy without re-reading the seed.
func TestLoadHostedEnvironment_FrozenIdentityLoadsEnrollment(t *testing.T) {
	bm := newFakeBoxMeta()
	bm.m[store.BoxMetaBoxID] = "cindy-fox"
	bm.m[store.BoxMetaBootstrapSecretHash] = "storedhash"
	bm.m[store.BoxMetaEnrollment] = `{"subdomain":"abc-123","username":"u","password":"p"}`

	_, _, enr := loadHostedEnvironment(profile.Hosted, bm, "/nonexistent/seed.json")
	if !enr.Complete() || enr.Subdomain != "abc-123" {
		t.Errorf("reloaded enrollment = %+v; want {abc-123 u p}", enr)
	}
	if len(bm.sets) != 0 {
		t.Errorf("frozen-identity boot wrote box_meta: %v", bm.sets)
	}
}
