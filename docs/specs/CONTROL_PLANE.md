# malmo Control Plane

> Working spec for malmo's control plane — the brain that orchestrates apps, routing, identity, storage, and the mesh. Companion to `SPEC.md`.
>
> **Topical specs that used to live here:**
> - App lifecycle (install, run, update, uninstall, reconciler, slug allocation, Caddy timing) → **`APP_LIFECYCLE.md`**
> - Brain ↔ UI API protocol (sync, jobs, SSE, errors, auth, versioning) → **`BRAIN_UI_PROTOCOL.md`**
> - Brain ↔ host-agent protocol → **`BRAIN_HOST_PROTOCOL.md`**
> - Web UI codebase, stack, deploy model → **`WEB_UI.md`**
>
> This doc stays at the architectural-overview level: what the control plane is, its layered shape, and the brain's deployment-time decisions.

## What the control plane is responsible for

- **App lifecycle** — install, start, stop, update, uninstall. Talks to Docker. Full spec in `APP_LIFECYCLE.md`.
- **Routing** — dynamically configures the reverse proxy as apps come and go (`photos.local` → container).
- **mDNS publishing** — registers each app's hostname with Avahi (via `host-agent`).
- **API + UI** — HTTP + SSE + (future) WebSocket API for the web UI; ships the UI. Wire spec in `BRAIN_UI_PROTOCOL.md`; client-side stack in `WEB_UI.md`.
- **Identity** — user accounts, sessions, admin/root account, sharing/ACLs. See `AUTH.md`.
- **Managed services** — runs shared Postgres / Redis / etc., provisions databases on app install, handles backups + upgrades. See `SERVICE_PROVISIONING.md`.
- **Storage** — manages the data drive (mergerfs union, ext4+LUKS+TPM unlock), bind mounts for `/home/` and `/var/lib/malmo/`, app data directories. See `STORAGE.md`.
- **Mesh** — integrates with Headscale, registers the box, manages pairing tokens, surfaces people/devices UI. See `MALMO_NETWORK.md`.
- **Backups** — orchestrates app backup hooks, schedules, restores.
- **Auto-updates** — apps, the OS itself, and the control plane. See `UPDATES.md`.

## Layered architecture

```
┌─────────────────────────────────────────────────────────┐
│                       Host (Debian)                     │
│                                                         │
│   ┌─────────────────┐                                   │
│   │ host-agent      │ ← tiny native binary, systemd     │
│   │ (Go, static)    │   • mounts disks / mergerfs pool  │
│   └────────┬────────┘   • bootstraps the brain          │
│            │            • OS-level updates              │
│            ▼                                            │
│   ┌──────────────────────────────────────────────┐      │
│   │           Docker (containerd under)          │      │
│   │                                              │      │
│   │  ┌──────────────┐  ┌──────────────────────┐  │      │
│   │  │ malmo-brain  │  │ Caddy (reverse proxy)│  │      │
│   │  │ (Go daemon)  │◄─┤ config via admin API │  │      │
│   │  │  + SQLite    │  └──────────────────────┘  │      │
│   │  │              │  ┌──────────────────────┐  │      │
│   │  │              │  │  managed Postgres    │  │      │
│   │  │              │◄─┤  (per major version) │  │      │
│   │  │              │  └──────────────────────┘  │      │
│   │  │              │  ┌──────────────────────┐  │      │
│   │  │              │  │  managed Redis       │  │      │
│   │  │              │◄─┤                      │  │      │
│   │  │              │  └──────────────────────┘  │      │
│   │  │              │  ┌──────────────────────┐  │      │
│   │  │              │  │  user apps           │  │      │
│   │  │              │◄─┤  (Photos, Grocery…)  │  │      │
│   │  │              │  └──────────────────────┘  │      │
│   │  └──────┬───────┘                            │      │
│   │         │ Docker API (via socket proxy)      │      │
│   └─────────┼────────────────────────────────────┘      │
│             ▼                                           │
│   ┌─────────────┐  ┌─────────────┐                      │
│   │   Avahi     │  │  Headscale  │                      │
│   │ (mDNS host) │  │   client    │                      │
│   └─────────────┘  └─────────────┘                      │
└─────────────────────────────────────────────────────────┘
```

