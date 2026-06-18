# App tile quick menu + icon-only open

- **Status:** done
- **Date:** 2026-06-17
- **Specs touched:** `DASHBOARD.md`

The home launcher's tile was one big click target: clicking anywhere on the tile (icon or name) opened a running app or started a stopped/failed one. This slice narrows that affordance to the **logo square only, and only to open a running app** — a stopped/failed tile is inert. Starting, stopping, and retrying all move into a new **quick menu**: a lightweight per-app control surface reachable without leaving the launcher, opened by a menu button beside the name.

## What was done

Frontend-only, four files in `web-ui/`:

- **`components/AppTile.vue`** — restructured so the clickable `<component>` (a link when running, otherwise an inert `div`) is the **logo square alone**, not the wrapper around logo + name. The logo opens a running app and does nothing on a stopped/failed tile — the tile-level click-to-start/retry (`onClick`, the `start` emit, the `starting`/`retrying` captions, `canAct`/`canStart`/`canRetry`) was removed. Below the name sits a **menu button** (`EllipsisVertical`), pinned to the right edge (absolutely positioned so the name stays centered) and shown only to a viewer who may control the app (`canControl`: admin for any app, owner for a personal one). Hover captions simplified to bare status — **"Service stopped"** / **"Failed"** (no "click to…" hint, since the tile no longer acts); the failed-state "View details" link stays.
- **`views/HomeView.vue`** — dropped the now-unused start wiring (`startApp`, `startingIds`, the `@start`/`:starting` props, and the `waitForJob`/`Job`/`pushErrorToast`/`useQueryClient` imports). The quick menu owns its own mutations, so the tile no longer reports in-flight starts up to Home.
- **`components/AppMenuDialog.vue`** (new) — the popup. A centered modal (matching `ElevateDialog`'s overlay pattern, teleported to `body`, dismissed by backdrop click / X button / Escape) showing the app's **logo, name, and short description**, then two full-width actions: a **service control** that adapts to state (Stop running / Start stopped / Retry failed — the same `POST /apps/{id}/stop|start` jobs the detail page runs, with the same `awaitJob` poll-to-terminal + query invalidation) and an **App settings** link to `/settings/apps/<id>`. The popup stays open across a Stop/Start so the user sees the control flip as the instance prop updates from the invalidated `["apps"]` query. `short_description` isn't on the instance DTO — it's fetched lazily from `GET /catalog/{manifest_id}` (best-effort, no retry; a Door-2 custom app with no catalog entry simply shows logo + name).
- **`docs/specs/DASHBOARD.md`** # Tile / # Quick menu / # Open-app interaction — narrowed the logo to open-running-only, moved start/stop/retry to the quick menu, simplified the hover captions, and noted the controller-only gating.

Authorization mirrors the existing model end-to-end: the menu button is gated to controllers in the UI, and the brain re-checks `authorizeAppMutation` on every Stop/Start regardless. No brain, OpenAPI, or generated-type change. `make check-web` (typecheck + production build) green.

## What's next

- The quick menu is a thin slice of the Settings → Installed apps detail page (logs, uninstall, outgoing-email, setup secrets stay there). If the menu grows, watch for divergence between the two control surfaces — they run the identical Stop/Start mutations today but duplicate the wiring.
- Not verified against a running brain in this change (frontend-only, behind the existing mutations); a manual pass on the dev stack (`make dev`) would confirm the popup's live Stop ↔ Start flip and the catalog fetch fallback for a Door-2 app.
