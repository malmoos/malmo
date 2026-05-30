# Notification Web UI: dashboard bell + dropdown inbox

- **Status:** done
- **Date:** 2026-05-29
- **Specs touched:** `NOTIFICATIONS.md` (# Knock-ons — `WEB_UI.md` line reconciled to "server state in Query, not Pinia")

The render half of the bell. [0026](0026-notification-read-surface.md) landed the backend read surface (`/api/v1/notifications` family, `notification_reads` per-recipient join, `notification.created` / `notification.updated` SSE kinds) but nothing rendered it. This slice adds the dashboard-chrome bell: an unread-count badge, a click-to-open dropdown inbox, and live SSE-driven updates. Frontend only — no brain changes.

> Stacked on the [0026](0026-notification-read-surface.md) re-land branch. (0025+0026 were stranded off `main` by a stacked-merge that landed them in their parent branches instead of `main`; PR #23 re-lands the backend, this slice stacks on it.)

## What was done

### `useNotifications()` composable — `web-ui/src/useNotifications.ts` (new)

Wraps the read surface as TanStack Query state, the sibling of the catalog/apps queries in `Dashboard.vue`:

- `list` — `useQuery(["notifications","list"])` → `GET /api/v1/notifications` (newest-first, the caller's audience-scoped inbox).
- `unreadCount` — `useQuery(["notifications","unread-count"])` → `GET /api/v1/notifications/unread-count`. The badge reads the **dedicated count endpoint**, not a derived `list.filter(!read).length`, so it reflects every unread row including ones past the first page.
- `markRead` / `markAllRead` / `dismiss` — `useMutation`s posting to the respective endpoints; each `onSettled` invalidates the `["notifications"]` key prefix so list + badge refetch together.

Server state goes through Query, not Pinia, per `WEB_UI.md`'s "server state lives in Query" rule — the only client-side notification state is the dropdown's open/closed flag, which is a component-local `ref`.

### Live updates — `web-ui/src/useEvents.ts`

Extends the single shared `EventSource` (already invalidating `["apps"]` on app events) to also listen for `notification.created` / `notification.updated` and invalidate `["notifications"]`. Consistent with the push/pull-share-one-cache pattern (`WEB_UI.md`): the SSE payload is an advisory refetch trigger (the global bus is unfiltered), so the client re-reads its own audience-scoped list rather than merging event bodies — which is what keeps per-recipient scoping correct.

### `NotificationBell.vue` — `web-ui/src/NotificationBell.vue` (new)

The chrome bell (`NOTIFICATIONS.md` # Surfaces):

- **Bell + badge.** A bell button with a red unread-count pill (hidden at 0, `99+` cap).
- **Dropdown inbox** on click: reverse-chronological, **grouped Unread / Earlier**, each row severity-colored (a dot keyed on `info` / `warning` / `error` / `critical`), with summary, optional body, relative time, a `resolved` tag when `resolved_at` is set, and the action label as an intent hint. A per-row `×` dismisses (`@click.stop` so it doesn't also mark-read); clicking a row marks it read. A "Mark all read" affordance shows while the badge is non-zero.
- **No modal / no forced interrupt** (`NOTIFICATIONS.md` # Surfaces): the dropdown is click-to-open and closes on click-outside (a `document` listener added/removed in `onMounted`/`onUnmounted`).
- Empty state ("You're all caught up.") and loading state.

### Wire type + chrome wiring — `web-ui/src/api.ts`, `web-ui/src/Dashboard.vue`

- `api.ts` gains the `Notification` interface mirroring `NotificationDTO` (routing fields stay server-side; `read` is the per-recipient bool; `ts` / `resolved_at` are unix epoch ms).
- `Dashboard.vue` mounts `<NotificationBell>` in the header (`margin-left: auto`); the header's `align-items` moves `baseline → center` so the icon button aligns with the title.

## How it maps to the specs

- `NOTIFICATIONS.md` # Surfaces: bell always present, unread badge scoped to the current user, reverse-chron severity-colored dropdown grouped by read/unread with relative time + action hint, live SSE updates, no modal.
- `NOTIFICATIONS.md` # Lifecycle: dismiss removes from the active inbox but is not resolve; resolved (not deleted) rows still render, tagged.
- `WEB_UI.md`: server state in TanStack Query (not Pinia); the push (SSE invalidation) and pull (`useQuery`) share one cache; `useNotifications()` composable; thin `api` fetch wrapper reused verbatim.
- `BRAIN_UI_PROTOCOL.md`: consumes the `/api/v1/notifications` family and the `notification.created` / `notification.updated` SSE kinds from [0026](0026-notification-read-surface.md).

## Known gaps & deviations

- **No deep-link navigation.** This app has no client router (`App.vue` is a 3-way manual view switch), so `action_route` can't be navigated to — the action label renders as an intent hint only. Wiring it lands when a router does (`WEB_UI.md`).
- **No pagination / load-more.** The list shows the first page (server default 50). The `after_id` cursor exists server-side; infinite scroll / "load older" is deferred.
- **Member transparency variant + "all clear" still backend-deferred.** No producer emits `audience: user` notifications or the resolved-follow-up yet (carried from [0025](0025-health-notifications.md) / [0026](0026-notification-read-surface.md)); the bell renders them correctly when they appear.
- **No per-category mute UI.** Deferred (`NOTIFICATIONS.md` # Configuration) — a preferences surface, orthogonal to the inbox.
- **No automated UI test.** `web-ui/` has no test harness yet; the gate is `vue-tsc --noEmit` + `vite build` (both green) plus the live render check.
- **Retention/pruning** still unimplemented (carried over) — rows accumulate.

## What's next

- **Member transparency variant + "all clear"** (brain): emit the info-only member copy for box-blocking criticals and the resolved follow-up on clear (`NOTIFICATIONS.md`). The bell already renders both.
- **Per-category mute.** Per-user mute preferences surface (`NOTIFICATIONS.md` # Configuration).
- **Deep-link routing.** When a client router lands, make `action_route` navigate and close the dropdown.
- **Retention/pruning.** Capped-count / age policy for `notifications` + `notification_reads` (`NEXT.md`).
