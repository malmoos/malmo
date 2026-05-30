# 0003 — Door-2 custom apps + admission policy

- **Status:** done
- **Date:** 2026-05-22
- **Specs touched:** `APP_MANIFEST.md`, `APP_LIFECYCLE.md`

Adds the "paste any compose and run it" path, and the security gate it forced —
which now protects catalog apps too.

## What was done

### Admission policy (new `internal/admission`)

`admission.Check(ctx, composeBytes)` runs **for both doors** at install
(`APP_LIFECYCLE.md` # admission policy), writing no state on rejection:

- Validates syntax via `docker compose config -q` (catches malformed compose).
- Inspects the **raw** YAML (not the normalized output, which would absolutize
  relative bind paths) and rejects, naming the exact service + field:
  host `ports:`, `privileged: true`, `cap_add`, `build:`, `extends:`,
  `network_mode: host|none|container:*`, `pid/ipc/userns: host`, absolute-path
  bind mounts, and named volumes (bind-mounts-only rule from `APP_MANIFEST.md`).

Rejections are a typed `admission.Error` surfaced as HTTP 422.

### Shared install core (`internal/lifecycle`)

`Install` (Door-1) and `InstallCustom` (Door-2) both converge on an unexported
`install(ctx, man, composeBytes, progress)` — the transaction from
[walking-skeleton.md](walking-skeleton.md)/[reconcile-and-health-wait.md](reconcile-and-health-wait.md),
now with admission as its first step. Door-1 loads the pair from the catalog;
Door-2 synthesizes it. This is the spec's "one model, two doors" made literal.

### Synthetic manifest (`internal/manifest/synthesize.go`)

`Synthesize(name, compose, mainService, mainPort)` builds a manifest from a
pasted compose (`APP_MANIFEST.md` # Custom container — synthetic manifest):
slugified `id` + entropy, `version: custom`, `preferred_slugs: [slug(name)]`,
`internet: true` default. Infers `main_service` when the compose has exactly one
service; otherwise returns a clear error listing the services.

### API (`internal/api`)

`POST /api/v1/apps/custom` (job) with `{ name, compose, main_service?,
main_port }`. Synthesize + admission run **synchronously in the handler** so the
user gets an immediate, specific 422 instead of a failed job; the actual install
then runs as a job like Door-1.

### UI (`web-ui`)

An "Add custom app" form (name, port, compose textarea) hitting the new
endpoint, with the rejection message shown inline. Plain CSS; the shadcn
restyle is a later slice.

## How it maps to the specs

- `APP_MANIFEST.md` — synthetic manifest fields and defaults; one-model/two-doors.
- `APP_LIFECYCLE.md` — admission rejection list; both doors converge on one
  install transaction.

## Verified

- **Door-2 happy path:** pasted a single-service whoami compose → synthesized to
  slug `my-custom-app`, `version: custom`, `running`, and routes through Caddy.
- **Admission:** `ports:`, `privileged: true`, and named volumes each rejected
  with HTTP 422 and a field-naming message; ambiguous `main_service` (2 services)
  rejected with the service list. No instance dir or SQLite row created for any
  rejection.
- **Door-1 regression:** catalog `whoami` uninstall + reinstall still green
  through the new admission gate, and serves.
- UI build green (API path verified via curl; not clicked through a browser).

## Known gaps & deviations

- **No TOFU digest pinning** for Door-2 (`APP_LIFECYCLE.md` # image digest
  pinning) — images still run by tag, not pinned `sha256:` digest. Deliberately
  deferred; this is the next meaningful lifecycle hardening.
- **Named-volume rejection** is partly enforced by `docker compose config`
  (undeclared volumes) and partly by the structural check (declared ones); both
  reject, messages differ slightly.
- **No post-install manifest editing** — can't graduate a synthetic manifest
  (add managed services, refine storage) yet.
- **No multi-service `main_service` picker in the UI** — the API accepts
  `main_service`, but the form doesn't prompt for it yet.

## What's next

1. **TOFU digest pinning** — resolve + pin image digests on install; keep the
   previous digest for one-generation rollback.
2. **`WEB_UI.md` component stack** — Tailwind 4 + shadcn-vue; surface
   splash/failed states and the `main_service` picker.
3. **Post-install manifest editing** — graduate synthetic manifests.
4. **VM outer loop** — begin the real host-agent.