### Layer 1 — `host-agent` (native, tiny)

A small native Go binary running as a systemd service. **Does as little as possible.**

- Bootstrap the system at boot — unlock and mount the data drive(s), assemble the mergerfs union, check disks, start Docker.
- Pull and start the `malmo-brain`.
- Apply OS-level updates (today: `apt`; tomorrow: A/B image swaps).
- Recover the brain if it crashes.

When we eventually move to A/B immutable updates, this is the only piece that's *part of* the immutable OS image. It's small, changes rarely. **This positioning makes the future immutable migration painless.**

For v1, host-agent could be a few hundred lines of Go. Deliberately boring.

The brain ↔ host-agent wire-level contract is specified in **`BRAIN_HOST_PROTOCOL.md`** (HTTP/JSON over UNIX socket, two API patterns, SSE for streams, lockstep versioning). Anything that crosses this boundary — mDNS publish/unpublish, OS updates, Tier-2 systemd/config ops — runs on that protocol.

### Layer 2 — `malmo-brain` (where 95% of logic lives)

A single Go process holding all orchestration logic. Internal packages:
- App manager — install / lifecycle / updates (spec: `APP_LIFECYCLE.md`)
- Proxy manager — talks to Caddy admin API
- Mesh manager — talks to Headscale
- Service manager — provisions DBs in managed Postgres / Redis
- Storage manager — mergerfs, tiers, app data dirs
- Identity — accounts, sessions, ACLs
- API server — serves the dashboard API (spec: `BRAIN_UI_PROTOCOL.md`)
- Backup orchestrator
- **Health manager** — owns the typed set of active health issues, consults them to gate write/app/user operations, surfaces them via the API + SSE channel. The brain runs in *degraded mode* when any issue is active. Spec: `HEALTH.md`.

