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
- **Routing** — dynamically configures the reverse proxy as apps come and go (`photos.malmo.local` → container).
- **mDNS publishing** — registers each app's hostname with Avahi (via `host-agent`).
- **API + UI** — HTTP + SSE + (future) WebSocket API for the web UI; ships the UI. Wire spec in `BRAIN_UI_PROTOCOL.md`; client-side stack in `WEB_UI.md`.
- **Identity** — user accounts, sessions, admin/root account, sharing/ACLs. See `AUTH.md`.
- **Managed services** — runs shared Postgres / Redis / etc., provisions databases on app install, handles backups + upgrades. See `SERVICE_PROVISIONING.md`.
- **Storage** — manages the disk pool, fast/slow tier mounts, app data directories. See `STORAGE.md`.
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

- Bootstrap the system at boot — mount the storage pool, check disks, start Docker.
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

Persists its own state in **SQLite** (single file, no separate DB process for malmo's own data; managed Postgres is for *apps*, SQLite is for *malmo*).

**Why Go:** every adjacent tool is in Go (Docker, containerd, Headscale, Caddy, Traefik, Avahi bindings). Static binary, mature concurrency model, the lingua franca of the ecosystem.

**Why one binary, not microservices:** single-node home appliance, microservices are pure overhead. Internal modularity via clean Go packages is enough.

### Layer 3 — managed sidecars

- **Caddy** as the reverse proxy. Brain calls its admin API to add/remove routes when apps install. Built for dynamic config; native Let's Encrypt.
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

### Locked: implementation specifics

- **Language:** Go. Single binary. Static.
- **Internal structure:** clean packages (app manager, proxy manager, mesh manager, service manager, storage manager, identity, API server, backup orchestrator). Single process — no microservices.
- **State:** SQLite, single file in a persistent volume, for malmo's own state. *Managed Postgres is for apps; SQLite is for malmo.*
- **Reverse proxy:** Caddy, controlled via its admin API.
- **`host-agent` is and stays minimal** — bootstrap, brain supervision, OS-level updates. Anything that changes frequently lives in the brain, not here.

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in the relevant topical doc) plus `DECISIONS.md` if they flip a position.
