# A per-app catalog field is a coordinated release, not a publish-side change

- **Status:** done
- **Date:** 2026-08-10
- **Specs touched:** `docs/specs/APP_STORE.md` — # What the box models, and what it drops split into the two cases and corrected

Follow-up to [catalog-wire-external-costs.md](catalog-wire-external-costs.md). No behaviour changes here: this records a rule the spec had backwards, and pins it with a test.

## The problem

`APP_STORE.md` said the publish side can add a catalog field without waiting for deployed boxes, because `encoding/json` drops unknown keys, and stated plainly that "the integrity digest cannot catch this — `index_sha256` covers the app index (`apps`) only."

That is true for a field **outside** the app array, like `home`. It is exactly backwards for a field **inside** an app.

`verify()` recomputes the digest by re-marshalling the apps it parsed. A box that does not model a per-app field drops the key on parse, so it is missing from the re-marshal, so the digest cannot reproduce and the box rejects **the whole snapshot**. It then serves its last-good cache: not visibly broken, just frozen — no new app, no version bump, no curation change, and one log line that reads like corruption rather than a shape change.

`external_costs` was added on the strength of the sentence above. Measured against a real published snapshot: a box at `e825e9f` returned `catalog index digest mismatch`, while the same snapshot verified on a box that models the field.

## What was done

**The spec section is split into the two cases**, each named for what actually happens: a field outside the app array is *dropped, safely*; a field inside an app is *rejected, and it takes the catalog with it*. The second carries the release ordering — model on the box, cut a release, let the fleet take it, and only then publish data that uses the field. Authoring the data is safe at any time; the danger starts when the sync tool carries it, which happens on the next publish automatically.

**The schema version is the diagnosis, not the fix.** Bumping `wireSchemaVersion` alongside the data turns `catalog index digest mismatch` into `catalog schema version 1, want 2`, which names the real cause. It does not prevent the freeze — an older box refuses either way. It must be bumped *with* the data and never ahead of it: the check is exact equality, so raising it while no app uses the field would reject every snapshot on every deployed box immediately, causing the outage early rather than avoiding it. Nothing is bumped in this change, because nothing publishes a per-app field yet.

**`TestUnknownFieldAsymmetry`** builds two snapshots — one with an unmodelled key inside an app, one with an unmodelled key at top level — and asserts the first is rejected and the second is accepted. The doc claim is now executable, so it cannot quietly rot.

**The PR template asks the question** whenever a per-app field is added, next to the existing catalog-wire checkbox.

## Why not make the box tolerant instead

The digest could be computed over the raw `apps` bytes (`json.RawMessage`) rather than a re-marshal, which would make unknown per-app fields as harmless as top-level ones and retire this whole class. It is the better long-term shape and it is deliberately **not** in this change: it alters the box↔cloud integrity contract, so it needs its own coordinated change and an owner's call. It would also not help any box already deployed, which is the fleet this rule exists to protect.

## What was tested

`go build`, `go vet`, `gofmt`, `make openapi-check`, and `go test ./internal/catalog/` green. The new test was checked against the real failure it describes, not only in the abstract: the per-app case reproduces the same `digest mismatch` a real box produced against a real published snapshot.

## Known gaps

- **The rule is a doc plus a unit test, not a gate.** Nothing mechanically stops someone publishing a per-app field before the fleet updates, because neither repo knows the fleet's version spread. The raw-bytes digest above is the only fix that removes the possibility rather than documenting it.
- **The same asymmetry applies to any other consumer** of the published shape that verifies the digest.
