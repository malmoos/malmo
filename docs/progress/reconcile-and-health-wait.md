# 0002 — Startup reconcile + health-wait & splash flip

- **Status:** done
- **Date:** 2026-05-22
- **Specs touched:** `APP_LIFECYCLE.md`, `BRAIN_HOST_PROTOCOL.md`

Hardens the install lifecycle from [walking-skeleton.md](walking-skeleton.md) so it
survives restarts and reflects real readiness. Two related changes.

## What was done

### 1. Startup reconcile pass (`APP_LIFECYCLE.md` # reconciliation is imperative)

`Manager.Reconcile(ctx)` runs at brain startup (after `EnsureIngress`) and
converges Docker + routing to the SQLite desired state:

- **`running` but no containers** → `docker compose up -d`.
- **`stopped` but containers up** → `docker compose stop`.
- **Orphan containers** (carry `molma.managed=true` but no SQLite row) → torn
  down (compose down if the dir survived, else `docker rm` by label + drop the
  per-app network).
- For every `running` instance it **re-asserts the Caddy route + mDNS**.

This fixes "a brain restart loses routes": `EnsureServer` now resets the Caddy
route list to empty at startup (PATCH `…/routes` → `[]`), and the reconcile pass
rebuilds it from desired state. The override now stamps every container with
`molma.managed` / `molma.instance_id` / `molma.manifest_id` labels so the
reconciler can find and map managed containers.

**Verified:** wiping the live Caddy route while the brain was down, then
restarting → reconcile re-added the route and the app served HTTP 200; stopping
a container behind the brain's back, then restarting → reconcile logged
`should be running but has no containers — starting` and brought it back.

### 2. Health-wait + splash route flip (`APP_LIFECYCLE.md` install transaction, steps 8–11)

The install transaction now:

- **Step 8** — registers the Caddy route immediately, pointing at a
  molma-served **splash page** (`static_response`, auto-refresh) so the hostname
  never returns connection-refused. Three states: `starting`, `stopped`,
  `failed`.
- **Step 10** — `waitHealthy` polls the `main_service` container until it's
  `Running` and (if the image declares a healthcheck) `healthy`, with a 120s
  default timeout. Containers with no healthcheck are ready as soon as they run.
- **Step 11** — flips the Caddy upstream from the splash to the real container.
- **Failure** (timeout / `unhealthy`) does **not** roll back: the instance dir
  is kept, the route flips to a `failed` splash, and state goes to `failed`
  (matching the spec's steps 10–11 failure handling, distinct from the 3–9
  full-rollback region).

Caddy route ops are now idempotent upserts (remove-then-add by `@id`), so the
splash→real flip can't produce duplicate routes.

**Verified:** a deliberately slow app served the "starting" splash, then either
flipped to the real upstream (happy path, shown by the `whoami` reinstall ending
as a `reverse_proxy` serving the app) or, when it never became healthy, landed
in `failed` with the failed splash. (The slow test fixtures failed to become
healthy because the isolation override `cap_drop: [ALL]` correctly blocks
`nginx`/`python` from binding privileged ports / chowning — a fixture quirk, not
a brain bug.)

## Known gaps & deviations

- **Reconcile handles only `running`/`stopped`.** Interrupted
  `installing`/`uninstalling` states (crash mid-transaction) aren't swept yet;
  the dangerous-op-aware drift policy (`APP_LIFECYCLE.md` drift table) is a
  follow-up.
- **No 60s heartbeat reconcile yet** — only the startup pass. The heartbeat +
  post-op verify triggers from `BRAIN_HOST_PROTOCOL.md` # reconciler are not
  wired.
- **No stop/start API endpoints** — the `stopped` splash state exists but
  nothing drives an instance into it via the UI yet.
- **Production Caddy server creation** — `EnsureServer` assumes the `molma`
  server already exists (dev `caddy.json`); creating it when Caddy is
  brain-managed is deferred.
- **Health-wait reads Docker via the CLI** (`docker ps`/`inspect`); the spec's
  `/events` stream for push-based health is still a follow-up.

## What's next

Picks up the [walking-skeleton.md](walking-skeleton.md) list, now that 1–2 are done:

1. ~~**Door-2 custom-compose path + admission policy**~~ — done in [door-2-and-admission.md](door-2-and-admission.md).
2. **`WEB_UI.md` component stack** — Tailwind 4 + shadcn-vue, `useJob()`
   composable, splash/failed state surfaced in the dashboard.
3. **60s heartbeat reconcile + post-op verify** — extend reconcile beyond the
   startup pass.
4. **Stop/start endpoints** — drive the `stopped` lifecycle state from the API.
5. **VM outer loop** (QEMU + swtpm) — begin the real host-agent.
