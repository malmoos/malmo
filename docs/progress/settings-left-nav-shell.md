# Settings left-nav shell

- **Status:** done
- **Date:** 2026-06-10
- **Specs touched:** `DASHBOARD.md`

Restructured Settings from a single long scrolling page into a two-pane shell: a sidebar of sections on the left and the active section's content on the right (the Tailwind "sidebar application shell" pattern). Each section is its own nested route under `/settings`, so sections deep-link and the avatar-menu links can land directly on them.

## What was done

### Web UI

- **`views/settings/SettingsLayout.vue`** — the shell. A role-filtered nav rail (sidebar on `md+`, a horizontal scroll/tab strip on narrow screens, matching the mobile-first dock) plus a `<RouterView>` content pane. The Users nav item is hidden from members.
- Split the old `SettingsView.vue` into three section components — **`AccountSection.vue`** (identity + self-service password change), **`NotificationsSection.vue`** (per-category bell mutes), **`InstalledAppsSection.vue`** (the manage/uninstall/logs list). Logic moved verbatim; only imports were rewritten to the `@/` alias.
- Moved `UsersView.vue` → **`UsersSection.vue`** and `ActivityView.vue` → **`ActivitySection.vue`**, dropping their `← Settings` breadcrumbs (the sidebar is the navigation now). Guards, queries, and elevation flows unchanged.
- **`AboutSection.vue`** — a minimal product-identity panel (name, tagline, GitHub link). Deliberately carries no version/box-name, because no such endpoint is exposed to the UI yet (see "What's next").
- **`router.ts`** — `/settings` is now a parent route rendering `SettingsLayout` with children `account` / `notifications` / `apps` / `activity` / `users` / `about`; bare `/settings` redirects to `account`. The old `/settings/users` and `/settings/activity` paths are preserved as the same nested children, so existing links keep working.
- Deleted `SettingsView.vue`, `UsersView.vue`, `ActivityView.vue`. Updated the stale `AppLogs.vue` comment that named `SettingsView`.

### Docs

- `DASHBOARD.md` # global navigation — documented Settings as a left-nav shell and listed the built section set + role gating, keeping the sidebar visuals as implementation-time UX.
- `docs/dev/web-ui.md` — updated the views file tree to the `settings/` subfolder and added an "Add a Settings section" recipe.

## Verification

`make check-web` (typecheck + production build) passes; all six section chunks build.

## What's next

- The reserved admin **Storage / Network / System** sections aren't built — they were left out rather than stubbed as empty panes until they have real backing data. They slot in as additional nav items + nested routes when implemented.
- **About** is intentionally thin: there is no version/build or box-name surface exposed to the UI today. When the brain exposes one, About grows to show it.
- This was a frontend-only restructure — no brain, API, or OpenAPI change.
