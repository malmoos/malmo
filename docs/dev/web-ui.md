# web-ui code architecture

How the dashboard front-end is built, as it exists in the repo today. This is the **code-level map** for someone about to edit `web-ui/`. It is the third leg of three web-ui docs — read them in this order:

- **`docs/specs/WEB_UI.md`** — the design source of truth: stack picks, deploy model (`molma-ui` container), versioning posture, and the architectural rules ("server state lives in Query", "SSE is the cache-invalidation channel", "`<script setup>` only"). Read it first; this doc assumes those decisions.
- **`docs/architecture.md`** — the *external* contract: how the browser, web-ui, brain, and Caddy wire together (REST `/api/v1/*` + SSE, cookie auth).
- **This doc** — the *internal* shape: folder layout, the cross-cutting modules, state model, and recipes for adding a view/component/query.

For how to *run* it (Vite dev server, Node version, CI), see [`running-locally.md`](running-locally.md). For the API contract the UI consumes, see `docs/specs/BRAIN_UI_PROTOCOL.md`.

## Stack, as built

Vue 3 (Composition API, `<script setup>` only) + Vite 5 + TypeScript `strict` (with `noUncheckedIndexedAccess`). Server state through `@tanstack/vue-query` v5; routing through Vue Router 4 (history mode); styling through Tailwind CSS 4 (CSS-config via `@theme`, no `tailwind.config.js`). Icons via `lucide-vue-next`. `reka-ui` + the `cn()` helper (`clsx` + `tailwind-merge`) are present as the shadcn-vue scaffolding so components can be added via the shadcn CLI later — but no shadcn components have been copied in yet; views use plain elements with Tailwind classes and the design tokens in `style.css`.

`main.ts` is the whole bootstrap: `createApp(App)` with Pinia, the router, and `VueQueryPlugin`. That's it — twelve lines.

### Where the as-built diverges from `WEB_UI.md`

The spec is the target; a few picks haven't landed yet. Don't treat these as bugs — they're staged work:

