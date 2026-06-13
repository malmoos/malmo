# Failed app instances get a click-to-retry recovery path (#154)

- **Status:** done
- **Date:** 2026-06-13
- **Specs touched:** `DASHBOARD.md` (# Tile), `APP_LIFECYCLE.md` (# per-instance state machine, # stop, start, uninstall)

Closes #154, building directly on [start-reasserts-mdns.md](start-reasserts-mdns.md) (#153 — the reason `<slug>.local` returns on a recovery Start). Surfaced recovering a Jupyter instance (#136/#124) that installed straight into `failed`: once `failed`, the dashboard offered no way back — `Start`/`Stop` both rejected it, `Reconcile` skipped it, and the only documented escape was uninstall/reinstall (which re-triggers the original failure) or hand-editing SQLite. This gives `failed` a UI recovery path that reuses the existing Start transaction.

## What was done

The insight is that a *retry is exactly a Start*: `compose up -d` + `waitHealthy` + the Caddy splash→upstream flip + the #153 mDNS re-publish are precisely what a failed instance needs to recover. So rather than a parallel `Retry` method (which would duplicate ~40 lines of the Start transaction), the change **widens the `Start` guard** to accept `failed` alongside `stopped`.

**Backend (`internal/lifecycle/lifecycle.go`):**

- `Manager.Start`'s guard `inst.State != "stopped"` → `inst.State != "stopped" && inst.State != "failed"`. Everything downstream is unchanged: `publishHost` re-asserts the mDNS name up-front, state commits `running` first (brain-commits-first), `compose up -d` runs under the health-wait budget, and a retry that still won't go healthy lands back in `failed` through the same `startFailed` path (failed splash, row reset to `failed`). So success → `running` with name + route restored; persistent failure → `failed` again.
- One correctness fix the widening exposed: the success path emitted a **hardcoded** `"stopped"` as the previous state in the `app.state_changed` event. From a `failed` retry that prev is wrong, so the previous state is now captured (`prevState := inst.State`) before the optimistic `SetState` and passed to `emitState`. The event now reads `stopped→running` or `failed→running` correctly.

**Backend (`internal/api/api.go`):** `startApp`'s synchronous transition guard widened to `{stopped, failed}` (message → "app is not stopped or failed"); the 409 still fires for every other state. No new endpoint — the existing `POST /api/v1/apps/{id}/start` and its `authorizeAppMutation` (household = admin, personal = owner/admin) carry the retry, so authorization mirrors stop/start/uninstall for free. No OpenAPI/schema change (the widening is a runtime guard, not a contract change), so the generated TS client is untouched.

**`Reconcile` deliberately left untouched.** A `failed` instance stays `failed` across a brain restart — retry is a deliberate user action, not auto-remediation, matching `HEALTH.md`'s "the brain detects, blocks, and surfaces; it does not autonomously … roll back." The reconcile pass handles only `running`/`stopped` drift; adding a `failed` case would silently re-run whatever failed on every reboot.

**Frontend (`web-ui/`):**

- `components/AppTile.vue` — a `failed` tile now takes a **light amber/warning tint** (`bg-amber-50` + `border-amber-400`), distinct from the gray `opacity-50` stopped tile, and keeps the corner alert mark (failed is trouble). For a controller it becomes a **button that retries** (emits the same `start` event the stopped tile uses → no `HomeView` change needed) with a hover caption "Failed — click to retry" and a "Retrying…" caption while the job runs. A controller also gets a small **"View details" link** to `/settings/apps/<id>` so a persistent failure is diagnosed (the failure logs live there) rather than blindly re-clicked — this needed a wrapper root since a `<button>` can't contain the `<RouterLink>`. A non-controller sees the amber alert tile with neither affordance. `canStart`/`canRetry`/`canAct` split the gate; `showAlert`/`tag`/`onClick` key off `canAct`.
- `views/settings/InstalledAppDetailSection.vue` — the Start control now also shows for `failed` (`canControl && (stopped || failed)`), labeled "Retry"/"Retrying…" instead of "Start service"/"Starting…". The existing `start` mutation hits the same widened endpoint; the logs accordion right below is the failure reason, so the detail-page retry isn't blind.

## Verification

- **`internal/lifecycle` — new `TestRetryFromFailed`:** installs, drives the instance to `failed` (stop, then a start whose `main_service` never goes healthy), drops the fake Avahi entry group, then retries via `Start` and asserts: legal from `failed` (no `ErrNotStopped`), state → `running`, route → real upstream, exactly one fresh `Publish` (the #153 re-assert, so `<slug>.local` resolves again), and the name re-announced. The existing `TestStartGuardRejectsNonStopped` (running → 409) and `TestStartHealthFailureMarksFailed` still pass, covering the rejection and the `failed`-landing sides.
- **`internal/api`:** `TestStart409WhenRunning` exercises the widened guard's rejection branch. The acceptance branch is covered at the lifecycle layer (the API harness runs with `life=nil`, so a guard-passing start would reach the job goroutine; the file's established pattern keeps happy-path coverage at the lifecycle layer).
- **Gates:** `make check` green (gofmt + vet + OpenAPI-fresh + full Go suite). `web-ui` typechecks + builds clean on top of #170's `LiveResources.vue` fix (that pre-existing `vue-tsc` break at `LiveResources.vue:161` is unrelated to this change and not touched here).

## What's next

- **Boot/visual confirmation in a running stack.** The retry path is unit-covered against the fakes; a manual pass on a real box (install something that fails, click the amber tile, watch it recover with its `.local` name back) would confirm the splash transitions and the amber tile end-to-end.
- **`update_failed` has no tile retry yet.** Its recovery is the rollback path (`# update + rollback`), not a plain Start, so it's intentionally out of this change; a tile affordance for it can follow when update flows get their own UI pass.
- **Mid-life-restart durability for untouched apps** (the `uptime_s`-poll gap from #153) is still open in `DISCOVERY.md` # Restart durability — orthogonal to retry, which always re-publishes.
