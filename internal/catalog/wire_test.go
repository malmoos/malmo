package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestVerifyRealSnapshot is the box side of the box↔cloud digest contract: a
// byte-faithful mirror of the cloud App shape must reproduce the exact index
// digest the sync tool stamped, or the box would reject every real snapshot.
// testdata/snapshot.json is a pinned copy of the control plane's published
// dist/catalog.json (../cloud internal/catalog/dist). If the wire shape here drifts
// from the cloud's, this fails — which is the point: the two are one contract.
func TestVerifyRealSnapshot(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseSnapshot(data)
	if err != nil {
		t.Fatalf("real snapshot must parse and verify: %v", err)
	}
	if len(f.Apps) == 0 {
		t.Fatal("fixture snapshot carries no apps")
	}
	// Re-marshalling the parsed index must reproduce the stamped digest byte for
	// byte — the invariant the whole thin-client integrity check rests on.
	got, err := indexDigest(f.Apps)
	if err != nil {
		t.Fatal(err)
	}
	if got != f.IndexSHA256 {
		t.Fatalf("digest mismatch: recomputed %q, stamped %q", got, f.IndexSHA256)
	}
}

// TestVerifyRejects covers the two ways a snapshot is refused before it can become
// the read source: a schema version the box can't read, and a digest that doesn't
// match the bytes (truncation / corruption / tamper).
func TestVerifyRejects(t *testing.T) {
	base := catalogFile{
		SchemaVersion: wireSchemaVersion,
		Apps:          []wireApp{{ID: "a", Name: "A", Version: "1"}},
	}
	digest, err := indexDigest(base.Apps)
	if err != nil {
		t.Fatal(err)
	}
	base.IndexSHA256 = digest
	if err := base.verify(); err != nil {
		t.Fatalf("well-formed snapshot must verify: %v", err)
	}

	t.Run("wrong schema", func(t *testing.T) {
		bad := base
		bad.SchemaVersion = wireSchemaVersion + 1
		if err := bad.verify(); err == nil {
			t.Fatal("want error for unreadable schema version")
		}
	})

	t.Run("digest mismatch", func(t *testing.T) {
		bad := base
		bad.Apps = append([]wireApp(nil), base.Apps...)
		bad.Apps[0].Name = "tampered" // digest no longer matches the stamped one
		if err := bad.verify(); err == nil {
			t.Fatal("want error for digest mismatch")
		}
	})

	t.Run("parseSnapshot rejects truncated json", func(t *testing.T) {
		b, _ := json.Marshal(base)
		if _, err := parseSnapshot(b[:len(b)/2]); err == nil {
			t.Fatal("want error for truncated snapshot body")
		}
	})
}
