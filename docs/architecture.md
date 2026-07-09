# Architecture (as built)

What exists in this repo today and how the pieces are wired. This doc is the
**current state** — design intent lives in [`specs/`](specs/), per-change history
lives in [`progress/`](progress/). When the code changes, this doc changes in
the same PR.

## Components

Five real processes/artifacts make up a running malmo right now. Three are Go,
one is JavaScript, one is a container we don't write.

| Component | Lives in | What it is | Status |
|---|---|---|---|
| **`malmo-brain`** | `cmd/brain/`, `internal/` | The control-plane daemon. Owns SQLite state, the REST+SSE API, the app lifecycle, and the Caddy config. One Go binary. | Real |
| **`host-agent` (fake)** | `cmd/host-agent/` | Privileged side used in the inner dev loop. Speaks the real `BRAIN_HOST_PROTOCOL.md` wire format over a UNIX socket; the host operations themselves (Avahi, LUKS, PAM, apt) are stubbed in memory. The file manager (`/v1/files/*`) is served in-process as the dev operator (no UID drop), so the Files destination works under `make dev`. | **Fake** (real wire, canned ops) |
| **`host-agent-real`** | `cmd/host-agent-real/`, `internal/hostagent/` | The real privileged binary. Seam-injected reporters: PAM password verify (`pamverifier`), `/proc` system sampling (`procsource`), disk usage, RAM pressure, journal streaming, service health, reboot-required flag, user manager, system time-zone setter (`timezone`, `timedatectl set-timezone` — the first-run wizard's Step 3, wired in both build profiles), and the in-dashboard file manager (`filemgr`, wired in both profiles — each `/v1/files/*` op runs in a child re-exec'd as the requesting user's UID/GID via `SysProcAttr.Credential`, sharing the pure `fileops` primitives with the fake, `FILES.md` # Execution). Discovery is real: per-LAN-interface Avahi announcements (`avahipublisher`) driven by the NetworkManager LAN set (`netstate`), with an avahi-daemon.conf allowlist sync and IP-change replay. Seeds the brain's Docker transport then launches the brain container on startup (`brainlaunch`: `EnsureTransport` creates `malmo-ingress` + runs the `docker-socket-proxy`; `Launch` docker-loads the bundled image if absent, lockstep `malmo.protocol.major` OCI-label check, `docker run --restart unless-stopped` on the ingress net with `DOCKER_HOST` at the proxy). Host ops not yet wired: LUKS/TPM, apt, NM configuration (WiFi setup, `/v1/network/*`). A build-tagged slim **`hosted`** profile (`go build -tags hosted`, #204/C1c) compiles the discovery/NetworkManager stack out for the cloud image — `avahipublisher`/`netstate` unwired, no-op publisher, nil `Net` — keeping the same PAM/user-mgmt/health-system/brain-launch seams (`cmd/host-agent-real/wiring_appliance.go` vs `wiring_hosted.go`). | Partial — see "What is not built yet" |
| **Caddy** | `dev/caddy.json`, `dev/docker-compose.yml` | Reverse proxy. Terminates `*.local` (appliance) or `*.<box-id>.malmo.network` over real Let's Encrypt HTTPS (hosted, via a custom acme-dns build) and routes to app containers + the brain. Configured live by the brain via Caddy's admin API. | Real (container) |
| **`web-ui`** | `web-ui/` | Vue 3 + Vite + TanStack Query dashboard. Talks only to the brain. Tailwind 4 landed; shadcn-vue scaffolding present, components not yet copied in. Internal code architecture: [`dev/web-ui.md`](dev/web-ui.md). | Real |
| **SQLite** | `$STATE_DIR/malmo.db` | The brain's only persistent store. Schema + queries in `internal/store/`. | Real |

Plus the **Docker daemon** on the host, which the brain drives with the
`docker compose` CLI (`internal/lifecycle/docker.go`). App containers run on the
`malmo-ingress` Docker network so Caddy can reach them by service name.

## How the wires connect

```
                           ┌─────────────────────┐
        browser ─────────► │      web-ui         │
                           │ (Vue, TanStack Q.)  │
                           └──────────┬──────────┘
                                      │ HTTP + SSE
                                      ▼
                           ┌─────────────────────┐    docker compose CLI
                           │     malmo-brain     │ ──────────────────────► Docker daemon
                           │                     │                                │
                           │  api / lifecycle    │ ─── Caddy admin API ──► Caddy ─┘
                           │  store / catalog    │                          │
                           │  auth / audit       │                          ▼
                           │  caddy / events     │                       app containers
                           └──────────┬──────────┘                       (malmo-ingress net)
                                      │ HTTP/JSON over UNIX socket
                                      ▼
                           ┌─────────────────────┐
                           │  host-agent (fake)  │
                           │  in-memory state    │
                           └─────────────────────┘
```

**Each arrow, in one line:**

- **browser → web-ui:** Vite dev server in dev; the brain serves the built
  bundle in prod (planned). The UI is plain SPA, no SSR.
- **web-ui → brain:** REST under `/v1/*` (OpenAPI generated by huma) for
  reads/mutations; SSE under `/v1/events` for install/lifecycle progress. Auth
  is an opaque cookie minted by `internal/auth`; the middleware in
  `internal/api` gates every mutation. See `specs/BRAIN_UI_PROTOCOL.md`.
- **brain → Docker:** the brain shells out to `docker compose` per
  instance. The compose file is held verbatim from the manifest — the brain
  never rewrites it. Driver interface lives in `internal/lifecycle/` (the
  consumer), implementation in the same package.
- **brain → Caddy:** the brain POSTs JSON to Caddy's admin API to add/remove
  site blocks per app. A splash route covers `<slug>.local` until the
  container's health check passes, then flips to the real upstream.
- **brain → host-agent:** HTTP/JSON over `MALMO_AGENT_SOCK`. Two patterns,
  sync request/response and SSE-streamed jobs (`internal/protocol/host.go`
  defines the types; `internal/hostclient/` is the brain-side client). Today
  the routes are `/v1/discovery/{publish,unpublish,state}`, `/v1/system/status`,
  and `/v1/auth/{verify-password,set-password,set-role,delete-user}`. See
  `specs/BRAIN_HOST_PROTOCOL.md`.

## Inside the brain

Packages under `internal/` and what each owns. Layer rules come from
[`../CLAUDE.md`](../CLAUDE.md) # Go code discipline; only the directional rules
are stated below.

| Package | Owns | Imported by |
|---|---|---|
| `api` | HTTP handlers (huma), auth middleware, request/response shapes. The only package that knows about HTTP. | `cmd/brain` |
| `lifecycle` | The install transaction: door-1 (catalog) and door-2 (paste-a-compose), digest pinning, reconcile pass, health-wait, Caddy timing, uninstall. Defines `DockerDriver` consumer-side. | `api`, `cmd/brain` |
| `store` | SQLite schema + queries. Sole persistence boundary. `ErrNotFound` is the only typed error. | `api`, `lifecycle`, `auth`, `audit`, `cmd/brain` |
| `catalog` | Door-1 source behind a fixed six-method facade. Production (every profile) uses the control-plane thin client (`NewRemote`, `MALMO_CATALOG_URL`): fetches `GET /catalog/sync`, integrity-digest-verifies, last-good on-disk cache, proxies+caches assets. The disk reader (`New`) is retained only as a test constructor; no catalog is baked into the image. | `lifecycle`, `api`, `cmd/brain` |
| `manifest` | `manifest.yml` schema (parse + validate), and the synthesizer that wraps a pasted compose into a door-2 manifest. | `catalog`, `lifecycle`, `api` |
| `admission` | The single compose admission policy applied to both doors (image pinning rules, forbidden constructs, etc.). | `lifecycle` |
| `caddy` | Client for Caddy's admin API. Site-block JSON generation lives here, plus the hosted wildcard-TLS automation policy (`EnsureWildcardTLS`: ACME DNS-01 via the `acmedns` provider for `*.<box-id>.malmo.network`). | `lifecycle`, `cmd/brain` |
| `profile` | The environment-profile marker (`appliance`\|`hosted`) + the first-boot seed reader, and the hosted URL-shape helpers (`HostedAppHost`/`HostedAppURL`/`HostedDashboardHost`/`CertSubjects` — the single place `<slug>.<box-id>.malmo.network` is named). Leaf package. | `api`, `lifecycle`, `cmd/brain` |
| `hostclient` | Brain-side client for `host-agent`. Mirrors the routes in `protocol`. | `lifecycle`, `api`, `auth`, `cmd/brain` |
| `protocol` | Wire types shared with `cmd/host-agent`. Source of truth for the host protocol. | `hostclient`, `cmd/host-agent` |
| `auth` | First-admin bootstrap, password verification (delegates to host-agent), opaque cookie sessions. No password hashes on the brain side. | `api`, `cmd/brain` |
| `assertion` | Verifies the portal's short-lived Ed25519 ownership assertion for the hosted portal-to-box SSO handshake (`Verify`: signature + expiry; box-id/issuer/replay are the handler's policy). Minimal signed token, not a JWT. Mirrors the cloud signer's wire format. Leaf package. | `api` |
| `audit` | Append-only `audit_events` table writes. Every elevation-class mutation calls `audit.Record` on success **and** failure. | `api` |
| `events` | In-memory pub-sub bus for SSE. Lifecycle stages publish; the SSE handler subscribes. | `lifecycle`, `api`, `cmd/brain` |

**Cross-cutting invariants:**

- **Brain commits first, host is reconstructible.** Mutations that span SQLite
  + host-agent commit to the brain first, then call the host. On host failure,
  the brain row is rolled back. Established by `/setup`, `createUser`,
  `updateUserRole`, `deleteUser`. See [`../CLAUDE.md`](../CLAUDE.md).
- **Single logger.** `slog.Default()` everywhere; no `*slog.Logger` threading,
  no `log` package, no `fmt.Println` for diagnostics. Standard field names are
  listed in CLAUDE.md.
- **Audit on success and failure.** Elevation-class handlers emit
  `audit.Record(..., success=false)` on every observable failure path (host
  502, store 500, conflict 409, guard rejection), mirroring `login.failure`.

## On-disk layout (dev)

```
.dev/
  agent.sock          UNIX socket the brain dials the fake host-agent on
  state/
    malmo.db          brain's SQLite (schema in internal/store)
    instances/        per-app state (compose file, .env, digests)
  host-agent          built binary
  brain               built binary
catalog/
  whoami/             sample manifest (door-1 source)
dev/
  caddy.json          Caddy bootstrap config (replaced live via admin API)
  docker-compose.yml  brings up the dev Caddy + malmo-ingress network
```

`MALMO_STATE_DIR` and `MALMO_AGENT_SOCK` are set by the Makefile so the brain
and host-agent agree on paths.

## Dev orchestration

`make dev` runs the four foreground processes — Caddy (container), host-agent,
brain, Vite — in one terminal. `make help` lists the per-process targets for
the four-terminal layout. See [`dev/running-locally.md`](dev/running-locally.md)
for the full inner loop. The VM-based outer loop for host-integrated parts
(boot, LUKS, systemd) is not wired here yet.

## What is **not** built yet

So this doc isn't read as a claim about the finished product:

- **Full real host-agent.** `cmd/host-agent-real` is partially real: PAM password verify, `/proc` system sampling, disk usage, RAM pressure, journal streaming, service health, reboot-required, discovery (per-LAN-interface Avahi announcements from the NetworkManager LAN set, allowlist sync, IP-change replay), and the first-boot brain launch (#164: load-if-absent, lockstep label check, `docker run --restart unless-stopped`) are wired. LUKS/TPM, apt, and the NM configuration surface (WiFi setup, `/v1/network/*`) are not yet wired — those ops are still no-ops or stubs.
- **Control-plane stack bring-up — built (M1b, #165), VM-boot acceptance pending.** host-agent seeds the brain's Docker transport (the `malmo-ingress` network + the `docker-socket-proxy`, raw socket `:ro`, `EXEC` denied) before launching the brain, and points it at `DOCKER_HOST=tcp://docker-proxy:2375`; the brain then reconciles Caddy + `malmo-ui` from the staged control-plane compose (`lifecycle.EnsureControlPlane`) and installs the dashboard route (`/api/v1/* → brain`, else → `malmo-ui`). All of it is **production-gated** on `MALMO_CONTROL_PLANE_DIR`/`MALMO_DASHBOARD_UI_UPSTREAM`, so the natively-run dev brain is unchanged (standalone dev Caddy, Vite UI, raw socket). Managed-DB-in-production stays gated on a provisioning re-architecture off `docker exec` (the proxy denies `EXEC` — `DECISIONS.md` 2026-06-14). Unit-tested; a real VM boot pass (`sudo make test-medium-qemu`) is still outstanding.
- **Storage subsystem.** No `/srv/malmo`, no mergerfs, no LUKS-unlock flow,
  no `malmo-storage-ready.target`. Apps write to wherever Docker puts volumes.
- **Boot, install ISO, updates.** The `mkosi` image build (`BUILD.md` # 2;
  proven in the test lane, not yet the production ISO), the release manifest,
  and the five update streams are all spec-only.
- **Health / notifications / telemetry / time / discovery beyond stubs.** The
  brain doesn't surface health issues, the bell doesn't exist, no telemetry
  client, no chrony integration.
- **Login UI.** `Setup` and `Dashboard` render; `Login.vue` is kept in the tree
  but not routed (single-user dev phase). Cookie sessions and the underlying
  auth pipeline are real.
- **App store.** Every box syncs the catalog from the control plane (`GET /catalog/sync`) with integrity-digest verification, a last-good cache, and TLS for authenticity; no catalog is baked into the image and there is no Ed25519 signature (`DECISIONS.md` 2026-07-02). What remains is cloud-side: the store is the authoring surface, and reconciling the door-1 app-authoring how-to (`docs/dev/authoring-apps-with-an-agent.md`) with that.

For where each of these is planned, see the matching `specs/` doc.

## Reading order for a new contributor

1. This file.
2. [`specs/SPEC.md`](specs/SPEC.md) and [`specs/CONTROL_PLANE.md`](specs/CONTROL_PLANE.md)
   — the design vocabulary the code uses.
3. [`dev/running-locally.md`](dev/running-locally.md) — get the stack up.
4. [`progress/walking-skeleton.md`](progress/walking-skeleton.md)
   through the latest entry — the order things were built, with the why.
5. `cmd/brain/main.go` — 100 lines, names every package and how they wire.
