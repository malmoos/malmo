# Store browse filters — search + category pills

- **Status:** done
- **Date:** 2026-06-11
- **Specs touched:** `DASHBOARD.md` (# global navigation — the "Search is deferred" note now carves out the Store's in-page filters); `NEXT.md` (the categories-taxonomy item annotated with what the UI does today). Realizes `APP_STORE.md` # Catalog schema ("Browse UI groups by category") — contract unchanged.

Picks up the "what's next" that [store-app-detail-ui.md](store-app-detail-ui.md) left open — *"Flat grid only. Category grouping / search on the browse grid is deferred — earns its place as the catalog grows."* The catalog has since grown past the whoami sample to ~a dozen apps (calibre-web, docuseal, mealie, open-webui, kan, kimai, memos, …) spanning several categories, so the flat grid no longer scans at a glance. This slice adds in-page browse filtering to the Store, frontend-only.

## What was done

- `views/StoreView.vue` — the small uppercase "Catalog" label becomes a real **"Store"** page heading. Underneath it, two filters that compose and narrow the grid in place:
  - **Search** — a page-wide `<input type="search">` filtering over each app's `name` + `short_description`, case-insensitive, trimmed. Empty query matches everything.
  - **Category pills** — a wrapping row of rounded pills; **"All"** first (the default, no category filter), then the **sorted union of every loaded app's `categories`**. The set is derived from the corpus, not a hardcoded taxonomy (the taxonomy is still open, `NEXT.md`), so a new catalog category surfaces as a pill with no UI change. The pill row hides entirely when no app declares a category. Active pill is accent-filled; the rest are quiet bordered/`muted` hovers.
- Filtering is a single `filtered` computed (`matchesCategory && matchesQuery`). A new empty state — **"No apps match your search."** — distinguishes a zero-result filter from the pre-existing **"No apps in the catalog yet."** (empty corpus).
- The admin-only Door-2 "Install a custom container" footer is untouched.

No brain / OpenAPI / type change: `categories`, `name`, and `short_description` already ride on every `catalog.Entry` (served since [store-app-metadata-brain.md](store-app-metadata-brain.md)).

`make check-web` (vue-tsc typecheck + production build) passes.

## What's next

- **No automated UI tests.** Verified by typecheck + build + manual review; the standing `web-ui/` Vitest follow-up still applies (no harness yet).
- **Single-select categories.** One active pill at a time; multi-select / AND-ing categories isn't built and hasn't been asked for.
- **Category taxonomy still open.** Pills render whatever raw strings the manifests carry (lowercase slugs, capitalized for display). A curated taxonomy — canonical set, ordering, display names, localization — remains the open `NEXT.md` item; this UI doesn't presuppose one.
- **Search is name + short-description only.** It doesn't reach the long description, author, or category names; that's deliberate for the browse grid and can widen if it proves too narrow.
