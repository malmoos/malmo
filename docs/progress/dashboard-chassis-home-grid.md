# 0026 — Dashboard frontend: stack chassis + home grid

- **Status:** done
- **Date:** 2026-05-29
- **Specs touched:** none changed (realizes `WEB_UI.md` and `DASHBOARD.md`)

Queue item #1. Brings `web-ui` up to the locked `WEB_UI.md` stack and replaces the single plain-CSS dev screen with the real logged-in shell from `DASHBOARD.md`: the grouped **Household / Yours** app grid plus the four-item dock, rendered against the now owner-scoped `GET /apps` (see [owner-scoped-instances.md](owner-scoped-instances.md)).

## What was done

### Stack chassis (`WEB_UI.md` # Locked: stack)

- Added the locked dependencies: **Vue Router 4**, **Pinia**, **Tailwind CSS 4** (`@tailwindcss/vite`, CSS-config via `@theme` — no `tailwind.config.js`), **reka-ui**, **lucide-vue-next**, and the shadcn-vue helpers (`class-variance-authority`, `clsx`, `tailwind-merge`). Package manager stays **npm** for now (the Makefile already notes pnpm is deferred until it's set up on the box).
- `@` → `src/` alias in `vite.config.ts` + `tsconfig.json`, matching the shadcn-vue convention so the CLI can add components later without path rewrites. `src/lib/utils.ts` carries the `cn()` class-merge helper for the same reason.
- `src/style.css` is the Tailwind 4 entry with an `@theme` token block (calm near-white canvas, one quiet accent) following the shadcn variable names. Light theme only; dark-mode trigger stays a downstream `WEB_UI.md` decision.
- `main.ts` registers Pinia + Router + VueQuery. `router.ts` defines history-mode routes for the four dock destinations (`/`, `/files`, `/store`, `/settings`) with an unknown-path → Home fallback; views are lazy-loaded (code-split per the build output).

### Logged-in shell (`DASHBOARD.md` # global navigation / # the top bar)

- `App.vue` keeps the auth gate (bootstrapping → Setup → signed-in shell → dev "session unavailable" notice) and now renders `AppShell` when signed in.
- `AppShell.vue` owns the chrome: `TopBar` + routed `<RouterView>` + the floating `Dock`. The single SSE subscription (`useEvents()`) moved here so cache invalidation works on every view, not just one screen.
- `TopBar.vue` — the three quiet top-bar elements: a storage pill (routes to Settings; static `— / — TB` until a capacity endpoint exists), a notification bell (no unread dot until `NOTIFICATIONS.md` is wired — queue item #2), and an account menu (username + role; **no sign-out** in the single-user dev phase).
- `Dock.vue` — the four-item floating dock (Home / Files / Store / Settings) with lucide icons and router-active highlighting. Activity and Users are deliberately **not** here; they belong under Settings as role-gated routes.

### Home grid (`DASHBOARD.md` # the home screen is the app launcher)

- `HomeView.vue` fetches the scoped `GET /apps` and groups it into **Household** (`scope === "household"`) and **Yours** (personal instances owned by the current user). It filters Yours to `owner_user_id === me` so an admin never sees other members' personal instances on Home, even though the brain returns them in the admin-scoped list.
- `AppTile.vue` — icon + name + a quiet label; calm by default. A down/stopped instance grays out and gets a corner alert mark; a running tile is an `<a target="_blank">` to its own subdomain (`DASHBOARD.md` # Open-app interaction — launcher, not a frame).
- Empty state points at the Store rather than a wall of suggestions (`DASHBOARD.md` # First arrival).

### Views + cleanup

- `StoreView.vue` — the former dev Dashboard's catalog + custom-install surface, restyled and moved to its own route.
- `FilesView.vue` / `SettingsView.vue` — stubs. Files awaits `FILES.md` (`NEXT.md` Tier-1). Settings carries the account card plus a **temporary** "Installed apps" management list with uninstall (see gap below).
- `src/Dashboard.vue` deleted (replaced by the views).
- `api.ts`: `Instance` gains `owner_user_id` / `owner_username` / `scope`; the error parser now reads huma's default model (`detail` / `errors[].message` / `title`) in addition to `{code,message}`, so brain errors (e.g. the duplicate-install summary) surface a real message instead of the bare status text.

## How it maps to the specs

- Realizes `WEB_UI.md` # Locked: stack (Router/Pinia/Tailwind 4/reka-ui/lucide all now present) and its architectural rules (server state in Query, one SSE subscription at mount, `<script setup>` only, `@`-alias shadcn-readiness).
- Realizes `DASHBOARD.md` # the home screen is the app launcher (grouped grid, per-user scoping, tile down-state, open-in-new-tab, empty state) and # global navigation (the four-item dock) and # the top bar (storage pill / bell / account menu, quiet by default).
- Consumes the owner-scoped DTO and read-scoping pinned by [owner-scoped-instances.md](owner-scoped-instances.md) / `BRAIN_UI_PROTOCOL.md` # Pattern B.

## Known gaps & deviations

- **shadcn-vue components are not adopted yet.** The chassis (Tailwind tokens, `cn()`, `@` alias, reka-ui dependency) is scaffolded so the shadcn-vue CLI can add components, but the views are hand-rolled with Tailwind. Pull in CLI components (Button, DropdownMenu, etc.) as views demand them. The account menu is a hand-rolled toggle, not a reka-ui `DropdownMenu`.
- **Uninstall lives in Settings as a stopgap.** Home tiles only *open* apps and there's no per-app detail page yet, so the only place to uninstall is a temporary "Installed apps" list under Settings. When an app detail page lands, per-instance management moves there and that section goes away.
- **No scope picker / duplicate-confirm flow in Store.** Install uses the brain's default scope (admin → household, member → personal) and hides the catalog button once a caller-visible instance exists, sidestepping the warn-don't-block 409. The admin Household / "Just for me" picker and the duplicate-confirm dialog (`DASHBOARD.md` # Warn, don't block; wire shape pinned in [owner-scoped-instances.md](owner-scoped-instances.md)) are a follow-up.
- **Per-app icons are generic.** The manifest/DTO carries no icon yet, so every tile uses one glyph. Real icons follow when the catalog carries them.
- **Pinia is registered but has no store yet** — first store lands with the first genuine client-side state (the `useHealth()` store in the notifications/degraded-mode slice). No premature store added.
- **Health / degraded-mode surfacing is not built** (`WEB_UI.md` # Health & degraded mode): no banner, `useHealth()`, or `<HealthGated>`. Ties into queue item #2 (notifications) and a later degraded-mode slice.
- **Sign-in/out stays disabled** (single-user dev phase, unchanged from before). The headless render check confirmed the app mounts/renders/styles and the auth gate fires, but the signed-in shell is best seen via `make dev` + an authenticated session.

## Verification

- `npm run build` (vue-tsc `--noEmit` + vite build, i.e. `make check-web`) passes; views code-split into per-route chunks.
- Ran the brain + fake host-agent + Vite dev server natively: `/auth/state`, `/setup`, `/me`, and `/apps` return the exact shapes the UI consumes; a real `whoami` install produced an `/apps` row with `scope: "household"`, `owner_username`, `state: "running"`, and `url` — the precise shape `HomeView` groups and `AppTile` opens. Vite serves the SPA and proxies `/api` to the brain. Headless Chrome confirmed Vue mounts and Tailwind styles render (it hit the no-session dev branch, as expected without a cookie). Smoke processes, the docker container, and dev state were torn down afterward.

## What's next

- **Per-app member grant mechanism** — needs a spec home (`AUTH.md` + the instance model) before Home can honestly show "household apps a member is *permitted* to open"; today every member sees every household instance (deliberate stepping-stone, [owner-scoped-instances.md](owner-scoped-instances.md)). When it lands, update both `store.ListVisibleTo` and `api.canSee` (the `TestVisibilityPredicatesAgree` tripwire catches divergence).
- **Store install UX** — admin scope picker (Household / Just for me) + the warn-don't-block duplicate-confirm dialog against the `409 duplicate-install` wire shape.
- **App detail page** — gives uninstall/restart/per-app settings a real home and retires the Settings stopgap list.
- **Health/degraded surfacing + notification bell** — the `useHealth()` Pinia store, global banner, and `<HealthGated>`, landing alongside queue item #2 (`NOTIFICATIONS.md`).
- **Files** — awaits `FILES.md` (`NEXT.md` Tier-1).
