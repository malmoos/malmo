# Notification mute settings UI — per-category toggle list in Settings

- **Status:** done
- **Date:** 2026-05-31
- **Specs touched:** docs/specs/NOTIFICATIONS.md

## What was done

The dashboard surface for the per-category mute API that landed backend-only in [notification-category-mute.md](notification-category-mute.md). A **Settings → Notifications** section now lists the six notification categories, each with an on/off switch — *on* = receiving, *off* = muted — so a fresh account (no mute rows) reads as everything-on.

- `web-ui/src/useNotificationMutes.ts` (new) — TanStack Query composable over the mute API: `GET /api/v1/notifications/mutes` → `{ muted: string[] }`, and a `setMuted` mutation that maps `mute → PUT`, `unmute → DELETE /api/v1/notifications/mutes/{category}`. The mutation is **optimistic** (flip the cached set in `onMutate`, roll back in `onError`) because a toggle's whole point is instant feedback, then `onSettled` invalidates the whole `["notifications"]` prefix to reconcile the mute set and the now-refiltered inbox/badge. Lives under `["notifications","mutes"]` so the existing `useEvents` SSE handler (`notification.updated`, which the brain publishes on a mute change) refreshes it for free.
- `web-ui/src/views/SettingsView.vue` — new "Notifications" section rendering the category list with a reka-ui `SwitchRoot`/`SwitchThumb` per row. Switch state is `!mutedSet.has(category)`; `@update:model-value` calls `setMuted`. Category display metadata (label + one-line description) is defined in the view — the **brain owns the taxonomy, the UI owns the wording**.
- `web-ui/src/api.ts` — added a `put` method to the thin fetch wrapper (mute is the first `PUT` consumer in the UI).

## How it maps to the specs

- Realizes the **Surface (as implemented)** note added to `NOTIFICATIONS.md` # Configuration — closes the loop on the "per-user, per-category mute; everything on by default" locked decision, which had API + read-time-filter but no in-product control.
- Follows `WEB_UI.md`: server state in Query (not Pinia); push and pull share one cache (the SSE channel invalidates the same `["notifications"]` keys the mute query lives under).
- Mute toggling is **not audited** (personal view preference) — consistent with the backend slice and `CLAUDE.md`.

## Known gaps & deviations

- **Taxonomy is mirrored, not fetched.** The six categories + their order are hard-coded in `SettingsView.vue` to match the wire contract (`notify.Categories`). There is no "list all categories" endpoint — only the muted subset — so a new brain category needs a matching row in the view or it gets no toggle. Same coupling as the hard-coded `Scope`/severity enums in `api.ts`; acceptable for v1.
- **No FE unit test.** The web-ui has no test framework (`package.json` build = `vue-tsc --noEmit && vite build`); verified by typecheck + production build (green) over the already-tested mute API (`internal/api/notifications_test.go`). A live click-through wasn't run — the full stack (brain + host-agent + caddy) wasn't brought up for this slice.
- **Criticals are still muted by a category mute** — unchanged from the backend slice; the "should criticals ring through" carve-out remains an open question (`NEXT.md` # Observability).

## What's next

- **Notification retention / pruning** (`NEXT.md` # Observability) — the remaining half of the queued "mute settings UI + retention" item. Cap the `notifications` + `notification_reads` tables (per-recipient? global? resolved-first?) and pick where the prune runs. Brain-side, no UI.
- Settles the open "should criticals bypass a category mute" question once support signal exists.
