# malmo App Isolation

> Working spec for how the brain enforces what apps can do at runtime. Companion to `SPEC.md`, `CONTROL_PLANE.md`, `APP_MANIFEST.md`, `SERVICE_PROVISIONING.md`, `STORAGE.md`, `FIRST_RUN.md`.

The manifest declares *intent* (`internet: true`, `lan: false`, `shared_storage: [photos]`). This document describes what those declarations mean concretely — what Linux/Docker primitives back them, what the defaults are, and where the trust boundaries sit.

## Principles

- **The user owns the box.** Defaults are conservative; escape hatches exist (Door-2 custom compose, `security.*` overrides). We don't lock the user out of their own machine.
- **Store apps carry a meaningful trust claim.** Things that can root the host are not allowed in the catalog. Door-2 custom compose accepts anything — the user wrote it, the user owns the consequences.
- **Manifest declares intent. Brain enforces it.** Apps that lie about their permissions either fail (silently blocked at the kernel/Docker layer) or get pulled from the catalog.
- **Same enforcement everywhere.** Store apps and custom apps share the same runtime. Only the *defaults* and *catalog rules* differ.

## Trust tiers

| | Store apps (Door 1) | Custom apps (Door 2) |
|---|---|---|
| Manifest source | Catalog repo, reviewed | User-supplied or synthesized from raw compose |
| Default permissions | Least-permissive (must declare what they need) | Permissive (user wrote it) |
| Forbidden capabilities | `privileged`, docker socket, `SYS_ADMIN` | None — user can do anything |
| Enforcement mechanism | Identical to custom | Identical to store |

---

## Multi-user runtime

malmo is multi-user (`FIRST_RUN.md`). Every user has private data; admins manage the box but cannot read other users' files through normal paths. App execution reflects this: **most apps run as per-user instances, scoped to one user's data**.

### Per-user instances (default for Tier-3 apps)

When a user installs a Tier-3 app, the brain spawns a container scoped to that user. If two users install the same app, two containers run — one per (user, app) tuple. Containers are named `malmo-<app>-<user-slug>`.

- The container's main process runs as the **user's Linux UID/GID** (assigned at user creation, in the malmo-reserved 3000+ range). The brain enforces this via the compose `user:` field; we do not rely on `PUID`/`PGID` env-var conventions.
- The container sees only that user's pool directories (`/mnt/data/users/<slug>/...`). Other users' pools are not bind-mounted; they're not even on the filesystem the container can reach.
- App authors write a single-user app. They do not need to know about users, sessions, or identity. malmo handles replication.

### Tier-2 apps (always shared)

Tier-2 apps (Tailscale, SMB, DLNA, future entries — `SERVICE_PROVISIONING.md`) are inherently box-wide. One instance per box, admin-installed. They are not replicated per user. Tier-2 ≠ Tier-3 from a runtime-isolation standpoint and most of the rules in this document apply differently. See `SERVICE_PROVISIONING.md` for their model.

### No `multi_user` field in MVP

Every Tier-3 app is per-user. Every Tier-2 app is shared. No manifest field expresses this — it follows from the tier. The schema reserves a future `multi_user: per_user | aware | shared` field for sophisticated apps that want cross-user awareness (e.g., a future malmo-built Photos app with sharing UI), but it is not user-settable at v1.

### Routing across users

Subdomains stay clean: `<app>.malmo.local` for everyone. The reverse proxy reads the session cookie, identifies the logged-in user, and routes to that user's per-user instance.

Consequence: if Andrei sends Maria a URL like `photos.malmo.local`, Maria lands on *her* Photos, not Andrei's. This is a deliberate privacy property — no URL-level cross-user data leakage — but means the URL is not a stable shareable handle. Granular cross-user sharing (intentionally exposing one user's content to another) is a future feature; v1 sharing is "drop it in the global `shared/` pool" only (`STORAGE.md`).

### User lifecycle

- **Login:** brain loads the user's session, ensures their per-user containers are running for the apps they have installed.
- **Logout:** containers stay running. Lifecycle is decoupled from session activity, so background work (sync, backups, scheduled jobs) keeps working.
- **User deletion:** admin is prompted. Default action is to **archive** the user's data to `/mnt/data/archived-users/<slug>-<date>/` and stop their app instances, not to delete. Admin can choose to delete instead. The archive can be cleaned up later from Settings.

### Privacy ceiling at v1

Per-user data lives at `/mnt/data/users/<slug>/...` with `0700` perms owned by the user. Other malmo users cannot read it through the filesystem. **The admin (or anyone with shell as root) can read everything**, because v1 only encrypts at the disk level (LUKS), not per user. Admin-resistant per-user encryption (fscrypt) is on the roadmap; see `STORAGE.md` "Future: per-user encryption" for the planned upgrade. v1 features that touch user data are designed as if that upgrade were already in place — backup is per-user-keyed, etc. — so the upgrade is data-only, not feature-redesign.

