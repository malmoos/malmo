# Health / degraded-mode banners (dashboard)

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** `WEB_UI.md` # Health & degraded mode surfacing (reconciled — active issues live in the Query cache, not a Pinia store; banner click goes to the Home inline list); `HEALTH.md` # Knock-ons (same Pinia→Query reconciliation); `BRAIN_UI_PROTOCOL.md` (the active-issues read is `GET /api/v1/health`, now in the OpenAPI spec). Realizes `HEALTH.md` # Display / # Clear and the SSE kinds already named in `BRAIN_UI_PROTOCOL.md` Pattern C.

Closes issue #12. The brain already detected, persisted, and exposed health issues via `GET /api/v1/health`; the dashboard was blind to them. This adds the Synology-style degraded-mode surface — a global banner for active error/critical issues, live SSE updates, an inline active-issues list on Home, toast-on-clear, and `blocks_*`-gated affordances — plus the small backend seam that was missing: SSE events on issue transitions.

## What was done

### Backend seam (the missing half)

- `internal/events/events.go` — two new event kinds: `HealthIssueRaised = "health.issue_raised"` and `HealthIssueCleared = "health.issue_cleared"`. The underscore form is the **SSE event name**, deliberately distinct from the dotted **audit action** `health.issue.raised` (`LOGGING.md`).
- `cmd/brain/main.go` — threaded `*events.Bus` into `emitHealthTransitions` (the one function every transition path already funnels through) and published one advisory event per raised/cleared key, payload `{id, instance_key}`. The bus reaches it via its five callers — `pullSystemHealth`, `checkAgentVersion`, `checkBrainDBIntegrity`, `restartLoopDetector.check`, `appProbeDetector.check` — so **every** transition path publishes, not just the four the issue named: `brain-db-corrupt` shares the choke point and gets live updates for free (the registry and the bus are both in-memory, so it works even in minimal-functionality mode). The publish is **nil-guarded** so detector struct-literal tests and audit-only unit tests pass a nil bus and stay behavior-identical. New unit test `TestEmitHealthTransitions_PublishesToBus` pins the seam.
- `internal/api/api.go` — `OpenAPIDocument()` now builds the spec-emission Server with a **store-less health manager** (`health.NewManager(nil)`, never invoked — only reflected) so the health route's registration guard (`if s.health == nil`) no longer skips it. Result: `GET /api/v1/health` and its `Issue` schema land in the committed `api/openapi.{json,yaml}`, and the dashboard's wire type is **generated** rather than hand-written — health is huma-native, unlike the Door-2 endpoints that genuinely bypass huma.

### Frontend

- `web-ui/src/api.ts` — `export type HealthIssue = Schemas["Issue"]` (generated; `severity`/`category` are free strings as elsewhere).
- `web-ui/src/useHealth.ts` (new) — wraps `useQuery(['health/issues'])` calling `GET /api/v1/health`; derives `activeIssues`, `blockingIssues` (error/critical), `blocksApps` / `blocksWrites` / `blocksUsers`. Gated on `isAdmin` (the endpoint is admin-only). Every caller shares the one cached query — that shared cache *is* the "store."
- `web-ui/src/useEvents.ts` — listeners for `health.issue_raised` / `health.issue_cleared` invalidate `['health/issues']`, same advisory-refetch pattern as notifications.
- `web-ui/src/components/HealthBanner.vue` (new) — global bar in `AppShell` chrome (above `<main>`), shown when any error/critical issue is active: severity pill + the most-severe issue's summary + a "+N more" count, clickable to the Home issues list. Owns the **toast-on-clear**: a watcher diffs the active set and fires a success toast (`Resolved: <summary>`) for each issue that disappears, priming silently on first load so already-active issues don't toast.
- `web-ui/src/components/HealthGated.vue` (new) — `:blocks="'apps'|'writes'|'users'"` wrapper; when the gate is set it renders the slot non-interactive (pointer-events off, dimmed) inside a `title` tooltip ("Disabled because: <summary>"), else renders the slot untouched. Wired onto both **Install** affordances in `StoreView.vue` (`blocks="apps"`).
- `web-ui/src/views/HomeView.vue` — an `#health-issues` "System health" section at the top listing every active issue (severity dot + summary + details + id/instance_key + relative raised-time), including warnings the global banner doesn't surface. The banner's click target.
- `web-ui/src/toasts.ts` + `ToastHost.vue` — added a `success` variant (green) alongside `error`; `pushSuccessToast()` backs the toast-on-clear. The channel's comment had already anticipated this.
- `web-ui/src/router.ts` — `scrollBehavior` scrolls to a `#hash` target when present (the banner links to `#health-issues`); non-hash navigations keep their existing behavior.

