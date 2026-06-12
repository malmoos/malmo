# Store icon glyph fallback + card icon sizing

- **Status:** done
- **Date:** 2026-06-11
- **Specs touched:** `APP_MANIFEST.md` (# A — new optional `icon_glyph` field), `APP_STORE.md` (# Catalog schema — `icon_glyph` on `Entry`), `BRAIN_UI_PROTOCOL.md` (# Catalog browse — grid field list), `docs/dev/web-ui.md` (`AppGlyph` component + lazy-chunk note).

Sibling of [store-browse-filters.md](store-browse-filters.md) on the same store-UX branch. Replaces the store's one-size-fits-all fallback — a single generic `AppWindow` glyph for *every* app with no bundled icon — with an author-chosen **Lucide glyph named in the manifest**, plus a small card-icon sizing fix.

## What was done

### Manifest + brain (the contract)

- `internal/manifest`: new optional `icon_glyph` field (Lucide icon name, kebab-case, e.g. `notebook-pen`). Shape-validated with the existing `kebabSlug` regex — the brain **can't** confirm the name exists (the 1700+ icon set lives in the UI, not Go), so a well-formed-but-unknown name passes validation and degrades to the generic glyph client-side. A non-kebab name is rejected at parse time (and by `malmo manifest check`).
- `internal/catalog`: `Entry` gains `icon_glyph` (omitempty), populated verbatim from the manifest. Flows into OpenAPI + the generated TS types automatically (the `/catalog` + `/catalog/:id` handlers serialize `catalog.Entry`/`Detail` directly).
- Curated `icon_glyph` onto the four currently icon-less catalog apps: `calibre-web` → `book-open`, `hermes-agent` → `bot`, `files-demo` → `folder`, `whoami` → `flask-conical`.

### UI

- New shared `components/AppGlyph.vue` — given a kebab-case `name`, lazily resolves the Lucide component (kebab → PascalCase export) and renders it, falling back to `AppWindow` when the name is absent, malformed, or unresolved. Consumed by both `StoreAppCard` (grid) and `AppDetailView` (detail header), replacing the inline `AppWindow` in each.
- `StoreAppCard`: the raster icon now renders at **50% of the card, centered** (`size-1/2 object-contain`) instead of filling the tile edge-to-edge (`size-full object-cover`) — app logos read as logos, not cropped fills. The glyph fallback matches at `size-1/2`.
- Category pills got `cursor-pointer` (native `<button>` doesn't show the hand by default under the reset).

## How it maps to the specs

Realizes the "browse view is rendered from the catalog `Entry` alone" contract (`APP_STORE.md` # Catalog schema) by adding one more display-only `Entry` field; the brain still acts on none of the identity metadata. Icon choice is a UI concern, the same way "Browse UI groups by category" is.

## Known gaps & deviations

- **Whole-set lazy chunk.** `AppGlyph` does `import("lucide-vue-next")`, so the entire ~900 KB (≈155 KB gzip) icon library is one lazy chunk that loads the first time *any* glyph fallback renders — not per-icon. Acceptable because in a curated catalog real apps ship raster logos, so the fallback (and its chunk) is the exception; documented in `docs/dev/web-ui.md` with per-icon dynamic imports as the escape hatch if glyph usage grows. The dev catalog currently leans on it more than prod will (the four apps above are utility/demo entries).
- **No name-existence validation server-side.** A typo'd-but-kebab name (`note-book-pen`) passes the brain and silently falls back to the generic glyph; only `lucide.dev/icons` tells you the canonical name. A build-time check against a bundled name list was judged not worth the maintenance.
- **No automated UI tests** (standing `web-ui/` gap; verified by typecheck + build + manual review).

## What's next

- Per-icon lazy chunking for `AppGlyph` if/when glyph fallback becomes common in the catalog.
- A future curated category/icon **taxonomy** (`NEXT.md`) could also suggest default glyphs per category, reducing the need for authors to hand-pick.