Persists its own state in **SQLite** (single file, no separate DB process for malmo's own data; managed Postgres is for *apps*, SQLite is for *malmo*).

**Why Go:** every adjacent tool is in Go (Docker, containerd, Headscale, Caddy, Traefik, Avahi bindings). Static binary, mature concurrency model, the lingua franca of the ecosystem.

**Why one binary, not microservices:** single-node home appliance, microservices are pure overhead. Internal modularity via clean Go packages is enough.

### Layer 3 — managed sidecars

- **Caddy** as the reverse proxy. Brain calls its admin API to add/remove routes when apps install. Built for dynamic config; native Let's Encrypt.
- **`malmo-ui`** — the dashboard's static file server (`caddy:alpine` + the built UI bundle baked in). A brain-launched control-plane container; Caddy routes all non-API traffic to it. Stack and deploy model in `WEB_UI.md`.
- **Managed Postgres** — one shared instance per major version users depend on. Brain is the only DB creator. Apps get scoped credentials.
- **Managed Redis** similarly.
- **User apps** — each its own compose stack, brain manages lifecycle.

The brain runs *next to* these and orchestrates them — not inside them.

## Decisions

### Locked: brain runs as a container

- The `malmo-brain` ships as a Docker image, run by Docker, supervised by `host-agent`.
- **Why:** atomic production updates (pull new tag, recreate container, trivial rollback by reverting tag). No partial-install failure modes the way `apt`-based deploys produce. Same image runs in dev, staging, prod. Future migration to A/B immutable OS updates becomes a host-agent change, not a brain change — the brain doesn't move.
- **Cost we accept:** marginally slower dev loop than `go run` on the host. Mitigated with bind-mount + `air` (or equivalent) hot-reload during development. Production wins are worth the dev tax.
- Performance is a non-factor — container overhead for a long-running daemon is negligible.

### Locked: Docker socket exposure mitigated by socket proxy

- The brain does **not** mount `/var/run/docker.sock` directly. It talks to a `tecnativa/docker-socket-proxy` container at `tcp://docker-proxy:2375`. Brain config: one env var (`DOCKER_HOST=tcp://docker-proxy:2375`).
- The proxy is configured with an allowlist of Docker API endpoint families the brain actually needs (`CONTAINERS=1`, `IMAGES=1`, `NETWORKS=1`, etc.). Dangerous endpoints (`EXEC`, arbitrary host mounts on `POST /containers/create`) are denied.
- **Why:** the brain has the largest attack surface (HTTP API exposed to the LAN, third-party app manifests we evaluate). If it's ever compromised, the proxy prevents trivial host takeover via Docker. Cheap defense in depth — one extra container, one env var, ~5MB RAM, negligible ongoing burden.
- **Operational rule:** when the brain needs a new Docker API endpoint family, that's an explicit config change to the proxy. Forces conscious thought about what privileges the brain holds.

### Locked: host-agent runs under systemd as `Type=notify`

- Unit type **`Type=notify`**, not `simple`. host-agent calls `sd_notify(READY=1)` only after its UNIX socket is bound and accepting connections. Anything ordered `After=host-agent.service` (brain container, downstream services) sees a ready socket on first try — no startup races.
- **`Restart=always`**, `RestartSec=2s`, `StartLimitBurst=5` / `StartLimitIntervalSec=60s`. Crash → restart fast, but a sustained crashloop (5 in 60s) stops and surfaces failure. On stop, `OnFailure=malmo-recovery.target` routes the box into recovery boot — host-agent itself broken means the brain can't run, so the dashboard isn't reachable and a separate rescue page is the only option (see `BOOT.md` # Failure → recovery target — the narrow cases).
- **Watchdog enabled.** `WatchdogSec=30s`; host-agent pings `sd_notify(WATCHDOG=1)` every ~10s from a dedicated goroutine. Converts a hung process into a restart. Conservative interval avoids false positives during long legitimate operations (backup tarball walks, large image pulls).
- **Ordering**: `After=malmo-storage-ready.target docker.service network-online.target`, `Wants=network-online.target malmo-storage-ready.target`, `Requires=docker.service`. host-agent starts even if storage assembly partially failed — it reads `/run/malmo/health/storage.json` and forwards findings to the brain, which raises health issues per `HEALTH.md`. The brain is the single source of truth for "is it safe to write right now"; systemd ordering is best-effort, not strict-gate. Full boot-chain context in `BOOT.md`.
- **Socket activation is deliberately deferred** — extra complexity with no win on an always-on appliance. Tracked in `NEXT.md` if we ever revisit.

### Locked: host-agent hardening directives

host-agent is root, but its filesystem and kernel reach is constrained by systemd:

```
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/malmo /etc/malmo /run/malmo
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
```

`ProtectHome=true` is load-bearing: **host-agent has no general filesystem-read API over `/home`.** Any operation that touches a user's home directory is a narrow, named operation in `BRAIN_HOST_PROTOCOL.md`, not a generic file-read primitive. This is a deliberate constraint, not an oversight.

Capability dropping (`CapabilityBoundingSet=`) is **not** used — host-agent's job is "do root things on the box" (mount, cryptsetup, useradd, systemctl). The filesystem and kernel constraints above are where the meaningful blast-radius reduction happens.

### Locked: host-agent launches the brain container

- During its own startup, after Docker is ready, host-agent pulls (if needed) and starts the brain container with Docker restart policy `unless-stopped`. After that, Docker keeps brain alive across host-agent restarts; host-agent does not actively supervise brain during steady-state operation.
- **One chain of custody.** host-agent owns every container on the box (apps, managed services, *and* brain). The reconciler pattern from `APP_LIFECYCLE.md` extends to brain naturally.
- **Lockstep version check happens at launch.** host-agent refuses to start a brain whose major protocol version it doesn't speak (per `BRAIN_HOST_PROTOCOL.md`). One actor owns both endpoints' lifecycles, so the check is a function call, not an out-of-band reconciliation. Concretely (#164): the brain image declares its wire-protocol major in the `malmo.protocol.major` OCI label, and host-agent reads it (`docker image inspect`) and compares against its own `protocol.Major` before `docker run` — a mismatch, or a missing label, is a refused launch with a clear error, not a first-request failure.
- The alternative — a separate `malmo-brain.service` systemd unit — was rejected because it splits the update flow (host-agent already owns the brain+UI update stream per `UPDATES.md`) and moves the lockstep version check out-of-band into first-request failure.

### Locked: Caddy is malmo substrate, runs as a container

- Caddy is **not a Tier-2 OS integration**. It needs no NET_ADMIN, no external auth flow, has no user-facing settings page; it's malmo's own machinery, in the same bucket as the brain itself.
- Runs as a container, started by the brain alongside other malmo-managed containers. Joins the malmo Docker network and publishes host ports 80 and 443.
- Configured via Caddy's **admin API on `:2019`**, reached by the brain over the Docker network — no Caddyfile on disk, no `caddy reload` shell-out. Atomic reloads, no file I/O. The admin endpoint binds `0.0.0.0:2019` (not `localhost`): the brain is a *separate* container, so it can't reach Caddy's loopback — the listener must be on the container's network interface. The host never publishes 2019 (only 80/443 are exposed), so it's reachable only from inside the Docker network. **Network-isolation invariant:** Caddy's admin API has no auth — its only protection is that nothing untrusted shares its network. So **app containers must never be attached to the network that carries the admin port.** Caddy reaches app upstreams by joining each app's own per-app network (Caddy connects outward); the control-plane network (brain, Caddy, `malmo-ui`, the socket proxy) stays trusted-only. An app on the same network as `:2019` could rewrite the entire route table.
- **Catch-all 404 invariant.** Caddy's `malmo` server always has a final route at the end of `routes[]` with `@id=malmo-catchall` and no matcher, returning HTML 404 ("No app at this hostname"). The brain inserts dynamic per-app routes at index 0 (`POST /config/apps/http/servers/malmo/routes/0`) so the catch-all stays last. On startup the brain calls `EnsureCatchAll` which re-installs the catch-all if missing — survives Caddy state loss, hand-edits, or config drift. Returning 200 empty for unmatched routes is a UX failure and breaks tests that can't distinguish "routed" from "no-match".
- **Updates ride the brain+UI stream**, not the Debian base stream — image tag in the release manifest, pulled and reconciled by host-agent + brain. Decouples Caddy version from Debian's release cadence.
- **Performance is a non-issue for the household workload.** Docker bridge networking adds microseconds per connection — invisible at household scale. The heaviest "big file" path (SMB transfers of media to/from laptops) bypasses Caddy entirely: SMB is a Tier-2 host service on port 445. DLNA likewise. Caddy carries HTTP app traffic only (Jellyfin/Plex/Immich streaming, dashboards, app UIs); even a household of 4K streamers is well below containerized Caddy's ceiling.
- **Memory overhead from containerization is negligible.** Containers are not VMs — same process, same RSS, no extra kernel or libc.
- **Escape hatch if needed:** `--network=host` recovers host-level networking at the cost of internal-DNS service-name routing. We don't expect to need it.

### Locked: the dashboard UI is a brain-launched container

- The dashboard ships as its own `malmo-ui` container (`caddy:alpine` + the built UI bundle baked in — full deploy model in `WEB_UI.md`). It is **launched by the brain**, alongside Caddy and the docker-socket-proxy, as part of bringing up the control-plane stack — *not* by host-agent. host-agent's chain of custody stops at the brain (# Locked: host-agent launches the brain container); the brain owns every container downstream of itself, the UI included.
- **Why the brain and not host-agent:** the UI version is bound to the brain's API version (the bundle declares the API minor it requires; `WEB_UI.md` # deploy model), and brain+UI update as one stream (`UPDATES.md`). Keeping both launches under the brain means the version pairing is enforced by the actor that already owns it, not split across the host-agent boundary.
- The LAN-facing Caddy routes `/api/v1/*` (and the SSE/log streams) to `malmo-brain` and everything else to `malmo-ui` (`WEB_UI.md` # deploy model). Both are reconciled by the brain on startup the same way app containers are.

### Locked: implementation specifics

- **Language:** Go. Single binary. Static.
- **Internal structure:** clean packages (app manager, proxy manager, mesh manager, service manager, storage manager, identity, API server, backup orchestrator). Single process — no microservices.
- **State:** SQLite, single file in a persistent volume, for malmo's own state. *Managed Postgres is for apps; SQLite is for malmo.*
- **Reverse proxy:** Caddy, controlled via its admin API.
- **`host-agent` is and stays minimal** — bootstrap, brain supervision, OS-level updates. Anything that changes frequently lives in the brain, not here.

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in the relevant topical doc) plus `DECISIONS.md` if they flip a position.