## How it maps to the specs

- `HEALTH.md` # Display — global banner for critical/error; inline list; disabled affordances with explanatory tooltips. # Clear — toast-on-clear ("no silent auto-recovery"). The brain consults `blocks_*` itself; the UI mirrors the gate for UX, it is not the boundary (a blocked install still 409s server-side).
- `BRAIN_UI_PROTOCOL.md` Pattern C — consumes the global SSE stream; `health.issue_*` payloads are advisory ({id, instance_key}) and trigger a re-read of `GET /api/v1/health`.
- `WEB_UI.md` # State — **server state lives in Query, not Pinia.** See the reconciliation below.
- `DECISIONS.md` 2026-05-16 — Synology-style graceful degradation: banner, not modal.

## Known gaps & deviations

- **Pinia → Query (reconciliation, flagged).** The issue text and `WEB_UI.md` line 60 said "active issues live in a Pinia store," but `WEB_UI.md`'s own locked rule (*server state lives in Query, not Pinia*) plus the codebase (zero Pinia usage; `useNotifications` is the prior art) say otherwise. Health issues are server state, so they live in the TanStack Query cache via `useHealth()` — the shared cache gives the "components don't each subscribe" property a store would, without a second source of truth. Reconciled `WEB_UI.md` and `HEALTH.md`'s knock-on note to match the locked rule. No `DECISIONS.md` entry: nothing flipped — the locked rule was always Query; only the contradictory "Pinia store" phrasing was corrected.
- **Banner is admin-only (issue out-of-scope).** `GET /api/v1/health` is admin-only (`internal/api/health.go`), so `useHealth` is gated on `isAdmin` and members never see the banner. `HEALTH.md` # Knock-ons wants members to see box-wide criticals (transparency); that waits on a member-facing health read, deferred by the issue.
- **No remediation action buttons.** The `actions` array isn't on the `Issue` struct and the `POST /api/v1/health/:id/:act` endpoints don't exist yet; the banner/list render `summary` as the hint. Action buttons are a follow-up once those land.
- **No per-page warning inline cards.** The global banner covers error/critical; warnings surface only in the Home list, not yet as inline cards on Storage/Updates/per-app pages (issue out-of-scope).
- **No 409 `blocked-by-health-issue` toast.** The "your action was refused → View the banner" toast (`WEB_UI.md`) needs the `ApiError` handler to inspect `code`; deferred.
- **`api.getHealthIssues()` not added.** The issue suggested a named method on the `api` client, but the codebase convention (`useNotifications`) is to inline `api.get<...>()` in the composable; followed that — only the `HealthIssue` type was added to `api.ts`.
- **`HealthGated` tooltip is the native `title`.** Not a reka-ui Tooltip primitive — simplest dependency-free path; promote if richer tooltips are wanted.
- **`relativeTime` duplicated** a third time (Home, after `NotificationBell` and `ActivityView`). Extraction to a shared util is now justified by the rule-of-three — left as a tidy-up so this issue stays scoped.

## What's next

- **Member-facing health read** so members see box-wide critical banners (widen `GET /api/v1/health` or add a scoped variant), then drop `useHealth`'s `isAdmin` gate.
- **Remediation actions:** `actions` on `Issue` + `POST /api/v1/health/:id/:act`, rendered as the banner/list's primary buttons (every banner has a primary action — `HEALTH.md`).
- **Per-page warning inline cards** (Storage, Updates, per-app) for `warning`-severity issues.
- **409 `blocked-by-health-issue` → toast with "View"** linking back to the banner.
- **Extract `relativeTime`** into a shared util now that three consumers exist.
