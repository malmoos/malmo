# Notification mutation failures surface as error toasts

- **Status:** done
- **Date:** 2026-06-02
- **Specs touched:** none changed (realizes part of `WEB_UI.md`'s toast model — the error-feedback path; no divergence)

Closes issue #44. The notification UI mutations roll back optimistic state on failure but never tell the user anything went wrong, so a 5xx or network blip reads as a glitch: the Settings mute toggle flicks back with no explanation, and the bell's read-state actions (mark-read / dismiss / mark-all-read) fail with no visible change at all. This adds the missing user-visible failure feedback — a lightweight, ephemeral, non-blocking error toast — without touching the (already-correct) rollback or any backend.

## What was done

A small app-wide toast channel, then one `onError` per affected mutation:

- `web-ui/src/toasts.ts` (new) — a module-singleton ephemeral toast list plus `pushErrorToast(message)`, `dismissToast(id)`, and `useToasts()`. Same shape as `auth.ts`'s singleton state (a `reactive` list at module scope + exported functions), which is the codebase's established pattern for ephemeral client state — no Pinia store exists yet, and this isn't the place to introduce the first one. Each toast auto-dismisses after 6 s; ids come from a monotonic counter so dismissal never hits the wrong row.
- `web-ui/src/components/ToastHost.vue` (new) — renders the live list once, fixed bottom-right (clear of the centered `Dock`, which sits `bottom-4 z-20`; the host is `z-100`). Self-contained scoped CSS, matching `NotificationBell.vue`'s chrome-widget idiom rather than reaching for reka-ui's Toast primitive (the codebase's existing dropdown chrome is hand-rolled scoped CSS too). The region is `role="alert"` / `aria-live="assertive"` so screen readers announce failures; `pointer-events:none` on the empty region lets clicks fall through, re-enabled per toast so the dismiss button stays clickable.
- `web-ui/src/components/AppShell.vue` — mounts `<ToastHost />` once in the signed-in shell, next to `<Dock />`. AppShell (not `App.vue` root) is the right home: these mutations only fire from signed-in views, and AppShell already owns app-wide chrome (the single `useEvents()` SSE subscription).
- `web-ui/src/useNotifications.ts` — `markRead` / `markAllRead` / `dismiss` gain an `onError` that pushes a friendly, per-action toast. They had no `onError` at all (and no optimistic `onMutate`), so previously a failure was completely invisible; `onSettled: invalidate` stays, so the cache still reconciles to the server's truth.
- `web-ui/src/useNotificationMutes.ts` — `setMuted.onError` keeps its existing snapshot rollback and now also pushes a toast. Rollback first, then toast.

Wording is friendly and fixed per action ("Couldn't mark that as read. Try again.", "Couldn't update notification settings. Try again.", etc.) rather than echoing raw `ApiError` text — the audience is the non-technical "tech-curious adult" (`CLAUDE.md` # Audience), for whom "Internal Server Error" is noise. The one shared helper (`pushErrorToast`) backs every call site; no bespoke per-site UI.

## How it maps to the specs

- `WEB_UI.md` # Health & degraded mode surfacing — the toast model ("ephemeral, in-tab", "no modal, no interrupt", lines ~64/66) is realized for the error path. The spec also envisions confirm/success toasts ("toast on clear") and a 409 "View" toast; those are **not** built here (out of scope for #44) but extend this same channel when they land.
- `CLAUDE.md` # Simplicity / Surgical changes — minimal: two new files, three two-or-fewer-line edits, every changed line traces to the issue. No backend change, no new dependency, no abstraction beyond the one shared helper the issue asked for.

## Known gaps & deviations

- **Error-only.** `toasts.ts` exposes only `pushErrorToast`; there is no severity/kind parameter yet. That's deliberate (`CLAUDE.md` # Simplicity — no speculative generality): the only caller today is failure feedback. Adding success/info kinds is a small generalization of `pushErrorToast` → `pushToast({kind, message})` when `WEB_UI.md`'s "toast on clear" / 409 "View" toasts are built.
- **Not reka-ui's Toast primitive.** `WEB_UI.md` locks reka-ui as the component layer, but the imperative push model and the codebase's existing hand-rolled chrome (`NotificationBell.vue`'s dropdown is scoped CSS, not reka-ui Popover) make a self-contained `ToastHost` the lower-friction, precedent-consistent choice. A future toast-system pass can move to reka-ui Toast if richer behavior (swipe-dismiss, stacking semantics) is wanted.
- **No dedup / no stack cap.** Mashing a failing toggle on a down network can briefly stack a few toasts; each auto-dismisses at 6 s. Not worth a dedup mechanism at this scale (`CLAUDE.md` # Simplicity).
- **Verification is build + manual.** There is no FE test framework in the repo yet (noted in the issue). `npm run build` (`vue-tsc --noEmit && vite build`) passes; the failure path was reasoned through, not automated.

## Tests

None added — no frontend test framework exists yet. Gate is `npm run build` (typecheck + production build), which passes. Manual verification path: induce a mutation failure (offline tab / 5xx) and confirm a toast appears and auto-dismisses while the optimistic state still rolls back.

## What's next

- **Generalize the channel** to success/info toasts when `WEB_UI.md`'s "toast on clear" and the 409 `blocked-by-health-issue` "View" toast are built — both ride this same `toasts.ts` singleton.
- **A frontend test framework** (Vitest + Vue Test Utils) would let the failure-path feedback be asserted rather than reasoned through; tracked as a broader FE-tooling follow-up, not part of this slice.
