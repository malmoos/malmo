# Store curated landing page

- **Status:** done
- **Date:** 2026-08-04
- **Specs touched:** `APP_STORE.md`

The box's app store landing was much thinner than the marketing store on the control plane: the store repo's `home.yml` (a spotlight app + ordered category groups) was published on the control-plane snapshot but never carried onto the box, so the box's landing was pills + a flat Featured strip only, with no spotlight banner and no packed category rows.

## What was done

### `internal/catalog` (box)

- `wire.go` — added `wireHomePage` / `wireHomeGroup`, byte-faithful mirrors of cloud's `HomePage` / `HomeGroup` (`../cloud internal/catalog/published.go`), and a `Home wireHomePage` field on `catalogFile`. Confirmed `indexDigest` hashes `f.Apps` only, so this addition does not touch the box's index-digest contract with the cloud.
- `catalog.go` — extended `Home` with `Spotlight *Entry` and `Groups []HomeGroupView` (new type, mirroring cloud's `HomeGroupView`), keeping `Categories`/`Featured` as-is. Added a `home() (*Entry, []HomeGroupView, error)` method to the `source` interface, alongside the existing `featured()`, since neither can be derived from `List()`.
- `remote.go` — `snapshot` now carries the parsed `wireHomePage`; `remoteSource.home()` projects it filtered to the box's environment: an app the block names that isn't advertised on this surface drops the spotlight to nil or is skipped within its group, and a group left with no advertised apps is dropped entirely — mirroring cloud `Service.Home`'s semantics field-for-field.
- `disk.go` — `diskSource.home()` returns `nil, nil, nil` (no curation on a bare manifest tree); unchanged behavior otherwise.
- Tests: `segmented_test.go` gained `TestHomeProjectsSpotlightAndGroups` (spotlight/group projection + per-environment filtering, including a group that empties out entirely) and `TestHomeNoBlockYieldsNoSpotlightOrGroups` (a snapshot with no home block yields neither, with categories/featured unaffected), plus the `homeFixture`/`makeSnapshotHome`/`newFakeCPHome`/`syncedCatalogWithHome` fixtures they build on.
- Regenerated `api/openapi.json` / `api/openapi.yaml` (`make openapi`) so the `Home` schema carries `spotlight`/`groups` and a new `HomeGroupView` schema exists.

### `web-ui` (box dashboard)

- `src/api.ts` — regenerated `src/generated/openapi.ts` (`npm run gen:api`) and added the `HomeGroupView` type alias.
- `src/lib/storeLayout.ts` (new) — `packRows`, a faithful port of the marketing store's row-packing algorithm (`../cloud internal/web/static/store.js` `packRows`): groups walk in authored order, pulling forward the first later group that fits the remaining row width (4 columns), so a group that can't be paired stands alone. Also `groupSpan`/`groupCols`, which map a group's app count to a fixed, written-out set of Tailwind `sm:col-span-N` / `sm:grid-cols-N` classes (not interpolated, since Tailwind's scanner can't see interpolated class names — same reasoning as the marketing store's `groupRow`). No test file: `web-ui` has no test runner configured (`package.json` has no `test` script, no vitest), so this stays plain, readable code rather than adding a first-of-its-kind test harness for one helper.
- `src/components/StoreSpotlight.vue` (new) — the spotlight banner: icon tile, "Spotlight" eyebrow, name, tagline, the whole card a `RouterLink` to `/store/:id`. Styled with the box's semantic tokens (`border-border`, `bg-card`, `text-accent`, `text-muted-foreground`), not cloud's raw `olive-950/…` classes.
- `src/components/StoreAppCard.vue` — added the tagline (`short_description`) as a second, truncated line under the name; widened the icon from `size-1/2` to `size-3/5`. Kept the existing `icon_glyph` fallback and broken-icon guard unchanged.
- `src/views/StoreView.vue` — landing (`mode === "home"`) now renders, in order: the spotlight banner, then the authored category groups packed via `packRows` (each row its own `sm:grid-cols-4` grid, each group a `flex flex-col` sized by `groupSpan`/`groupCols`). Fallback chain preserved: authored home (spotlight/groups) → the existing flat Featured row (now shown on the landing only when nothing is authored) → the existing "Pick a category or search" line. Category and search modes are unchanged except for the grid-width change below. Every store grid (`sm:grid-cols-4 lg:grid-cols-6` → `sm:grid-cols-3 lg:grid-cols-4`) is now wider per-card: the featured row, the category grid, the search grid, and the new group rows all match.

### Docs

- `docs/specs/APP_STORE.md` — new "Landing page" section describing the authored-in-`home.yml`, published-verbatim, filtered-per-environment, rendered-by-both-consumers model, plus a bullet in "Locked decisions" pointing at it.
- `docs/architecture.md` — the `catalog` package row in the per-package table now mentions the landing-page projection.

## Known gaps & deviations

- No unit test for `packRows`/`groupSpan`/`groupCols` in `web-ui` — the repo has no test runner (`vitest`, `jest`, etc.) configured for the frontend at all, appliance- or box-wide; adding one for a single helper felt like scope creep on a UI feature change. `make check-web` (typecheck + production build) is the only frontend gate today.
- The landing's curated-home fallback (spotlight/groups → featured → prompt line) was verified by reading the computed logic and the build/typecheck passing; it was not clicked through in a running `make dev` session against a real seeded catalog with a `home.yml` block (the dev catalog seed path, `make seed-catalog`, builds from a single app package directory, not a `home.yml`).
- `categories.yml` display labels are deliberately not on the wire (out of scope per the issue); the group heading renders the bare category id with `capitalize`, matching the existing category-page heading's own styling rather than adding a label lookup.

## What's next

- Wire a `home.yml` block into the dev catalog seed path (`make seed-catalog` / `dev/mkcatalog`) so the curated landing is clickable in `make dev` without a real control-plane sync.
- If `web-ui` ever gains a test runner, `src/lib/storeLayout.ts`'s `packRows` (2+2, 3+1, 2+1+1, 1+1+1+1, and a >4-app defensive case) is a natural first unit test.
