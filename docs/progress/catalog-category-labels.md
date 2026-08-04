# Render the authored category label instead of inventing one from the id

- **Status:** done
- **Date:** 2026-08-04
- **Specs touched:** `docs/specs/APP_STORE.md` — new # Category labels section, # Landing page corrected, a Locked-decisions bullet added

Closes the gap [store-curated-landing.md](store-curated-landing.md) recorded as a known gap and [catalog-wire-guards.md](catalog-wire-guards.md) left open: the catalog's authored category `label` was not on the wire, so the box invented display text from the category id. The curation source has authored a label per category all along; only ids reached the box.

## The problem

An app's `categories:` holds ids — `developer-tools`, `ai`. Ids are not display text, so something has to turn one into a heading, and every surface did it itself. They disagreed: the same category read as "Developer-tools" in one store and "developer tools" in the other. `store-curated-landing.md` patched that with an explicit stopgap in `web-ui/src/lib/storeLayout.ts` (hyphens to spaces) whose own comment said the real fix was putting the authored label on the wire — it made the two surfaces agree with each other without making either correct. `ai` is the case that proves derivation can't work: no rule over the id produces "AI".

## What was done

**Modelled `categories` on the wire** (`internal/catalog/wire.go`): a `[{id, label}]` vocabulary in authored order, plus `wireCategory` mirroring the control plane's shape. It sits outside the index digest, like `home`.

**Projected it through the facade** (`internal/catalog/catalog.go`). The `source` interface gains `categories()`; the remote source reads it off the snapshot, the disk source returns nil (it has no curation). `Home.Categories` moves from `[]string` to `[]Category`, `HomeGroupView` and the category page each gain `Label`.

The view type `Category` was renamed to **`CategoryPage`**, because `Category` is now the authored vocabulary entry. Both names mirror the control plane's, which had to make the same split.

**Pills follow authored order**, not the sorted union they were before. The vocabulary file is deliberately ordered, so the order is a curation decision — the same reason the landing rows are in authored order.

**The UI renders the label** and the stopgap is deleted. `StoreView.vue` uses `c.label` on pills, `g.label` on row headings, and the category payload's own `label` on the category heading. `AppDetailView.vue`'s info panel used to print raw ids joined with `capitalize` CSS; it now resolves them through the landing payload's vocabulary, on the same query key so it's a cache read rather than a second request. Every `capitalize` class on a category was removed — an authored "Developer tools" must not become "Developer Tools".

**`dev/mkcatalog` gained `-categories`**, and `make seed-catalog` passes the curation source's `categories.yml` automatically when the checkout has one, so a locally seeded store shows real labels rather than the fallback.

**Two fallbacks stay, and neither is the normal path.** A category the vocabulary omits still gets a pill (dropping it would hide a browsable app behind no entry point at all), labelled with a readable form of the id and appended after the authored entries in sorted order so the result is deterministic. A never-synced box, or a disk catalog, has no vocabulary and labels everything that way. The control plane holds the same fallback, so even the degraded modes agree.

## How it maps to the specs

`APP_STORE.md` # Landing page described the stopgap as the design ("it does not look up the catalog's authored category `label:` … that field isn't on the wire"). That paragraph is now wrong and is corrected. A new # Category labels section states the rule (authored once, carried, never derived), the authored-order consequence, the no-text-transform consequence, and both fallbacks. A Locked-decisions bullet records it.

## What was tested

- `make check` and `make check-web` green. `make openapi` regenerated; the TS client regenerated from it.
- New Go tests: the authored label reaches the pills, the landing row headings, and the category page; the pills come back in authored order (the fixture authors `tools` before `media`, which sorts the other way, so a sorted result fails it); a box with no vocabulary falls back to readable ids; a category missing from the vocabulary still gets a pill, after the authored ones.
- The pinned wire fixture was refreshed from the current published snapshot, which is what let `TestNoUnmodeledFields` see the new key at all — an un-refreshed fixture would have left the guard blind and the box silently dropping the field.
- **Verified against a real running brain, not only tests.** Seeded 17 real app packages plus the real curated `home.yml` and `categories.yml`, booted the fake host-agent and a native brain against that cache with an inert catalog URL, bootstrapped an admin, and read the API: `GET /api/v1/catalog/home` returns all seven pills in authored order with authored labels (`ai` → "AI", `developer-tools` → "Developer tools") and all seven landing rows labelled; `GET /api/v1/catalog/category?name=developer-tools` returns label "Developer tools" with its 5 apps, and `?name=ai` returns "AI".

## Known gaps

- **Not clicked through in a browser.** The API is verified end to end against a running brain and the Vue half is typechecked and production-built, but nobody loaded the page and looked at the pills. `make seed-catalog` now sets this up in one command.
- **No frontend unit test** for the label resolution in `AppDetailView`, because `web-ui` still has no test runner configured at all — the same gap `store-curated-landing.md` recorded.
- The curation source also authors a `description` per category. It is not on the wire; nothing renders it today.

## What's next

- The two store surfaces are now consistent on category text, but nothing *keeps* them consistent on presentation generally — no test compares them. A parity checklist and a statement of which layer owns card versus detail presentation are still unwritten.
