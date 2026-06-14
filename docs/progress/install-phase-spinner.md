# Install button shows a live phase spinner (#150)

- **Status:** done
- **Date:** 2026-06-13
- **Specs touched:** none — this is a richer rendering of state the brain already emits (`DASHBOARD.md` # install authorization is unchanged). Dev how-to `docs/dev/web-ui.md` (# Install flow) updated to describe the live phase label.

Closes #150. Built on the `installing` state added by #114. The install button's feedback was text-only and static: once the POST was accepted it read **"Installing…"** for the whole image-pull / compose-up / health-wait window (many seconds on a real install), with no motion — it could look frozen, then jump straight to **Open**. The data to do better was already on the wire and thrown away: the brain's install job emits a fine-grained `step` throughout (`internal/lifecycle/lifecycle.go`: `admitting_compose` → `resolving_digests` → `compose_up` → `waiting_healthy` → `flipping_route` …) and `Job.step` is already on the generated client type, but `waitForJob` returned only at a terminal state, discarding every intermediate poll. This slice surfaces it as a spinner plus a live, friendly phase label on the button.

## What was done

Frontend-only, four files, no brain / OpenAPI / type change.

- **`web-ui/src/api.ts`** — `waitForJob(jobId, onPoll?)` gains an optional callback fired with each **non-terminal** poll, so callers can observe the running job's live `step` instead of only its terminal result. The poll loop and terminal-state contract are otherwise unchanged.
- **`web-ui/src/useInstall.ts`** — new reactive `currentStep` ref, set from the running job's `step` via the `onPoll` callback and cleared in `onSettled` alongside `installingId`. It is also cleared in the `manifestId` watch, so a background install for a previous app can't leak its phase onto a newly-navigated `/store/:id` page (same scoping the existing `installing`/`installingId` state already uses — the composable survives route changes). Exposed raw; **no wording lives here.**
- **`web-ui/src/components/SplitButton.vue`** — new `loading?: boolean` prop renders an `animate-spin` `Loader2` (lucide-vue-next) before the label. Inert by default, so the button's other call sites are unaffected.
- **`web-ui/src/views/AppDetailView.vue`** — a frontend-only `INSTALL_PHASES` map collapses the ~15 technical steps into three user-facing phases (**Preparing…** for the local setup steps, **Downloading…** for `resolving_digests`/`compose_up`, **Starting…** for `waiting_healthy`/`flipping_route`); the `installPhaseLabel` computed falls back to the generic **"Installing…"** for any unknown or empty step, so a newly-added brain step never surfaces raw. The wording is collapsed in the view (per the `useInstall` "all wording stays in the view" convention). The header `SplitButton` renders the spinner + phase label while installing. The duplicate-install **"Install my own copy"** button stays disabled while installing but shows no spinner: `handleConfirmDuplicate` clears `duplicateInfo` and calls `install.mutate` in the same synchronous tick, so Vue unmounts the duplicate panel before `installing` becomes true — the user sees the spinner on the header button instead.

UX per the issue: spinner + live phase text rendered **on the button** (no separate progress card, no determinate %, since `job.progress` is only ever 0→1 today). The button stays disabled for the whole POST-pending + job window, so there's no #114 regression and no duplicate POST.

## Verification

- `make check-web` (vue-tsc typecheck + vite build) passes. The web-ui has no unit-test harness (no vitest/jest, zero `*.test.*` — the established posture); adding one for a small UI change would be a new dependency against that posture, so the change is covered by the typecheck/build gate plus the reasoning above, not new tests.
- **CI caveat:** `make check-web` is currently red on `main` for an **unrelated, pre-existing** reason — a vue-tsc null-narrowing break in `LiveResources.vue` that **PR #170** fixes (and which #168 is already flagged "unstable" against). This PR's own diff is limited to the four install-button files and passes `check-web` cleanly on top of #170 (verified locally by temporarily applying #170's one-file fix, running the gate green, then reverting it so the committed diff stays surgical). CI on this PR goes green once #170 merges to `main`.

## What's next

- The phase map lives in `AppDetailView`; if a second surface ever needs the same labels (e.g. an installed-apps list showing in-flight installs), promote `INSTALL_PHASES` + `installPhaseLabel` to a small shared helper rather than duplicating the wording.
- `waitForJob`'s `onPoll` is the minimal seam for live job state. The spec's real shape is a `useJob(jobId)` composable (`useQuery` + `refetchInterval`, `docs/dev/web-ui.md` # `useJob()`); if more views start needing live job steps, that composable is the place to consolidate the poll loop.
