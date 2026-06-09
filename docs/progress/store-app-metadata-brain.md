# Store app metadata + detail endpoint (brain)

- **Status:** done
- **Date:** 2026-06-08
- **Specs touched:** `BRAIN_UI_PROTOCOL.md` (`APP_STORE.md` # Catalog schema, `APP_MANIFEST.md` # A — realized, not changed)

The backend half of giving the store a card grid + Umbrel-style app detail page. The store UI today renders a thin list because the brain only exposes `id/name/version/footprint` per catalog entry — even though the sample manifests already carry the full identity block (`description.long`, `icon`, `screenshots`, `categories`, `author`, `license`, `links`, `changelog_url`). This change parses that metadata and serves it; the card grid and detail view are the follow-up UI slice.

## What was done

### `internal/manifest`

Added the identity/metadata fields the YAML already contained but the parser dropped: `Icon`, `Screenshots`, `Categories`, `Author{Name,URL}`, `License`, `Links{Homepage,Source,Support}`, `ChangelogURL`. All optional, parse-only — the brain acts on none of them (a ~7-line manifest stays valid). No validation tightening.

### `internal/catalog`

- `Entry` (the browse-grid shape) gains `ShortDescription`, `Categories`, and `IconURL`. `IconURL` is set only when the manifest declares an icon, so the store renders a glyph fallback without ever requesting a 404.
- New `Detail` type embeds `Entry` and adds `LongDescription`, `ScreenshotURLs` (manifest order), `Author`, `License`, `Links`, `ChangelogURL`. `(*Catalog).Detail(id)` builds it; `ErrNotFound` → 404, other load errors → 500 (same integrity posture as the rest of the catalog).
- `IconPath` / `ScreenshotPath` resolve an app's asset to an absolute path inside its catalog directory for serving. `assetPath` roots the manifest-relative path at the app dir (`Clean("/"+rel)`) and prefix-checks the result, so a traversal-y `icon: ../secret` can't escape; a missing file / out-of-range index → `ErrNotFound`.

### `internal/api`

- `GET /api/v1/catalog/{id}` → `Detail` DTO (huma, in the OpenAPI surface).
- `GET /api/v1/catalog/{id}/icon` and `/screenshots/{n}` → raw image bytes via `http.ServeFile`, registered on the bare mux (like SSE / app-log) so they stay out of OpenAPI — the store loads them directly in `<img>` tags. A non-numeric `{n}` is rejected before any catalog lookup; lookup errors map through `serveAsset` (ErrNotFound → 404, else 500).
- Regenerated `api/openapi.{json,yaml}` and `web-ui/src/generated/openapi.ts`; added `CatalogDetail` alias in `web-ui/src/api.ts`.

Tests: `internal/catalog` covers Entry metadata, icon-URL-omitted-when-absent, Detail shape, the asset path resolvers, and traversal/missing/out-of-range/unknown-app errors. `internal/api` covers the detail endpoint (200/404/401), the icon + screenshot routes serving real bytes, and the 404 matrix.

## What's next

- **Store card grid** — replace `StoreView.vue`'s list with a flat responsive grid of `StoreAppCard` (logo + name, whole card is a `RouterLink` to `/store/:id`). Size moves off the card (detail page + install dialog only, per the product call this slice was scoped against).
- **App detail page** — new `/store/:id` → `AppDetailView.vue` mirroring the Umbrel app-page structure (header with icon/name/tagline/developer/Install, screenshot gallery, markdown long description via `marked` + `DOMPurify`, info sidebar with version/category/links/size). The install flow (install-plan fetch, `InstallDialog`, 409-duplicate handling, household/personal split-button) moves out of `StoreView` into the detail page — likely via a `useInstall(catalogId)` composable.
- `APP_STORE.md` # Catalog schema describes the future *remote signed* catalog file (manifest_url/hashes); the v1 local catalog directory + brain-served assets are documented in `BRAIN_UI_PROTOCOL.md` # Catalog browse, detail, and assets. When the remote catalog lands, reconcile how icon/screenshot bytes are served (brain proxy vs. store CDN).
