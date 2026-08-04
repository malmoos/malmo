package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/malmoos/malmo/internal/manifest"
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

// TestNoUnmodeledFields is the guard for the exact failure mode that let a new
// top-level field (the curated landing page, "home") reach the box silently:
// verify() only checks the schema version and the apps-array digest, so a
// wire struct that has fallen behind the real published shape stays green
// forever unless something asserts the two key sets match. This test parses
// the pinned fixture into a generic map[string]any and fails on any top-level
// or per-app key that the box's own wire types (catalogFile / wireApp, plus
// the nested footprint/author/links/images shapes) do not declare a json tag
// for.
//
// A field showing up here is not automatically a bug — it means the
// published shape moved and this box needs to make a decision: model the
// field (in wire.go, and in dev/mkcatalog if mkcatalog should emit it too),
// or add it to ignoredTopLevelKeys with a comment saying why the box doesn't
// need it. Don't delete or weaken this test to make it pass.
func TestNoUnmodeledFields(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "snapshot.json"))
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
		t.Errorf("testdata/snapshot.json has top-level key %q that catalogFile (internal/catalog/wire.go) does not model: "+
			"either add a field for it there (and in dev/mkcatalog if mkcatalog should emit it too), or add it to "+
			"ignoredTopLevelKeys with a comment explaining why the box doesn't need it", key)
	}

	appsRaw, ok := raw["apps"].([]any)
	if !ok {
		t.Fatal("testdata/snapshot.json: \"apps\" is missing or not an array")
	}
	if len(appsRaw) == 0 {
		t.Fatal("testdata/snapshot.json: \"apps\" is empty, guard has nothing to check")
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
			t.Fatal("testdata/snapshot.json: an \"apps\" entry is not an object")
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
