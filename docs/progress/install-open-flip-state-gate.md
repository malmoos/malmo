# Install button flips to "Open" ~1s in before the app is actually up

- **Status:** done
- **Date:** 2026-07-03
- **Specs touched:** none — a rendering bug in state the brain already emits (`DASHBOARD.md` # install authorization unchanged). Closes #297. Follows [install-phase-spinner.md](install-phase-spinner.md) (#150) and [install-pending-button-state.md](install-pending-button-state.md) (#114).

The app detail page's Install button dropped its "Installing…" phase spinner about a second after you clicked Install and flipped straight to **Open** — but the app was not installed yet (clicking Open pointed at an app that wasn't running). The live phase labels added by #150 never had a chance to show.

## Root cause

The brain creates the instance row in state `installing` and emits `app.state_changed` at the very **start** of the install job — `internal/lifecycle/lifecycle.go` (`State: "installing"` then `m.emitState(inst, "absent")`), right after slug allocation and long before image pull / `compose up` / health-wait. The dashboard's global SSE listener (`web-ui/src/useEvents.ts`) invalidates `["apps"]` on `app.state_changed`, so the apps list refetches and the instance appears within ~1s.

`useInstall`'s `householdInstance` / `ownPersonalInstance` matched purely on `manifest_id` / `scope` / `owner_user_id` and **ignored `state`**, and `AppDetailView`'s header rendered the "Open" link off the mere presence of that instance (`v-else-if="ownPersonalInstance"`), hiding the `SplitButton` (`v-if="!ownPersonalInstance"`). So the moment the installing-state row landed, the loading button unmounted and a dead "Open" link took its place — for the whole rest of the job. `AppTile` already gates its open affordance on `state === "running"`; the detail page didn't.

## What was done

Frontend-only, two files, no brain / OpenAPI / type change.

- **`web-ui/src/useInstall.ts`** — the `installing` computed now also reads true when the caller-relevant instance row is in the `installing` state (`householdInstance?.state === "installing"` / `ownPersonalInstance?.state === "installing"`), in addition to the existing local-mutation / POST-pending signals. This covers the SSE-refetch-lands-before-the-mutation-resolves window and a mid-install page reload (where there's no local mutation at all).
- **`web-ui/src/views/AppDetailView.vue`** — the header "Open shared app" / "Open" links now require `state !== 'installing'`, and the `SplitButton` shows whenever the caller has no own instance **or** their own instance is still `installing`. The button therefore stays on its live phase label for the whole job and only flips to "Open" once the instance reaches a past-installing state (`running`, or the pre-existing stopped/failed handling, unchanged).

Net effect: Install → "Installing…" with live phase labels for the full download/start window → "Open" only when the app is actually up.

## Verification

- `make check-web` (vue-tsc typecheck + vite build) passes. The web-ui has no unit-test harness (established posture — see #150); the change is covered by the typecheck/build gate plus the reasoning above. Behaviorally exercised by reading the two state machines against the brain's emit order (row-create `installing` emit → running emit at job end).

## What's next

- The `state !== 'installing'` gate keeps the pre-existing behavior for `stopped` / `failed` personal instances (they still render an "Open" link on the detail page, while `AppTile` treats them as inert). If the store detail page should also reflect stopped/failed distinctly, that's a separate, broader affordance change.
- The deeper seam is still the poll-based `waitForJob`; a real `useJob(jobId)` composable (`docs/dev/web-ui.md` # `useJob()`) remains the consolidation point if more surfaces need live job state.
