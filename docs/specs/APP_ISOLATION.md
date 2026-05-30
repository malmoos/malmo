# malmo App Isolation

> Working spec for how the brain enforces what apps can do at runtime. Companion to `SPEC.md`, `CONTROL_PLANE.md`, `APP_MANIFEST.md`, `SERVICE_PROVISIONING.md`, `STORAGE.md`, `FIRST_RUN.md`.

The manifest declares *intent* (`internet: true`, `lan: false`, `folders: [{folder: photos}]`). This document describes what those declarations mean concretely — what Linux/Docker primitives back them, what the defaults are, and where the trust boundaries sit.

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

malmo is multi-user (`FIRST_RUN.md`). Every user has private data; admins manage the box but cannot read other users' files through normal paths. App execution reflects this: **every app instance has an owner**, and a personal instance is scoped to its owner's data (`DASHBOARD.md` # instances are owner-scoped, `DECISIONS.md` 2026-05-29).

### Owner-scoped instances

Whether an instance is **household** (admin-owned, shared — one instance, the app's own internal multi-user separates people inside it) or **personal** (owned by one user, its own data, folders, and route) is **elected by the installing user**, not derived from a tier or declared in the manifest (`APP_MANIFEST.md` # G). Admins choose household or personal; members install personal only.

A personal instance is the per-owner compose-project shape locked in `APP_LIFECYCLE.md` # an app instance is a Docker Compose project: an independent project named `malmo-<instance-id>`, with its own instance id, data dir, and slug `<slug>--<user>`. If two users each install the same app personally, two independent instances run.

- A personal instance's main process runs as the **owner's Linux UID/GID** (assigned at user creation, in the malmo-reserved 3000+ range). The brain enforces this via the compose `user:` field; we do not rely on `PUID`/`PGID` env-var conventions. (A household instance runs as a shared service identity, not a single member's UID.)
- The container sees only the bind-mounted use-case folders declared in the manifest (the owner's `/home/<user>/Photos/`, `Documents/`, etc., or a household `/srv/malmo/shared/<Folder>/` when the installer elected the shared source — see `APP_MANIFEST.md` # `folders`), mounted at the fixed `/malmo/<folder>` paths. Other users' homes are not bind-mounted; they're not even on the filesystem the container can reach.
- App authors write a single-user app. They do not need to know about users, sessions, or identity — malmo runs one instance per owner.

### Tier-2 apps (always shared)

Tier-2 apps (Tailscale, SMB, DLNA, future entries — `SERVICE_PROVISIONING.md`) are inherently box-wide. One instance per box, admin-installed. They are not owner-scoped. Tier-2 ≠ Tier-3 from a runtime-isolation standpoint and most of the rules in this document apply differently. See `SERVICE_PROVISIONING.md` for their model.

### No `multi_user` field

Household-vs-personal is an install-time election, not a static property of the app, so **no manifest field expresses it** (an earlier draft's `multi_user.mode` was removed — `DECISIONS.md` 2026-05-29). Cross-user *awareness* inside a single instance (e.g., a future malmo-built Photos app with its own sharing UI) is a separate, deferred concern, not a v1 manifest field.

### Routing per instance

Each instance has its own subdomain: bare `<slug>.malmo.local` for the household instance, `<slug>--<user>.malmo.local` for a personal one (`DASHBOARD.md` # instance naming). Ownership is legible in the URL itself, and one wildcard cert covers every instance.

Consequence: a personal instance's URL is owner-specific — `immich--alex.malmo.local` is Alex's. Granular cross-user sharing (intentionally exposing one user's content to another) is a future feature; v1 sharing is "drop it in `~/Shared/`" only (`STORAGE.md`).

### User lifecycle

- **Login:** brain loads the user's session, ensures their per-user containers are running for the apps they have installed.
- **Logout:** containers stay running. Lifecycle is decoupled from session activity, so background work (sync, backups, scheduled jobs) keeps working.
- **User deletion:** admin is prompted. Default action is to **archive** the user's data — rename `/home/<slug>/` to `/home/.archived/<slug>-<date>/` (atomic rename within the same filesystem) and stop their app instances, not to delete. Admin can choose to delete instead. The archive can be cleaned up later from Settings.

### Privacy ceiling at v1

Per-user data lives at `/home/<user>/` with `0750` perms owned by the user (`STORAGE.md` # Permissions). Other malmo users cannot read it through the filesystem. **The admin (or anyone with shell as root) can read everything**, because v1 only encrypts at the disk level (LUKS), not per user. Admin-resistant per-user encryption (fscrypt) is on the roadmap; see `STORAGE.md` "Future: per-user encryption" for the planned upgrade. v1 features that touch user data are designed as if that upgrade were already in place — backup is per-user-keyed, etc. — so the upgrade is data-only, not feature-redesign.

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
- A shared use-case folder (`folders: [...]` — two of the same user's apps pointed at the same source see the same `~/Photos/`, or the same `/srv/malmo/shared/` tree)

A user who genuinely wants two apps wired directly together puts them in one Door-2 compose. Cross-app networking is not a v1 feature.

---

## Filesystem & devices

### Root filesystem

Writable by default (Docker default). Most existing images assume `/var/log`, `/var/cache`, `/run` are writable; enforcing read-only would break ~30% of catalog candidates and burn support cycles.

Opt-in hardening: `security.read_only_root: true` in the manifest enables read-only root + tmpfs for declared scratch paths. Recommended for security-sensitive apps; not the default.

The writable layer resets on container *recreation* (image update, uninstall/reinstall) but persists across restarts.

### Volumes

App state (indexes, configs, the app's own DB) lives under the instance dir at `/var/lib/malmo/instances/<id>/data/`, via bind mounts only — no Docker named volumes (`APP_LIFECYCLE.md` # on-disk layout per instance). The author writes the bind mount against `${MALMO_DATA_DIR}/foo:/foo` (or the relative `./data/foo:/foo`); the brain injects `MALMO_DATA_DIR`, so authors reference a stable variable rather than a hardcoded host path.

`/tmp` is a size-capped tmpfs.

**Bind mounts to arbitrary host paths are forbidden in store manifests.** Allowed in Door-2 compose because the user wrote it.

### User content (use-case folders)

Apps reach user content by **bind-mounting use-case folders** declared in the manifest (`APP_MANIFEST.md` # `folders`):

```yaml
permissions:
  folders:
    - { folder: photos, mode: write }
    - { folder: documents, mode: read }
```

Each declared folder is bind-mounted at a fixed in-container path `/malmo/<folder>` and the absolute path injected as `MALMO_FOLDER_<NAME>`; the app's compose maps that variable to its own library path (`APP_MANIFEST.md` # `folders`). `mode` defaults to `read` if unspecified — least privilege, and `write` is a deliberate choice the catalog reviewer notices.

Use-case folder taxonomy v1 (fixed): `photos`, `documents`, `movies`, `music`, `notes`, `downloads` — mapped to capitalized directories (`Photos/`, `Documents/`, etc., per `STORAGE.md`). User-defined folders deferred.

**The host source is the installer's per-folder election, not the manifest's.** A declared folder binds one of two sources, chosen at install:

- **Personal source** — the owner's `/home/<user>/<Folder>/`, owned by that user (UID in the malmo 3000+ range), with `/home/<user>/` mode `0750` (`STORAGE.md` # Permissions). The container runs as the owner's UID and reaches no other user's home. This is the default offered for a personal instance.
- **Shared source** — `/srv/malmo/shared/<Folder>/`, the household tree every member can already reach via the `malmo-shared` group (`STORAGE.md`, `USERS_AND_GROUPS.md`). The brain adds the container to `malmo-shared` (compose `group_add`) so it has exactly that group's access — no new privilege. Always used by a household instance; offered to a personal instance when the installer elects it.

A personal instance reading the shared tree (the "my own Jellyfin on the family library" case) is now a supported election — **this supersedes the earlier MVP carve-out** that forbade Tier-3 per-user apps from crossing the per-user/shared boundary (`DECISIONS.md` 2026-05-30). The boundary still holds in the one direction that matters: a household (shared) instance never binds a single member's private `~/`, because there is no one owner to scope it to.

The `PUID`/`PGID` env-var pattern from earlier drafts is gone — we set the container's runtime UID directly via the compose `user:` field, so file ownership lines up natively. Apps that hardcode an internal UID and ignore the runtime user override will hit permission errors and get pulled from the catalog.

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
| `folders: [...]` (personal source) | bind mount of `/home/<user>/<Folder>/` at `/malmo/<folder>` + injected `MALMO_FOLDER_<NAME>` |
| `folders: [...]` (shared source) | bind mount of `/srv/malmo/shared/<Folder>/` at `/malmo/<folder>` + `malmo-shared` group membership (`group_add`) + injected `MALMO_FOLDER_<NAME>` |

App authors think "I need to control the network," not "I need `NET_ADMIN`." The brain does the translation.

### Escape hatch — not a v1 store field

There is **no `permissions.capabilities` list in the v1 store schema.** A store app gets `cap_drop: [ALL]` and adds nothing back; admission rejects any `cap_add` (`APP_LIFECYCLE.md` # admission policy, `APP_MANIFEST.md` # E). A reviewed-at-submission capability list for the rare legitimate case (`NET_ADMIN`, `SYS_TIME`) is a **deferred** schema addition — tracked in `NEXT.md`, not assumed by the catalog today.

The app that genuinely needs a capability, `privileged`, the Docker socket, or low-level hardware access goes through the **Door-2 custom path** (the user wrote the compose, owns the consequences) or, for curated OS integrations, **Tier 2** (`SERVICE_PROVISIONING.md`).

### Forbidden in store

These are container-escape primitives and are catalog-rejected:

- `privileged: true`
- Mounting `/var/run/docker.sock`
- any `cap_add` (`SYS_ADMIN` especially)

A store app cannot request them. The intent is that Door-2 custom compose carries them — the user wrote it — but note that the **current admission policy runs identically for both doors** (`APP_LIFECYCLE.md` # admission policy), so the exact set of primitives Door-2 may relax is an **open item** (`NEXT.md`), not yet a door-asymmetric rule.

### Not in v1

- **User namespace remap.** Breaks too many images. Revisit once the catalog is mature.
- **Custom seccomp / AppArmor profiles.** Docker defaults are sufficient for the threat model.

---

## Resource limits

The manifest declares **recommended** specs only — never a ceiling:

```yaml
resources:
  recommended:
    memory: 512M
    cpu: 1.0
```

Used for:
- **Capacity check at install time.** "You have 800M free; this app wants 512M; fine."
- **Store display and sorting.**

**The manifest cannot impose a limit — there is no `limit` field, by design.** The author can't see the user's hardware: a baked-in `mem_limit` would OOM-kill an app during a legitimate usage peak (photo indexing, transcode, ML) on a box with gigabytes free. Limiting is the *user's* call ("the user owns the box"), not the author's. `recommended` is advice; it never throttles.

### Default runtime: burst freely

Apps run with **no cgroup cap by default** and may burst above their recommendation. Peaks should complete fast, not be throttled. CPU in particular is **never capped** — it's time-shared, so a CPU-hungry app simply runs slower and harms nothing; throttling it only makes it feel needlessly sluggish.

### Control-plane protection (structural, not a cap)

"Default unlimited" leaves one hole: a runaway app exhausting RAM could trip the kernel OOM-killer and take down the *brain*, removing the very UI the user needs to fix it. Closed structurally, independent of any app limit:

- **The brain and host-agent get a protected OOM score** (effectively last-to-be-killed under memory pressure). A genuine runaway app is killed before the control plane.

This is deliberately **OOM-priority, not a hard cap**: a cap bites even when RAM is free (degrading peaks); OOM scoring only acts when memory is *actually* exhausted, so peaks that fit are untouched and only a true runaway is reaped — and the offending app dies before the brain. It serves the no-degraded-peaks goal and the don't-starve-the-brain goal at once.

### Optional user-set cap (memory only)

For the rare app that *persistently* misbehaves, the user can set a per-app **memory** cap from the UI:

- **Default off.** Set only when a user chooses to clamp a specific hog.
- **Memory only** — no CPU cap (see above).
- Surfaced reactively: the per-app usage view (`LOCAL_ANALYTICS.md`) and the app-hog signal (`NOTIFICATIONS.md`) help the user identify *which* app to clamp.
- Setting it shows a plain-language warning: *"Limiting this app may cause it to slow down or restart during heavy use."* The user-caused OOM-during-peak risk is theirs, explicitly acknowledged.

When the user sets a cap, the brain applies the corresponding cgroup `mem_limit` on the container; clearing it returns the app to unlimited burst.

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
- Postgres data lives under the per-(user, app) instance dir (`/var/lib/malmo/instances/<id>/managed/postgres/...`), owned by the user's UID with restrictive perms. Cross-user filesystem access is blocked the same way every other app-state dir is — POSIX ownership + the brain controlling the bind-mount surface. **Open:** when fscrypt lands for `/home/<user>/`, does it extend to `/var/lib/malmo/instances/` for per-user app state? Tracked in `NEXT.md`.
- Network-layer isolation: cross-user, cross-app database access is impossible by construction.
- Cost: in the worst case, N users × M apps requesting Postgres = N×M Postgres instances. Realistic case (1–2 users, one heavy account running most apps) keeps this well within home-server budgets.

**Likely to change.** Once we have catalog data on duplication patterns, we may move to a shared service plane (one Postgres process, per-(user, app)-scoped databases and credentials). Schema and manifest stay the same; placement becomes an internal detail.

---

## Failure mode

When an app violates its declared permissions at runtime — tries to reach the LAN with `lan: false`, opens a raw socket without `NET_RAW`, writes to a `read`-mode folder — the action **silently fails at the kernel/Docker layer** and is **logged to the app's log stream** with a clear reason:

```
[malmo-isolation] blocked outbound connection to 192.168.1.50:80 — app declares lan: false
```

No popup, no kill. The app sees a normal "connection refused" or "permission denied" and handles it (or doesn't). The brain sees the violation in the log stream and surfaces it in the app's troubleshooting view.

This matches the user's mental model: "the app says it doesn't need X, the OS makes sure it really doesn't get X." Loud failures would punish honest apps that probe for optional capabilities.

---

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
