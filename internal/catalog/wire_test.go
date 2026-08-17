package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/malmoos/malmo/internal/manifest"
)

// updateFixture rewrites the fixture's stamped digest from its own apps array,
// so editing the shape by hand does not mean computing a SHA-256 by hand:
//
//	go test ./internal/catalog -run TestVerifyFixtureSnapshot -update
//
// It only ever touches the index_sha256 value. Every other byte of the fixture
// stays as authored, which is what keeps TestNoUnmodeledFields honest — see the
// note on hand-authoring there.
var updateFixture = flag.Bool("update", false, "rewrite testdata/snapshot.json's index_sha256 from its own apps array")

// fixturePath is the pinned snapshot both this file's fixture-reading tests use.
var fixturePath = filepath.Join("testdata", "snapshot.json")

// digestRe matches the fixture's stamped digest field, so -update can replace
// that one value without re-marshalling (and thereby reordering and reformatting)
// the hand-authored file around it.
var digestRe = regexp.MustCompile(`"index_sha256":\s*"[0-9a-f]*"`)

// TestVerifyFixtureSnapshot is the box side of the box↔cloud digest contract: a
// byte-faithful mirror of the cloud App shape must reproduce the exact index
// digest the sync tool stamps, or the box would reject every snapshot it is
// served.
//
// testdata/snapshot.json is a SYNTHETIC snapshot: fake apps, written here, in
// the published wire shape. It is not a copy of any catalog the control plane
// serves — app manifests and compose files are authored in the store, and this
// repo holds none of them (CLAUDE.md # Catalog apps). What the fixture pins is
// the SHAPE, which is all these tests need.
func TestVerifyFixtureSnapshot(t *testing.T) {
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if *updateFixture {
		var raw catalogFile
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		digest, err := indexDigest(raw.Apps)
		if err != nil {
			t.Fatal(err)
		}
		updated := digestRe.ReplaceAll(data, []byte(`"index_sha256": "`+digest+`"`))
		if !digestRe.Match(data) {
			t.Fatalf("%s has no index_sha256 field to update", fixturePath)
		}
		if err := os.WriteFile(fixturePath, updated, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s index_sha256 to %s", fixturePath, digest)
		data = updated
	}
	f, err := parseSnapshot(data)
	if err != nil {
		t.Fatalf("fixture snapshot must parse and verify (re-stamp it with -update): %v", err)
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

// jsonKeys returns the set of JSON object keys a struct type will unmarshal
// into, derived from its own `json:"..."` tags rather than hand-listed — a
// hand-list is one more place for the wire shape to drift out of sync with
// the actual struct. Fields tagged "-" are skipped.
func jsonKeys(t reflect.Type) map[string]bool {
	keys := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := tag
		if idx := strings.IndexByte(tag, ','); idx >= 0 {
			name = tag[:idx]
		}
		if name != "" {
			keys[name] = true
		}
	}
	return keys
}

// ignoredTopLevelKeys are published-catalog top-level fields the box
// deliberately does not model. Each entry records why, so widening this list
// is a decision on record, not a silent bypass of TestNoUnmodeledFields.
var ignoredTopLevelKeys = map[string]string{
	// os_capabilities_version is a publish-side provenance stamp: which
	// capability set the control plane admitted apps against when it built this
	// snapshot. It has no box-side meaning — the box enforces admission against
	// its own manifest.Version each time it parses one, not against a
	// catalog-wide stamp — so there is nothing for the box to do with it.
	"os_capabilities_version": "publish-side provenance stamp, no box-side meaning",
}

// TestNoUnmodeledFields asserts that the wire shape written down in the fixture
// and the wire shape the box's types model are the same set of keys. It parses
// the fixture into a generic map[string]any and fails on any top-level or
// per-app key that catalogFile / wireApp (plus the nested
// footprint/author/links/images shapes) do not declare a json tag for.
//
// Read what this does and does not catch. The fixture is hand-authored, so this
// is a check between two things kept in this repo: it catches a json tag renamed
// or dropped in wire.go without the pinned shape following, and it makes the
// shape the box believes in reviewable as one file. It CANNOT tell you that the
// published shape moved — the box does not hold a published snapshot to compare
// against, by design. Noticing that a newly published field is one the box does
// not model is a publish-side check, on the side that has the published
// snapshot.
//
// Because the fixture is hand-authored, keep it that way: never regenerate it
// from the Go types in this package. A fixture generated from the very structs
// it is checked against agrees with them always, and this test becomes a test of
// nothing. -update (see TestVerifyFixtureSnapshot) re-stamps the digest only,
// for that reason.
//
// When the box starts modelling a new field, add it here in the fixture too, so
// the shape stays written down in one readable place. When the box deliberately
// does not model a top-level field, record it in ignoredTopLevelKeys with the
// reason. Don't delete or weaken this test to make it pass.
func TestNoUnmodeledFields(t *testing.T) {
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	knownTop := jsonKeys(reflect.TypeOf(catalogFile{}))
	for key := range raw {
		if knownTop[key] || ignoredTopLevelKeys[key] != "" {
			continue
		}
		t.Errorf("the pinned fixture has top-level key %q that catalogFile (internal/catalog/wire.go) does not model: "+
			"either add a field for it there (and in dev/mkcatalog if mkcatalog should emit it too), or add it to "+
			"ignoredTopLevelKeys with a comment explaining why the box doesn't need it", key)
	}

	appsRaw, ok := raw["apps"].([]any)
	if !ok {
		t.Fatal("the pinned fixture: \"apps\" is missing or not an array")
	}
	if len(appsRaw) == 0 {
		t.Fatal("the pinned fixture: \"apps\" is empty, guard has nothing to check")
	}

	knownApp := jsonKeys(reflect.TypeOf(wireApp{}))
	knownFootprint := jsonKeys(reflect.TypeOf(manifest.Footprint{}))
	knownAuthor := jsonKeys(reflect.TypeOf(manifest.Author{}))
	knownLinks := jsonKeys(reflect.TypeOf(manifest.Links{}))
	knownImageRef := jsonKeys(reflect.TypeOf(manifest.ImageRef{}))

	checkNested := func(id, label string, obj map[string]any, known map[string]bool) {
		for key := range obj {
			if known[key] {
				continue
			}
			t.Errorf("app %q: %s has key %q that its wire type (internal/catalog/wire.go / internal/manifest) does not "+
				"model: add the field, or record why it's ignored", id, label, key)
		}
	}

	for _, a := range appsRaw {
		app, ok := a.(map[string]any)
		if !ok {
			t.Fatal("the pinned fixture: an \"apps\" entry is not an object")
		}
		id, _ := app["id"].(string)
		for key := range app {
			if knownApp[key] {
				continue
			}
			t.Errorf("app %q has key %q that wireApp (internal/catalog/wire.go) does not model: "+
				"either add a field for it there (and in dev/mkcatalog if mkcatalog should emit it too), or record it as a "+
				"deliberate omission with a comment", id, key)
		}
		if fp, ok := app["footprint"].(map[string]any); ok {
			checkNested(id, "footprint", fp, knownFootprint)
		}
		if au, ok := app["author"].(map[string]any); ok {
			checkNested(id, "author", au, knownAuthor)
		}
		if li, ok := app["links"].(map[string]any); ok {
			checkNested(id, "links", li, knownLinks)
		}
		if images, ok := app["images"].(map[string]any); ok {
			for imgRef, v := range images {
				if ir, ok := v.(map[string]any); ok {
					checkNested(id, fmt.Sprintf("images[%q]", imgRef), ir, knownImageRef)
				}
			}
		}
	}
}

// TestExternalCostsSurviveTheDigest is the byte-fidelity proof for the newest
// field on the wire. No published app declares a cost yet, so the pinned fixture
// cannot exercise it — this builds a snapshot that does, the way the control
// plane marshals one, and checks the box reproduces the digest over it and
// projects it onto the detail page.
//
// The digest is computed over json.Marshal of the app array, so a mirror that
// declared ExternalCosts in a different position than the control plane's App
// would still parse and still pass every field-level assertion, and only fail
// here. That is the failure this test exists to catch.
func TestExternalCostsSurviveTheDigest(t *testing.T) {
	apps := []wireApp{{
		ID: "openclaw", Name: "OpenClaw", Version: "1.0",
		Environments: []string{"appliance", "hosted"},
		ExternalCosts: []ExternalCost{{
			ID:              "model-access",
			Title:           "Model access",
			Description:     "You bring your own provider key and the provider bills you.",
			Required:        true,
			Estimate:        "$3 per million tokens (long agent runs use many times more)",
			EstimateChecked: "2026-08-10",
		}},
		Manifest: "id: openclaw\n", Compose: "services: {}\n",
	}}
	digest, err := indexDigest(apps)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(catalogFile{
		SchemaVersion: wireSchemaVersion,
		IndexSHA256:   digest,
		Apps:          apps,
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseSnapshot(raw)
	if err != nil {
		t.Fatalf("snapshot carrying external_costs must parse and verify: %v", err)
	}
	got := detailOfApp(&f.Apps[0])
	if len(got.ExternalCosts) != 1 {
		t.Fatalf("Detail.ExternalCosts = %+v, want the one declared cost", got.ExternalCosts)
	}
	c := got.ExternalCosts[0]
	if c.ID != "model-access" || !c.Required || c.EstimateChecked != "2026-08-10" {
		t.Errorf("Detail.ExternalCosts[0] = %+v", c)
	}
	if c.Estimate != "$3 per million tokens (long agent runs use many times more)" {
		t.Errorf("estimate not carried verbatim: %q", c.Estimate)
	}
	// The grid card must stay free of it: a cost is a paragraph of reading.
	entry, err := json.Marshal(entryOfApp(&f.Apps[0]))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(entry), "external_costs") {
		t.Errorf("Entry carries external_costs: %s", entry)
	}
}

// TestUnknownFieldAsymmetry pins the rule in APP_STORE.md # What the box models:
// an unknown field OUTSIDE the app array is dropped harmlessly, while an unknown
// field INSIDE an app rejects the whole snapshot. The two cases behave in
// opposite ways, and the difference decides whether a publish-side change can
// ship before the fleet updates or must wait for a release.
//
// The reason is that verify() recomputes the digest by re-marshalling what it
// parsed: a per-app key the box does not model is absent from the re-marshal, so
// the digest cannot reproduce. This test exists because the spec used to state
// the harmless half as if it covered both, and a per-app field was added on that
// reading — the box rejected the whole snapshot instead of dropping a field.
func TestUnknownFieldAsymmetry(t *testing.T) {
	// The published bytes, as the control plane would marshal them: one app
	// carrying a per-app key this box does not model.
	appsJSON := []byte(`[{"id":"alpha","name":"Alpha","version":"1.0",` +
		`"footprint":{"image_download_bytes":0,"image_disk_bytes":0},` +
		`"environments":["hosted"],"not_modelled_here":{"any":"shape"},` +
		`"manifest":"id: alpha\n","compose":"services: {}\n"}]`)
	sum := sha256.Sum256(appsJSON)
	stamped := hex.EncodeToString(sum[:])

	perApp := []byte(`{"schema_version":` + strconv.Itoa(wireSchemaVersion) +
		`,"index_sha256":"` + stamped + `","apps":` + string(appsJSON) + `}`)
	if _, err := parseSnapshot(perApp); err == nil {
		t.Error("a per-app field the box does not model was accepted; it must reject the snapshot, " +
			"which is why adding one is a coordinated release and not a publish-side change")
	} else {
		t.Logf("per-app unknown field rejects the snapshot, as documented: %v", err)
	}

	// Same snapshot without the unmodelled per-app key, plus an unmodelled
	// TOP-LEVEL key. That one sits outside the digest, so it is dropped and the
	// snapshot still verifies — the case the publish side may ship ahead of boxes.
	cleanApps := []byte(`[{"id":"alpha","name":"Alpha","version":"1.0",` +
		`"footprint":{"image_download_bytes":0,"image_disk_bytes":0},` +
		`"environments":["hosted"],` +
		`"manifest":"id: alpha\n","compose":"services: {}\n"}]`)
	sum = sha256.Sum256(cleanApps)
	topLevel := []byte(`{"schema_version":` + strconv.Itoa(wireSchemaVersion) +
		`,"index_sha256":"` + hex.EncodeToString(sum[:]) +
		`","not_modelled_here":{"any":"shape"},"apps":` + string(cleanApps) + `}`)
	if _, err := parseSnapshot(topLevel); err != nil {
		t.Errorf("a top-level field the box does not model must be dropped, not rejected: %v", err)
	}
}