- **Package manager is npm, not pnpm.** There's a `package-lock.json` and `running-locally.md`/CI use `npm ci`. The spec names pnpm; revisit if/when we switch.
- **No ESLint/Prettier yet.** The spec lists them; `package.json` has neither. `vue-tsc --noEmit` (run by `npm run build` and CI) is the only automated gate today.
- **Pinia is registered but unused.** The spec reserves Pinia for client-side state; in practice the cross-cutting client state (auth, toasts, elevation) is held as **module-singleton refs** (see below), which is simpler at this size. Pinia is wired in `main.ts` and ready when a real store appears (the spec's `useHealth()` health store is the likely first).
- **`useJob()` is `waitForJob()` for now.** The spec's `useJob(jobId)` composable (a `useQuery` with `refetchInterval`) isn't built; `api.ts` has a plain poll loop instead. Fine for the skeleton.
- **`useHealth()` / `<HealthGated>` / degraded-mode banners** (WEB_UI.md # Health & degraded mode) are not built yet.

When you close one of these gaps, delete its bullet here in the same change.

## Folder layout

```
web-ui/
├── index.html              # Vite entry; mounts #app
├── vite.config.ts          # @ → src/ alias; dev proxy /api → brain (SSE-aware)
├── tsconfig.json           # strict + noUncheckedIndexedAccess; @/* path
├── package.json            # npm scripts: dev / build / preview / gen:api
└── src/
    ├── main.ts             # app bootstrap (Pinia + router + Vue Query)
    ├── App.vue             # auth-aware root: bootstrap → Setup | AppShell | Login
    ├── style.css           # Tailwind 4 entry + @theme design tokens
    ├── router.ts           # Vue Router 4 route table (lazy-imported views)
    │
    ├── api.ts              # fetch wrapper + ApiError + generated-type re-exports
    ├── auth.ts             # session lifecycle: bootstrap/login/setup/logout
    ├── useEvents.ts        # one SSE subscription → Query cache invalidation
    ├── toasts.ts           # app-wide ephemeral error-toast channel
    ├── elevate.ts          # 5-min elevation flow + withElevation() wrapper
    ├── useNotifications.ts # notification list/badge queries + mutations
    ├── useNotificationMutes.ts
    │
    ├── generated/
    │   └── openapi.ts      # GENERATED from api/openapi.json — do not hand-edit
    ├── lib/
    │   └── utils.ts        # cn() class-merge helper (shadcn convention)
    │
    ├── useInstall.ts       # catalog-app install flow (plan fetch, consent dialog,
    │                       #   duplicate/job errors, per-app button state) — shared
    │                       #   by AppDetailView; see "Install flow" below
    │
    ├── views/              # one component per route (lazy-loaded)
    │   ├── HomeView.vue        # installed-app grid
    │   ├── StoreView.vue       # catalog browse grid (cards → detail page)
    │   ├── AppDetailView.vue   # /store/:id — app detail page; Install lives here
    │   ├── CustomInstallView.vue  # Door-2 custom-container form (admin-only)
    │   ├── FilesView.vue
    │   └── settings/           # Settings left-nav shell + its sections
    │       ├── SettingsLayout.vue        # sidebar + nested-route content pane
    │       ├── AccountSection.vue        # identity + self-service password change
    │       ├── NotificationsSection.vue  # per-category bell mutes
    │       ├── InstalledAppsSection.vue  # manage/uninstall/logs list
    │       ├── ActivitySection.vue       # audit-log browser (all users)
    │       ├── UsersSection.vue          # admin-only user management
    │       └── AboutSection.vue          # product identity
    │
    └── components/         # reusable chrome + dialogs
        ├── AppShell.vue        # signed-in chrome; mounts useEvents() once
        ├── TopBar.vue, Dock.vue
        ├── AppTile.vue         # dashboard launcher tile (opens the app)
        ├── StoreAppCard.vue    # store browse card (links to the detail page)
        ├── SplitButton.vue
        ├── InstallDialog.vue, ElevateDialog.vue
        └── ToastHost.vue
```

A handful of top-level `.vue` files (`Login.vue`, `Setup.vue`, `NotificationBell.vue`, `LiveResources.vue`) sit directly in `src/` rather than `components/` — they're the pre-shell / standalone surfaces. New reusable components go in `components/`; new routed screens go in `views/`.

## State model — three tiers, in order of preference

1. **Server state → TanStack Query.** Everything fetched from the brain (apps, catalog, users, notifications, jobs) goes through `useQuery`/`useMutation` keyed by a stable array (`["apps"]`, `["notifications"]`, …). One cache, one source of truth. This is the load-bearing rule from `WEB_UI.md` — never stash fetched data in a local `ref` that can drift.
2. **Client state → module-singleton refs** (today) / Pinia (when it grows). `auth.ts`, `toasts.ts`, and `elevate.ts` each export a module-level `ref`/`reactive` plus imperative functions and a `useX()` accessor returning computed views. Any module can import and mutate; components read reactively. This is deliberately not Pinia yet — see the divergence note above.
3. **Component-local `ref`** for form drafts and view-local UI toggles.

### The cache-invalidation channel

`useEvents.ts` opens **one** `EventSource("/api/v1/events")` and is called exactly once, in `AppShell.vue` (so it covers every signed-in view). It does not carry payloads into components — it listens for event kinds (`app.state_changed`, `app.installed`, `app.uninstalled`, `notification.created`, `notification.updated`) and calls `queryClient.invalidateQueries(...)`. Components stay pull-only via `useQuery` and re-render when the relevant query refetches. Push and pull share the one cache (WEB_UI.md, BRAIN_UI_PROTOCOL.md Pattern C). When you add a new live-updating resource, add its event kind(s) here rather than subscribing from the component.

## Cross-cutting modules

- **`api.ts`** — the ~30-LOC `fetch` wrapper. `api.get/post/put/patch/del` prepend `/api/v1`, send `credentials: "include"`, and normalize both error shapes the brain emits (huma's `{detail,title,errors}` and the jobs `{code,message}`) into a typed `ApiError(code, message, status)`. A 401 from *any* call fires the `onUnauthenticated` handler (registered by `auth.ts`) to drop the session. It also re-exports the **wire types** as friendly aliases (`User`, `Instance`, `CatalogEntry`, …) sourced from `generated/openapi.ts`, plus a few hand-maintained types for endpoints that bypass huma codegen (the Door-2 custom-install request/result types, and the `Scope` literal union the generator emits as a bare string).
- **`auth.ts`** — owns the session lifecycle and the `currentUser`/`hasUsers`/`booted` singletons that drive `App.vue`'s three-way branch. `bootstrap()` runs `GET /auth/state` → (`/me` | login | setup). `setup()`/`setupComplete()` are split intentionally so the Setup view stays mounted to show the one-time recovery code before flipping to the shell. Call `refreshCurrentUser()` in the `onSettled` of user-management mutations so `single_user_mode` stays accurate without a reload.
- **`elevate.ts`** — the 5-minute re-prompt window for destructive admin ops (`USERS_AND_GROUPS.md` # Elevation in the UI). Wrap a mutation in `withElevation(fn)`: it runs `fn`, and on a `403 elevation_required` it drives the single `ElevateDialog` (mounted in `AppShell`), elevates the session, and retries once. Inside a live window the prompt never shows. A user cancel rejects with `elevationCancelled` — map it to a no-op, not an error.
- **`toasts.ts`** — app-wide ephemeral feedback. `pushErrorToast(message)` from anywhere; `<ToastHost>` (mounted in `AppShell`) renders the live list, auto-dismissing after 6s. Error-only today (the rollback feedback for optimistic notification mutations); success/confirm toasts extend this same channel when they land.

## Routing

`router.ts` is a flat lazy-imported table (history mode). Four primary destinations mirror the dock (`DASHBOARD.md` # global navigation): Home, Files, Store, Settings. Admin-only screens (`/store/custom`, `/settings/users`) **guard the role inside the view component** rather than via a router guard — follow the `CustomInstallView` pattern when adding another admin-only screen. Unknown paths redirect to `/` so the SPA never 404s its own chrome (production Caddy also serves `index.html` for unmatched routes).

The Store is a **browse → detail** pair: `/store` (`StoreView`) is a flat grid of `StoreAppCard`s (logo + name) that link to `/store/:id` (`AppDetailView`), the app-store-style detail page where the description, screenshots, and the Install flow live. `/store/custom` is declared before `/store/:id` (and Vue Router ranks the static segment higher anyway, so `custom` never matches the `:id` param).

## Install flow

`useInstall(manifestId)` (`src/useInstall.ts`) owns the catalog-app install flow so the detail page renders it without re-implementing it: the advisory install-plan fetch (enabled only while the consent dialog is open), the install mutation with its three error branches (409 duplicate → warn-don't-block banner, 422 election → inline dialog error, mid-job failure → standalone banner), and the per-app button state (does the caller already have a household / own-personal instance?). The view supplies wording and renders `InstallDialog` from the returned `activePlan`. It reads/writes the shared `["apps"]` query cache, so an install elsewhere reflects here and vice versa.

## Styling

`style.css` is the Tailwind 4 entry: `@import "tailwindcss"` then an `@theme` block of design tokens named to the **shadcn-vue CSS-variable convention** (`--color-background`, `--color-card`, `--color-accent`, …) so shadcn components added later inherit the palette automatically. Light theme only for now (dark-mode trigger is deferred). Use these semantic token classes (`bg-background`, `text-muted-foreground`, `border-border`) rather than raw hex so the eventual dark theme is a token swap.

## OpenAPI codegen workflow

The brain's huma handler structs are the single source of truth for wire types. `src/generated/openapi.ts` is generated by `openapi-typescript` from `api/openapi.json`:

```
npm run gen:api      # openapi-typescript ../api/openapi.json -o src/generated/openapi.ts
```

Regenerate after any brain DTO change, and re-export the new schema as a friendly alias in `api.ts` (don't import `components["schemas"][...]` from call sites). CI's `make openapi-check` keeps the committed `api/openapi.json` in sync with the Go code, so the generated types can't silently drift. Never hand-edit `generated/openapi.ts`. See `docs/progress/openapi-codegen.md` for the why (including the `package.json` `overrides` pinning the codegen dependency closure to pre-May-2026 releases).

## Recipes

**Add a screen:** create `views/FooView.vue` (`<script setup>`), add a lazy route in `router.ts`, link it from `Dock.vue` or the relevant parent. Admin-only? Guard the role in the view like `CustomInstallView`/`UsersSection`.

**Add a Settings section:** create `views/settings/FooSection.vue`, add a nested child route under `/settings` in `router.ts`, and add a nav item to `SettingsLayout.vue` (`adminOnly: true` hides it from members). The section renders inside the shell's content pane — no breadcrumb or back-link; the sidebar is the navigation.

**Fetch brain data:** `useQuery({ queryKey: ["foo"], queryFn: () => api.get<Foo>("/foo") })`. If the brain emits an SSE event when `foo` changes, add that event kind to `useEvents.ts` to invalidate `["foo"]`. Never copy query results into a standalone `ref`.

**Mutate brain state:** `useMutation` calling `api.post/put/del`. Destructive/admin op? Wrap the mutation fn in `withElevation(...)`. On failure of an optimistic mutation, `pushErrorToast(...)` and roll back. Touches the current user's role/mode? `refreshCurrentUser()` in `onSettled`.

**Add a wire type:** change the Go DTO, regenerate (`npm run gen:api`), add the alias export in `api.ts`.
