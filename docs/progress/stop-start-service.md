# Stop / Start a service + per-app detail page

- **Status:** done
- **Date:** 2026-06-10
- **Specs touched:** `APP_LIFECYCLE.md`, `DASHBOARD.md`, `DECISIONS.md`

Lets a user stop an installed app they don't currently need (freeing its CPU/RAM) and start it again later. Until now an installed instance was always running; the only off-switch was uninstall, which deletes the app. This slice adds the stop/start half of the `running ⇄ stopped` state machine that `APP_LIFECYCLE.md` already locked, wires it through the API, and gives it two UI surfaces: a per-app management page in Settings, and click-to-start straight from a grayed home tile.

## What was done

### Backend — `internal/lifecycle`

- **`Manager.Stop`** — `docker compose stop` (never `down`, so containers/network/route/mDNS all survive), flips the Caddy route to the "stopped" splash, sets state `stopped`. Guarded to `running`; returns `ErrNotRunning` otherwise.
- **`Manager.Start`** — `docker compose up -d` (the same op the reconcile pass uses — see `DECISIONS.md` 2026-06-10, **not** `compose start`), bounded by the health-wait budget; flips the route to the "starting" splash, waits for `main_service` healthy, then flips to the real upstream. State is written `running` **before** the docker op (brain-commits-first), so a crash mid-start is finished by the reconcile pass exactly as a reboot is. A start that comes up but never goes healthy lands in `failed` with the "failed" splash, mirroring an install health-timeout. Guarded to `stopped`; returns `ErrNotStopped` otherwise.
- **Per-instance lock** — `instLocks` map + `lockInstance(id)`; `Stop`, `Start`, and `Uninstall` take it so a stop can't race an uninstall (or each other). Implements the "one lifecycle op at a time per instance" the spec locked but the code hadn't enforced. Install allocates a fresh id, so it has nothing to contend with and skips the lock.
- `ErrNotRunning` / `ErrNotStopped` sentinels are the only conflict discriminators lifecycle exposes; the API maps them to 409.

### API — `internal/api`

- `POST /api/v1/apps/{id}/stop` and `POST /api/v1/apps/{id}/start`, both job-based (Pattern B) like uninstall.
- `authorizeAppMutation` shared gate: 404-leak-guard via `canSee`, then **household = admin only, personal = owner or admin** (mirrors uninstall). Illegal transitions are a synchronous **409** before the job runs, so the UI gets a clean error instead of a failed job to poll.
- Regenerated `api/openapi.{json,yaml}` + `web-ui/src/generated/openapi.ts`.

### Web UI — `web-ui`

- **`InstalledAppDetailSection.vue`** (new) at `/settings/apps/:id`, rendered inside the Settings shell: header (logo + name + description, with logo/description best-effort from `GET /catalog/{manifest_id}` — Door-2 custom apps fall back to the glyph), an action row (Open / **Stop service**·**Start service** / **Uninstall** with an inline two-step confirm), and the app's **Logs** at the bottom. Control + logs gated to admins / the personal owner.
- **`InstalledAppsSection.vue`** is now a list of links to the detail page; its old inline Logs/Uninstall buttons moved onto the detail page.
- **`AppTile.vue`** — a deliberately *stopped* tile stays grayed but loses the corner alert mark (that's reserved for failed/crashed), becomes clickable for a viewer who may control it, shows a "Service stopped - click to start again" hover caption, and a persistent "Starting up…" caption while the start job runs. `HomeView.vue` owns the start mutation + the per-id `starting` set; tiles stay presentational.

### Tests

- `lifecycle_stopstart_test.go` — stop→start round-trip (state + route variant + driver calls), both transition guards, and the start-health-failure → `failed` path.
- `stopstart_test.go` (api) — the synchronous rejection matrix: member-vs-household 403, unknown 404, member-other-personal 404 leak guard, and both 409 guards. (The happy path runs the job goroutine against `life`, which the api harness builds as nil, so it's covered at the lifecycle layer.)

## What's next / known gaps

- **Crash detection vs. stopped.** The Docker `/events` crash-detection subscriber (`APP_LIFECYCLE.md` # crash detection) is still unbuilt; when it lands it must read SQLite state and suppress the "unhealthy" badge for a `stopped` instance, or a deliberate stop will read as a crash.
- **Stop doesn't reclaim the shared service.** A stopped app backed by a Tier-1 managed Postgres/MySQL leaves that shared service running (it's brain-owned, not part of the app's compose project). Grace-shutdown of an idle managed service stays deferred (`NEXT.md`).
- **Uninstall confirm is bare.** The detail page's uninstall is a two-step inline confirm; the spec's "keep data" checkbox (`APP_LIFECYCLE.md` # uninstall) is not yet wired — uninstall still always deletes data, same as before this slice.
