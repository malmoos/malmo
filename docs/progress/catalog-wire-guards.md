# Catalog wire guards: close the blind spots that let `home` reach the box silently

- **Status:** done
- **Date:** 2026-08-04
- **Specs touched:** none (guards and de-duplication only; no spec behavior changed)

Follow-up to [`store-curated-landing.md`](store-curated-landing.md), which added the `home` block to the box's catalog wire shape (`internal/catalog/wire.go`). That change shipped fine, but it also showed that the box had **no test that would have caught it going the other way**: a new top-level field on the published catalog snapshot could be added upstream and silently dropped by the box, with `TestVerifyRealSnapshot` staying green throughout, because the integrity digest it checks covers only the `apps` array. On top of that, the pinned fixture (`internal/catalog/testdata/snapshot.json`) was already stale by the time this slice started — it predated both `home` and `os_capabilities_version`. This slice closes both gaps and removes a third, unrelated copy of the same wire shape that had already drifted once.

## What was done

### An unknown-key guard (`internal/catalog/wire_test.go`)

`TestNoUnmodeledFields` parses the pinned fixture into a generic `map[string]any` and fails on any top-level key, or per-app key (including the nested `footprint`, `author`, `links`, `images` shapes), that the box's own wire types don't declare a `json` tag for. The known-key set is derived by reflection over `catalogFile`, `wireApp`, and the reused `manifest.Footprint` / `manifest.Author` / `manifest.Links` / `manifest.ImageRef` types (a `jsonKeys(reflect.Type)` helper reading each field's `json:"..."` tag) — not a hand-listed set, which is exactly the kind of thing that would have drifted the same way the digest-only check did.

One field is deliberately not modeled: `os_capabilities_version`, recorded in `ignoredTopLevelKeys` with a comment — it's a publish-side provenance stamp (which capability set the control plane admitted apps against when it built the snapshot) with no box-side meaning, since the box enforces admission against its own `manifest.Version` per-parse, not against a catalog-wide stamp. Every other field in the refreshed fixture is already modeled: the app-key union across all 33 apps in the fixture is a strict subset of `wireApp`'s fields, and the nested `footprint`/`author`/`links`/`images.*` key sets line up exactly with `manifest.Footprint`/`Author`/`Links`/`ImageRef`. `home` itself needed no ignore-list entry — it was modeled in the prior slice, and now has fixture coverage that actually exercises it.

The fixture (`internal/catalog/testdata/snapshot.json`) was refreshed in the same change from the current published snapshot — a pinned copy, same practice as before — so the guard isn't vacuous. `TestVerifyRealSnapshot`'s digest-reproduction check still passes against the refreshed fixture (the `Apps`-only digest is unaffected by `home`/`os_capabilities_version`, both outside it).

### Deleted the third copy of the wire shape (`dev/mkcatalog`)

`dev/mkcatalog/main.go` used to re-declare `catalogFile` / `app` / `wireHomePage` / `wireHomeGroup` byte-for-byte, with a header comment asking humans to keep it in sync with `wire.go` by hand — and it had already drifted once (left behind the `home` field, caught only by review, not by any test). `internal/catalog` now exports a small seam for exactly this:

- `catalog.SnapshotApp`, `catalog.SnapshotHome`, `catalog.SnapshotHomeGroup` — type aliases (`type SnapshotApp = wireApp`, etc.), not copies, so a snapshot-building caller and the box's own parser are structurally the same type under two names. There is no field list to keep in sync because there's only one field list.
- `catalog.BuildSnapshot(apps []SnapshotApp, home SnapshotHome, storeRef string) ([]byte, error)` — stamps `SchemaVersion`/`GeneratedAt`, computes `IndexSHA256` over `apps` (the same `indexDigest` the box's own `verify()` recomputes), and marshals. This is the one function a builder needs; it replaced `mkcatalog`'s own `catalogFile` literal + `json.Marshal` + a second copy of `indexDigest`.

`dev/mkcatalog/main.go` now imports `internal/catalog` and builds `[]catalog.SnapshotApp` from each package's manifest/compose, same as before, just against the exported alias instead of a locally-declared `app` type. The CLI surface (`-pkg` repeatable, `-environments`, `-out`, `-home`) is unchanged.

## How it maps to the specs

No spec changed — this is guards and de-duplication on the existing box↔control-plane catalog contract (`docs/specs/APP_STORE.md` # Catalog schema, established by prior slices). `wire.go`'s own header comment already documents that contract; this slice doesn't touch it beyond adding the exported alias/`BuildSnapshot` seam.

## Known gaps & deviations

- The unknown-key guard walks one level of nesting (`footprint`, `author`, `links`, `images.*`) plus the top level and per-app keys. It does not recurse arbitrarily deep — there's no third level of nesting in the current shape, so this wasn't tested, but a future nested-within-nested field would not be caught unless the guard is extended.
- Per the task's out-of-scope note, no CI job or script fetches the fixture from the control plane. Keeping `testdata/snapshot.json` current is a push from the control-plane side (the publish process is expected to flag or update it when the wire shape changes), not something this repo reaches out for.

## What's next

- If the control plane's wire shape gains a new top-level or per-app field, `TestNoUnmodeledFields` will fail with the offending key named; the fix is either to model the field in `wire.go` (and `dev/mkcatalog` if it should emit it too) or add it to `ignoredTopLevelKeys` with a reason.
- No other duplicate copies of the wire shape are known to remain in this repo.
