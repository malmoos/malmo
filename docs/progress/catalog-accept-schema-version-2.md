# Accept catalog schema version 2

- **Status:** done
- **Date:** 2026-09-08
- **Specs touched:** none — the wire format is unchanged; this is the version stamp the box accepts

## The problem

The store was empty on every box: "No apps in the catalog yet", permanently, with no error a user could see.

`internal/catalog/wire.go` declared `const wireSchemaVersion = 1`, while `GET /catalog?env=<environment>` now serves `"schema_version": 2`. `catalogFile.verify()` refuses any stamp that is not an exact match — deliberately, so a box never half-reads a format it cannot project — so `parseSnapshot` returned an error, `syncOnce` never swapped in a snapshot, and the projection stayed empty forever. Every sync failed the same way, so there was no partial state and no recovery.

The refusal did its job. What was wrong was the number: the served format moved to 2 and the constant was left behind.

## What was done

Two one-line edits, and nothing else.

- **`wireSchemaVersion` is now `2`.** The box already reads schema 2 in full: `wireApp` models `manifest_url` / `compose_url`, `fetchDocument` pulls each app's install documents from its own route, and no code path reads the inline `manifest` / `compose` fields any more. That behaviour shipped in 514710e ("Catalog: fetch browse data and install payloads separately"); only the constant naming the accepted format was missed. So this is not new capability, it is the stamp catching up with code that was already correct.
- **`internal/catalog/testdata/snapshot.json`'s stamp is now `2`.** The fixture is synthetic — three fake apps (`alpha-notes`, `beta-media`, `gamma-demo`) written by hand in the published wire shape, never a copy of a served catalog (CLAUDE.md # Catalog apps). Its body was already schema 2 in every respect; only the top-level stamp said 1, so the stamp alone was edited. Nothing else in the file changed and nothing regenerated it.

## What was tested

- `go test ./internal/catalog/...` green. `TestParseFixtureSnapshot` is the test that was red on the stamp; it parses the pinned payload the way a box parses a served one.
- `make check` green end to end — gofmt, vet, OpenAPI freshness, and the full Go test suite. Nothing else in the tree was affected.
- **Verified against the live served payload, not only the fixture.** Fetched `GET /catalog?env=hosted` (110 KB, `schema_version: 2`) and fed it to this box's own `parseSnapshot` through a throwaway test in `internal/catalog`, then projected it with `newSnapshot`: **41 apps**, 8 categories, 7 authored home groups, spotlight `immich`, and install-document URLs present on every record (`/catalog/apps/<id>/manifest`, `/catalog/apps/<id>/compose`). That is the exact function `remote.go`'s sync path calls, on the exact bytes a box receives, so the empty store is closed against production data rather than against a fixture. The throwaway test was deleted; it is not in the change.

## Known gaps

- **The fixture's schema stamp is maintained by hand.** It was edited here because the served format had already moved, which is the wrong direction of travel: the box's pinned fixture should be refreshed from the published format rather than corrected after a fleet-wide symptom. Keeping the two in step belongs to the publishing side, and a mismatch should surface when the format changes, not when a store goes empty.
- **A refused snapshot is silent to the user.** The box logs a parse error and keeps serving an empty catalog; the store renders its ordinary empty state, which reads as "no apps published" rather than "this box could not read what it was served". A version refusal is a healthy guard, but it is currently indistinguishable from an empty catalog from the outside. Not fixed here — it is a health/notify question, not a wire one.
