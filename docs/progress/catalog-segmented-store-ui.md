# Segmented store UX in the box Vue UI (cloud #63)

- **Status:** done (box code + tests; live boot-acceptance rides the next image rollout)
- **Date:** 2026-07-02
- **Specs touched:** none rewritten. The serving model this builds on is already documented — cloud `specs/CATALOG.md` # Serve (the segmented endpoints) and `docs/progress/catalog-remote-thin-client.md` (the box's synced snapshot + last-good cache). `docs/specs/APP_STORE.md`'s field semantics (`categories`, `icon_glyph`, `footprint`) carry over unchanged; the "browse UI groups by category" note it already carries is now realized server-side rather than client-side.

Catalog "step 3b" (cloud issue `malmoos/cloud#63`), staged after step 3 (the thin-client swap, `catalog-remote-thin-client.md`). Move the box's Vue store UI off "load the whole catalog, then filter client-side" onto the control plane's **segmented** browse model, so the browser never pulls the entire catalog up front.

Everything is the box side; there is no cloud change. The segmented endpoints already exist on the control plane's public API; the box now mirrors their shapes on the brain and serves them **from its own synced snapshot** — not by re-proxying each hit upstream, which would have broken step 3's offline-browse guarantee.

## What was done

### Brain — segmented catalog methods (`internal/catalog`)

The box already holds the whole snapshot locally (step 3), so the three views are projections of it, computed on the `*Catalog` facade:

- **`catalog.go`** — new `Home{Categories, Featured}` and `Category{Category, Apps, Featured}` result types (byte-mirrors of cloud `catalog.Home`/`Category`), plus `Catalog.Home()` / `Category(cat)` / `Search(q)`. `Home` is the sorted category union + the featured row; `Category` filters `List` by a case-insensitive category (`containsFold`) and is `ErrNotFound` when nothing matches, so an unknown/emptied category is a clean 404; `Search` matches name + tagline + categories (case-insensitive substring) and returns nothing for a blank query (search narrows; browse is for everything).
- **The one input the facade can't derive from `List` is featured**, so the private `source` interface gained a single `featured() ([]Entry, error)`. `remoteSource.featured()` (`remote.go`) reads `Featured`/`Rank` straight off the snapshot — the fields the box already carries for wire fidelity but keeps off `Entry`/`Detail` — env-filtered and ascending by rank (`rankOf` sinks an unranked app). `diskSource.featured()` (`disk.go`, test-only backing) returns nil: a disk manifest tree has no store curation.
- Everything stays behind the facade, so `internal/api` and `internal/lifecycle` hold a `*catalog.Catalog` unchanged; the flat `List`/`Entry`/`Detail`/`Load` surface is untouched and still backs the detail page, installed-instance enrichment, and install.

### Brain — segmented HTTP endpoints (`internal/api/api.go`)

Three huma-registered routes under the existing box API, served from the local snapshot (so browse works offline):

- `GET /api/v1/catalog/home` → `Home`
- `GET /api/v1/catalog/category?name=…` → `Category` (404 on unknown)
- `GET /api/v1/catalog/search?q=…` → `{ apps: [] }`

`home` and `search` are literal path segments (strictly more specific than `/catalog/{id}`, so they win and reserve those two ids). **Category takes `?name=` rather than a `/category/{cat}` path deliberately:** a `/catalog/category/{cat}` route conflicts with the existing `/catalog/{id}/install-plan` under `net/http`'s mux precedence (both match e.g. `/catalog/category/install-plan`, neither is more specific → a registration-time panic; confirmed empirically). The cloud dodged the same collision by collapsing its app subresources into one `/{id}/{sub}` handler; the brain keeps its typed `install-plan` route, so category moves to a query param. This is a brain↔box-UI path (not the box↔cloud contract), so shaping it this way costs nothing.

OpenAPI regenerated (`make openapi`); the TS client regenerated from it (`npm run gen:api`) picks up the `Home` / `Category` / `Catalog-searchResponse` schemas.

### Box UI — `web-ui/src/views/StoreView.vue`

Rewritten from load-all-then-filter to three mutually exclusive entry points, each its own `useQuery`:

- **Landing** (`/catalog/home`): the category pills + a curated **Featured** row. No full grid — the landing never pulls every app.
- **Category** (`/catalog/category?name=`): that category's apps under a heading, with the featured row riding along (the payload carries it, matching the landing).
- **Search** (`/catalog/search?q=`, 200ms debounced): results only, no featured row.

Search wins over a selected category wins over the landing; selecting a pill clears the search and vice versa, so the grid always reflects exactly one view. The pill row is driven by `/catalog/home`'s category list (still the corpus-derived taxonomy, now computed by the brain), so a new catalog category still appears with no UI change. Empty-catalog, no-category-match, and no-search-match keep their empty-state blocks; a failed landing fetch takes down the whole store, a failed category/search fetch only the browse region. `api.ts` gained `CatalogHome` / `CatalogCategory` aliases.

## Tests

- **`internal/catalog/segmented_test.go`** — `Home`/`Category`/`Search` over a synced remote snapshot (facade wrapping a real `remoteSource`): category union sorted, featured ordered by rank and env-filtered (a hosted-only featured app never leaks onto the appliance landing), category case-folding + `ErrNotFound`, search matching name/category and honoring the env filter (a hosted-only app never surfaces in an appliance search), blank search empty, and the disk-backed empty-store path (Home empty-not-error, Category `ErrNotFound`, Search nil).
- **`internal/api/catalog_segmented_test.go`** — the three endpoints end-to-end through the auth'd harness: home category union, category filter + 404, search match + blank-empty, and that all three sit behind the same auth middleware (401 unauthenticated).
- `go test ./internal/catalog/ ./internal/api/` green; `gofmt`/`go vet` clean on the touched files; `web-ui` `vue-tsc --noEmit` + `vite build` green.

## Known gaps / not verified here

- **Live boot-acceptance is deferred**, as with the step-3 entry — a real box browsing the segmented store against the live control plane is validated on the next `make deploy-image` + redeploy, not in this loop. The behavior the box relies on (serving the segmented views from the local snapshot, degrading to an empty store when never synced) is covered by the unit tests.
- **No new offline path to prove:** the segmented views read the same in-memory snapshot the flat `List` already served offline, so step 3's last-good-cache browse guarantee extends to them for free.
