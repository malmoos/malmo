# App page: "Installing…" through the POST-pending window

- **Status:** done
- **Date:** 2026-06-10
- **Specs touched:** No design change — `DASHBOARD.md` # Install authorization already intends a single uninterrupted "Installing…" state from consent to completion, and `docs/dev/web-ui.md` documents the `useInstall` flow this lives in. This closes a state gap in the implementation.

Closes issue #114. Installing an app from the app detail page had a feedback gap: between clicking the consent dialog's **Install** and the POST being accepted, the dialog had already unmounted (it's gated on `!install.isPending.value`) but the page's Install button still read "Install" and was clickable — `installing` was `installingId === manifestId`, and `installingId` is only set once the POST returns 202. For a full network round-trip the user saw no progress and could re-click, firing a duplicate POST.

## What was done

One change, in `web-ui/src/useInstall.ts`: the `installing` computed (which drives the page button's label + disabled state) now also covers the POST-pending window:

```ts
const installing = computed(
  () =>
    installingId.value === manifestId.value ||
    (install.isPending.value && pendingRequest.value?.manifest_id === manifestId.value),
);
```

### Why not the issue's sketched one-liner

The issue suggested `installingId === manifestId || install.isPending.value`, reasoning "the mutation is per-composable (one `manifestId`), so `isPending` is already app-scoped". That premise doesn't hold: AppDetailView's route component is **reused across `/store/:id` navigations** — only the `manifestId` ref changes, the composable and its in-flight mutation persist (the `watch(manifestId, …)` reset exists precisely because of this). With bare `isPending`, a background install for app A would mark app B's page as "Installing…" after navigation.

`pendingRequest` is the right scoping signal: it's set in `handleSubmit` *before* `mutate()` (so it covers the whole pending window with no gap), it carries the request's `manifest_id`, it's preserved unchanged by the duplicate-retry path (`handleConfirmDuplicate` re-sends with the same `manifest_id`), and it's cleared by the `manifestId` navigation watch via `closeDialog()` — exactly the boundary where the pending state should stop applying to the page.

### Verified (per the issue's checklist)

- **No flicker:** the dialog's unmount (`!install.isPending.value` in its `v-if`) and the button's flip to disabled "Installing…" react to the same `isPending` transition, so they swap within one render flush — there is no frame where neither a dialog nor a disabled button is visible.
- **InstallDialog's confirm button needs no pending state of its own:** the dialog unmounts the instant the mutation goes pending; the page button is the single source of "Installing…".
- **Duplicate-retry path:** `handleConfirmDuplicate`'s retry POST shows "Installing…" through the same clause — `pendingRequest` survives the 409 (only `closeDialog`/navigation clear it) and the retry keeps its `manifest_id`.
- **422 election-rejection path unchanged:** on a POST-time 422 the dialog re-mounts after the mutation settles and shows the inline `dialogError`, same as before — `installing` drops back to false because `isPending` does.
- `make check-web` (vue-tsc type-check + production build) passes.

## Known gaps & deviations

- **Pre-existing edge left alone:** navigating away from an installing app and back to it mid-job shows a plain "Install" button — the `manifestId` watch deliberately clears `installingId`/`pendingRequest` on navigation (the job itself keeps running; the apps-list invalidation flips the button to "Open" on completion). That behavior predates this fix and is a separate design question (per-app install state keyed by id rather than per-composable refs), not part of #114's done-when.
- **No frontend unit test:** web-ui still has no test runner; verification is the type-check + build gate plus the flow trace above.

## What's next

- Nothing follow-up from this slice. The per-app-keyed install state mentioned above is only worth doing if the navigate-away-and-back case becomes a real complaint.