---

## Network model

### Per-app network

Every app gets its own Docker bridge network. Inter-container DNS works inside it (the app's own compose services resolve each other by name). Inter-*app* traffic is denied by default — apps live on separate networks.

The brain reaches the app's web port over this network for reverse-proxy routing. Apps **do not bind to host ports** in store mode; the brain owns 80/443 for the subdomain proxy + TLS termination. Manifest declares `web.port: 8080` and the brain wires `myapp.malmo.local → container:8080`. Door-2 compose can publish host ports if the user wrote it that way.

### `internet: true` / `false`

- `true` (default for custom, opt-in for store): per-app bridge has NAT to the host's default route. Outbound works, inbound is blocked except via the reverse proxy.
- `false`: per-app bridge is created with `internal: true`. No NAT, no external route — kernel-level block, not iptables rules we maintain. External DNS fails. Internal DNS for inter-container resolution still works.

Applies to IPv4 and IPv6.

### `lan: true`

The container gets a **macvlan attachment** to the host's primary LAN interface in addition to its per-app bridge. From the LAN's perspective the container is its own device with its own IP and MAC.

This is the only mechanism that preserves the use cases that justify `lan: true`:

- mDNS / SSDP / Bonjour discovery (Home Assistant, Scrypted, Frigate, Chromecast, AirPlay, DLNA renderers)
- Direct unicast to LAN devices (smart bulbs, IP cameras, network printers)
- Pi-hole / AdGuard serving DNS to other LAN devices

Cost: each `lan: true` app burns one IP on the user's LAN. Tolerable — single-digit count of such apps on a typical box.

**Wi-Fi caveat.** Some Wi-Fi drivers reject macvlan. On Wi-Fi-only setups the brain falls back to bridge + LAN route, with a documented limitation: unicast to LAN devices works, multicast discovery does not. The first-run network wizard captures the primary LAN interface; if it's wireless, the user is warned at install time for `lan: true` apps.

### Inter-app traffic

Denied. Apps that need to share data go through:
- Managed services (`services: [postgres]`)
- Shared storage pools (`shared_storage: [photos]`)

A user who genuinely wants two apps wired directly together puts them in one Door-2 compose. Cross-app networking is not a v1 feature.

---

## Filesystem & devices

### Root filesystem

Writable by default (Docker default). Most existing images assume `/var/log`, `/var/cache`, `/run` are writable; enforcing read-only would break ~30% of catalog candidates and burn support cycles.

Opt-in hardening: `security.read_only_root: true` in the manifest enables read-only root + tmpfs for declared scratch paths. Recommended for security-sensitive apps; not the default.

The writable layer resets on container *recreation* (image update, uninstall/reinstall) but persists across restarts.

### Volumes

Persistent paths in the manifest map to brain-controlled host paths under `/var/lib/malmo/apps/<id>/`. The app declares the path inside the container; the brain decides where on the host it lives. App authors don't pick host paths.

`/tmp` is a size-capped tmpfs.

**Bind mounts to arbitrary host paths are forbidden in store manifests.** Allowed in Door-2 compose because the user wrote it.

### Pool storage (per-user)

Each Tier-3 app instance is scoped to its user's private pool directories. Manifest:

```yaml
permissions:
  shared_storage:
    - { name: photos, mode: rw }
    - { name: documents, mode: ro }
```

`mode` defaults to `ro` if unspecified — least privilege, and `rw` is a deliberate choice the catalog reviewer notices.

Pool taxonomy v1 (fixed): `photos`, `documents`, `videos`, `music`, `downloads`. User-defined pools deferred; schema reserves `custom:<name>`.

**Layout.** Per-user pools live at `/mnt/data/users/<slug>/<pool>/`, owned by that user (UID in the malmo 3000+ range), mode `0700`. The container runs as the user's UID, sees its own pool dirs only, and never has a path to other users' files at all.

**The global `shared/` pool is not exposed to per-user app instances at MVP.** A second tree at `/mnt/data/shared/<pool>/` exists for the v1 cross-user sharing story (drop a file in shared, every user's file browser sees it), but Tier-3 app containers do not bind-mount it. Allowing apps to read across the user/shared boundary is a deferred feature; the manifest schema reserves `shared_storage_access: read | write` for it.

The `PUID`/`PGID` env-var pattern from earlier drafts is gone — we set the container's runtime UID directly via the compose `user:` field, so pool ownership lines up natively. Apps that hardcode an internal UID and ignore the runtime user override will hit permission errors and get pulled from the catalog.

### Devices

`permissions.devices` lists explicit device paths or shorthand categories:

```yaml
permissions:
  devices:
    - /dev/ttyUSB0          # Zigbee/Z-Wave dongle
    - /dev/video0           # webcam
```

Nothing else under `/dev` is exposed. The brain validates that requested devices exist before starting the app.

### GPU

Separate field, because driver wiring is platform-specific (NVIDIA runtime, Intel/AMD `/dev/dri`) and the brain has to do real work, not just pass a device through:

```yaml
permissions:
  gpu: true
```

The OS handles drivers, runtime selection, and device exposure. The app sees whatever GPU is present and can introspect model, memory, and capabilities through standard tooling (e.g., `nvidia-smi`, `/dev/dri`). App author requests access; the OS makes it work.

If no GPU is present, the brain refuses to install the app at capacity-check time — same path as `resources.recommended`.

---

## Capabilities & privilege

### Default

`cap_drop: ALL`. Linux capabilities are added back only for what the manifest declares.

### High-level toggles

The common case is expressed through high-level fields in the manifest, not raw cap names:

| Manifest field | Maps to |
|---|---|
| `internet: true` | NAT route on per-app bridge |
| `lan: true` | macvlan attachment + multicast |
| `devices: [...]` | device cgroup entries |
| `gpu: true` | platform-appropriate GPU runtime |
| `shared_storage: [...]` | bind mount + group membership |

App authors think "I need to control the network," not "I need `NET_ADMIN`." The brain does the translation.

### Escape hatch

For the rare app that legitimately needs a specific capability:

```yaml
permissions:
  capabilities: [NET_ADMIN, SYS_TIME]
```

Defaults to empty. Anything in this list is reviewed at catalog submission.

### Forbidden in store

These three are catalog-rejected because they are container-escape primitives:

- `privileged: true`
- Mounting `/var/run/docker.sock`
- `SYS_ADMIN` in `permissions.capabilities`

A store app cannot request them. Door-2 custom compose can do all three — the user wrote the compose, the user owns the consequences. Legit privileged use cases (low-level backup tools, hardware management) are pushed to the Door-2 path.

### Not in v1

- **User namespace remap.** Breaks too many images. Revisit once the catalog is mature.
- **Custom seccomp / AppArmor profiles.** Docker defaults are sufficient for the threat model.

---

## Resource limits

The manifest declares recommended specs:

```yaml
resources:
  recommended:
    memory: 512M
    cpu: 1.0
```

Used for:
- **Capacity check at install time.** "You have 800M free; this app wants 512M; fine."
- **Store display and sorting.**

**No hard cgroup enforcement in v1.** Apps can burst above their recommendation. A misbehaving app that starts evicting others can be addressed later with OOM-priority hints; not a v1 concern at home-server scale.

---

## Secrets & credentials

Injected as **environment variables** at container start. Managed-service credentials, API keys for malmo-provided integrations, etc.

```
DATABASE_URL=postgres://app_xyz:...@managed-postgres:5432/app_xyz
REDIS_URL=redis://...
```

Universally supported by Docker images. The brain may surface these in the UI for advanced users (toggleable view, off by default).

Mounted-file secrets (Docker secrets style) deferred — only ~half of images support it, and the threat model on a single-user home server doesn't justify the compatibility cost.

---

## Managed services placement

When a per-user app declares `services: [postgres]`, the brain runs a Postgres container **co-located on that (user, app)'s per-app network**. Only that specific instance — Andrei's Photos, not Maria's — can reach it.

- Lifecycle tied to the (user, app) tuple. Uninstall Andrei's Photos → Andrei's Postgres goes away; data backed up first per `SERVICE_PROVISIONING.md`. Maria's Photos is untouched.
- Postgres data lives under the user's pool (`/mnt/data/users/<slug>/managed/postgres/...`), so it inherits the same per-user privacy posture (and will inherit per-user encryption when fscrypt lands).
- Network-layer isolation: cross-user, cross-app database access is impossible by construction.
- Cost: in the worst case, N users × M apps requesting Postgres = N×M Postgres instances. Realistic case (1–2 users, one heavy account running most apps) keeps this well within home-server budgets.

**Likely to change.** Once we have catalog data on duplication patterns, we may move to a shared service plane (one Postgres process, per-(user, app)-scoped databases and credentials). Schema and manifest stay the same; placement becomes an internal detail.

---

## Failure mode

When an app violates its declared permissions at runtime — tries to reach the LAN with `lan: false`, opens a raw socket without `NET_RAW`, writes to a `ro` pool — the action **silently fails at the kernel/Docker layer** and is **logged to the app's log stream** with a clear reason:

```
[malmo-isolation] blocked outbound connection to 192.168.1.50:80 — app declares lan: false
```

No popup, no kill. The app sees a normal "connection refused" or "permission denied" and handles it (or doesn't). The brain sees the violation in the log stream and surfaces it in the app's troubleshooting view.

This matches the user's mental model: "the app says it doesn't need X, the OS makes sure it really doesn't get X." Loud failures would punish honest apps that probe for optional capabilities.

---

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
