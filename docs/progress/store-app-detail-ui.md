# Store card grid + app detail page (UI)

- **Status:** done
- **Date:** 2026-06-08
- **Specs touched:** none (realizes `DASHBOARD.md` # global navigation, `APP_STORE.md` # Catalog schema — UI shape, not contract)

The UI half of the store redesign, on top of [store-app-metadata-brain.md](store-app-metadata-brain.md) (which made the brain serve the metadata + a detail endpoint + icon/screenshot assets). Turns the Store from a flat list of rows into an app-store-style **browse grid → detail page**: every app is a card (logo + name) that opens its own page, structured like a typical app-store product page (header → screenshots → description → info), with Install living on the detail page rather than the row.

## What was done

### Browse grid

- `components/StoreAppCard.vue` — a sibling of the dashboard's `AppTile` (square, bordered, rounded) but a **navigation target, not a launcher**: the whole card is a `RouterLink` to `/store/:id`. Renders the catalog icon (`icon_url`) with a glyph fallback (the brain omits `icon_url` when the manifest declares none, and an `@error` guard covers a failed load).
- `views/StoreView.vue` — rewritten: the catalog section is now a flat responsive grid (`grid-cols-2 sm:grid-cols-4 lg:grid-cols-6`, the same rhythm as `HomeView`) of `StoreAppCard`s. All the install logic moved out (see below). The admin-only Door-2 "Install a custom container" footer is unchanged.

### Detail page

- `views/AppDetailView.vue` at `/store/:id` — header (large icon, name, short-description tagline, author/developer link, Install/Open affordance), a horizontally scrolling screenshot gallery, the long description, and an info sidebar (version, category, size, license, website/source/support/changelog links). Each block is hidden when its data is absent.
- Long description is author markdown rendered with **`marked`** and sanitized with **`DOMPurify`** before it touches the DOM (catalog text is author-controlled, but sanitize anyway). Scoped `:deep()` styles give the `v-html` body basic typography since the project has no `@tailwindcss/typography`.
- The coarse catalog footprint (`image_disk_bytes`) is shown only here (in the info sidebar) and in the install dialog — not on the cards — per the product call for this slice.

### Install flow extraction

- `useInstall.ts` — the catalog-app install flow lifted out of the old `StoreView` so the detail page owns it without duplicating ~150 lines: the advisory install-plan fetch (enabled only while the consent dialog is open), the install mutation with its 409-duplicate / 422-election / mid-job-failure branches, and the per-app button state (household vs. own-personal instance, admin "install for the whole household" split-button). Reads/writes the shared `["apps"]` query cache so installs reflect across views. `AppDetailView` consumes it and renders `InstallDialog` from the returned `activePlan`.

### Routing / deps / docs

- `/store/:id` route added (after `/store/custom`; Vue Router also ranks the static segment higher, so `custom` never matches the param).
- Added `marked` + `dompurify` to `web-ui` deps.
- `docs/dev/web-ui.md` — folder map, the browse→detail routing note, and an "Install flow" section for `useInstall`.

`make check-web` (vue-tsc typecheck + production build) passes.

## What's next

- **No automated UI tests.** The web-ui has no component-test harness yet; this slice was verified by typecheck + build + manual review. A Vitest/Testing-Library setup is the standing follow-up for the whole `web-ui/` (not specific to this slice).
- **Flat grid only.** Category grouping / search on the browse grid is deferred (`DASHBOARD.md` # Search — earns its place as the catalog grows).
- **Screenshots are eager.** All screenshots load when the detail page mounts; lazy-loading / a lightbox is a polish follow-up.
- Depends on the brain endpoints from [store-app-metadata-brain.md](store-app-metadata-brain.md) — this branch is stacked on that one and should merge after it.
