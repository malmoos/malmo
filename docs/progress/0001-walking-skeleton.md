# 0001 — Walking skeleton: install an app end-to-end

- **Status:** done
- **Date:** 2026-05-22
- **Specs touched:** `CONTROL_PLANE.md`, `APP_LIFECYCLE.md`, `APP_MANIFEST.md`, `BRAIN_UI_PROTOCOL.md`, `BRAIN_HOST_PROTOCOL.md`, `WEB_UI.md`, `APP_STORE.md`

The first vertical slice of malmo. Goal: prove the architecture spine
end-to-end with the fastest possible dev loop — everything runs natively on a
dev box, no VM. The thread exercised:

> UI → brain `POST /api/v1/apps` (job) → generate `compose.override.yml` + `.env`
> → `docker compose up -d` → fake host-agent `publish` (mDNS) → register Caddy
> route → SSE `app.installed` → UI updates → uninstall tears it all down.

## What was done

### Backend — `malmo-brain` (Go)

A single Go binary (`cmd/brain`) with clean internal packages, matching the
"one binary, internal modularity" decision in `CONTROL_PLANE.md`:

- `internal/api` — HTTP API via [huma](https://huma.rocks) (OpenAPI emitted as
  a byproduct, per `BRAIN_UI_PROTOCOL.md`) plus the raw SSE event stream.
  Endpoints: `GET /api/v1/catalog`, `GET/POST /api/v1/apps`,
  `GET /api/v1/apps/{id}`, `DELETE /api/v1/apps/{id}`, `GET /api/v1/jobs/{id}`,
  `GET /api/v1/events`. Install/uninstall are **jobs** (Pattern B); the rest are
  sync (Pattern A).
- `internal/lifecycle` — the install/uninstall transaction from
  `APP_LIFECYCLE.md`: slug allocation (with reserved-name list), instance dir
  tree, generated `compose.override.yml` + `.env`, per-app network creation,
  `docker compose up -d` via the **CLI driver**, mDNS publish, Caddy route
  registration, and full rollback on failure.
- `internal/store` — SQLite as the desired-state source of truth (instances
  table).
- `internal/catalog` — loads hand-curated manifests from `catalog/`.
- `internal/manifest` — parses + validates `manifest.yml` (required fields).
- `internal/caddy` — drives the Caddy admin API (no Caddyfile on disk).
- `internal/hostclient` — brain's client for the host-agent UNIX socket.
- `internal/events` — the global SSE event bus (typed `Kind` enum).
- `internal/protocol` — wire types shared by brain and host-agent.

The generated override is faithful to `APP_LIFECYCLE.md` # override file
contents: every service gets `cap_drop: [ALL]`, `security_opt:
[no-new-privileges:true]`, forced `restart: unless-stopped`, and attachment to
the per-app network; `main_service` additionally joins `malmo-ingress` with a
per-instance alias (`malmo-<id>-<service>`) so Caddy reaches exactly that
instance.

### host-agent — **fake** (Go)

`cmd/host-agent` speaks the *real* `BRAIN_HOST_PROTOCOL.md` wire format
(HTTP/JSON over a real UNIX socket) but its host operations are canned:
`/v1/discovery/publish|unpublish`, `/v1/discovery/state`, `/v1/system/status`.
This is the seam that lets the brain be built before the real host-agent
(Avahi/DBus/cryptsetup) exists.

### Frontend — dashboard (Vue 3 + Vite)

`web-ui/` per the `WEB_UI.md` stack: Vue 3 `<script setup>`, TanStack Query for
all server state, a thin `fetch` wrapper shaped for future `openapi-fetch`
codegen, and `useEvents()` subscribing once to the SSE stream to invalidate
Query caches (the push/pull-share-one-cache pattern). Lists the catalog and
installed apps; Install/Uninstall buttons drive jobs and poll to completion.

### Dev orchestration

`catalog/whoami` (a ~5 MB `traefik/whoami` image) as the smoke-test app.
`dev/` holds a standalone Caddy container + config; the `Makefile` wires the
all-native inner loop (`make help`). See
[`../dev/running-locally.md`](../dev/running-locally.md).

## How it maps to the specs

- **`CONTROL_PLANE.md`** — brain as one Go binary + SQLite; Caddy driven via its
  admin API; layered packages.
- **`APP_LIFECYCLE.md`** — compose-project unit, on-disk layout, CLI driver,
  install transaction with rollback, override contents, slug allocation, mDNS
  ownership by host-agent, register-Caddy-route timing.
- **`BRAIN_UI_PROTOCOL.md`** — `/api/v1` prefix, sync vs. job patterns, SSE
  global event stream with typed kinds, OpenAPI from huma.
- **`BRAIN_HOST_PROTOCOL.md`** — HTTP/JSON over UNIX socket, discovery +
  system-status endpoints, idempotent publish/unpublish.
- **`APP_MANIFEST.md`** — required-field manifest, compose held verbatim.

## Known gaps & deviations

- **host-agent ops are faked** — no real Avahi/LUKS/apt/NetworkManager. The
  protocol shape is real; the effects are not.
- **No auth.** `AUTH.md` is unbuilt; the brain uses a permissive dev CORS shim
  so Vite (`:5173`) can call it. No session cookie, no login.
- **Dev Caddy on host port `:8088`**, not `:80` — port 80 was occupied on the
  build box. Production Caddy publishes 80/443.
- **No startup reconcile of Caddy routes.** On brain restart, `EnsureServer`
  resets the route list; installed apps' routes are not re-asserted yet
  (`APP_LIFECYCLE.md` reconcile pass is the fix).
- **No health-wait / splash flip.** Install marks `running` after `compose up`
  without waiting for `main_service` healthy or doing the splash→real upstream
  flip (steps 10–11 of the install transaction).
- **No digest pinning, no admission policy, no Door-2 custom-compose path** yet.
- **No Tailwind/shadcn-vue.** Plain CSS placeholder; the `WEB_UI.md` component
  stack is deferred.
- **`.local` URLs are illustrative** — the dashboard shows
  `http://<slug>.malmo.local`, which won't resolve until the real host-agent +
  Avahi land. Real routing today is via Caddy on `:8088` with a `Host` header.
- **Go installed user-local** at `~/.local/go` (no system package).

## What's next

Ordered, roughly by leverage:

1. **Startup reconcile pass** (`APP_LIFECYCLE.md`) — on brain start, walk SQLite,
   re-assert Caddy routes + mDNS, fix container drift. Removes the
   "restart loses routes" gap.
2. **Health-wait + splash route flip** — register a splash route on install,
   wait for `main_service` healthy, flip to the real upstream; surface
   `failed` with logs.
3. **Door-2 custom-compose path + admission policy** — `docker compose config`
   rejection rules, synthetic manifest generation.
4. **`WEB_UI.md` component stack** — Tailwind 4 + shadcn-vue, real layout,
   `useJob()` composable, health/degraded-mode surfacing.
5. **VM outer loop** (QEMU + swtpm, per `TESTING.md`) — start building the
   *real* host-agent: boot ordering, storage assembly, LUKS/TPM unlock,
   NetworkManager, Avahi.
