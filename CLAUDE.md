# malmo

Home server OS — same category as Umbrel / ZimaOS / CasaOS. North star: **simplicity for non-technical users**. Tinkerers are early adopters, not the destination.

## Audience & positioning

**Long-term target: the tech-curious adult.** Comfortable with software but not infrastructure — owns a laptop, uses cloud apps (Google Photos, Dropbox, Notion), can follow a guided setup, but does not want to learn Docker, SSH, or filesystem layouts to use their box. Reference points for that level of comfort: a Plex user, a Synology DSM user, someone who installs apps from the App Store but doesn't enable Developer Tools. They want to **own their data and apps** on hardware they control, without becoming a sysadmin to do it.

**Early adopters: tinkerers.** Patient with rough edges, bring the app ecosystem with them, write manifests, file good bug reports. v1 ships *tolerable* for tinkerers and *delightful* for the long-term audience — when those tensions appear, the long-term audience wins. The "admins-get-sudo, UI is the path, SSH is rescue" stance (`DECISIONS.md` 2026-05-15) is exactly this shape: tinkerers can SSH if they want, but every privileged operation has a UI path so the long-term user never needs to.

**The pitch.** Install on an old laptop or PC, leave it running in the pantry, run the apps you use daily — a grocery list shared with your partner, photos, notes, files — on hardware you own, with data you own. If the original developer disappears, your app keeps working.

**Monetization shape.** Core OS is free + open. Money comes from paid cloud add-ons (off-site backup, DDNS, possibly remote access) and app-store revenue from paid apps. We do not sell hardware in v1; reference hardware is a possible future SKU but never a requirement.

## How we differ from neighbors

malmo's positioning is a *combination*, not a single axis: branding, hardware openness (BYO, broad compatibility), ease of install and daily use, **the app ecosystem (the strongest pillar)**, and developer-friendliness for app authors. The per-competitor view:

| OS | Category | What we copy | Where we differ | Notes |
|---|---|---|---|---|
| **Umbrel / umbrelOS** | Same category | One-click Docker apps, app-store-as-product, friendly install. | Multi-user from day one. Multi-disk + mergerfs from day one. Managed services (one shared Postgres, not one per app). UI is the path for privileged operations (Umbrel punts to SSH-rescue more often). Subdomain routing — Umbrel uses path-based, which we explicitly rejected for browser-isolation reasons (`SPEC.md`). | Closest competitor on category and audience; we differ on architectural bets. |
| **CasaOS** | Same category | Container-first, simple UX, browser-friendly. | Same as Umbrel — multi-user, managed services, subdomain routing. CasaOS leans more "Docker dashboard"; we're more "appliance OS." | Less integrated than Umbrel but in the same shape. |
| **ZimaOS** | Same category | Install-time storage presets (radio buttons, plain-language guidance) is directly inspired by their UX. Container-first. | Multi-user, managed services, subdomain routing. ZimaOS bundles with Zima hardware; we're explicitly BYO. | We copy ZimaOS's storage-setup language ("Maximize storage" / "Protect my data") because it's the right plain-English framing. |
| **Runtipi** | Same category, different vibe | App-catalog model. | Same architectural bets. | Acknowledge, don't actively study. |
| **Yunohost** | Same category, different vibe | App-catalog model. Two concrete borrowings: (a) the **`yunohost diagnosis` check taxonomy** as prior art for the v1 health-check set in `HEALTH.md`; (b) the **app integration-level (0–9) rating** as a model for catalog badges (`Files-first-class`, `Backup-aware`, `Multi-instance`). Their per-app `backup`/`restore` script convention is also a useful reference for our manifest backup hints. | Native packages (no containers), LDAP+SSOwat SSO across apps, path-based routing as default (works because nginx + package-time path patching — won't translate to Docker apps), public-facing assumptions baked in (email stack, DynDNS), sysadmin/activist audience. We're closed-by-default, Docker-app, no cross-app SSO. | Path-based routing is right *for them* because of native apps; doesn't change our subdomain-routing call. |
| **TrueNAS (CORE/SCALE) / HexOS** | **Different category** | Failure-mode philosophy: explicit boot states, "pool missing is a first-class state," graceful degraded UI (`DECISIONS.md` 2026-05-16). System dataset vs. data pool split (we have the same split via `/var/lib/malmo-state` vs. `/var/lib/malmo`). | We are explicitly NOT a NAS. No ZFS, no pools/vdevs/parity-as-first-class. No NAS vocabulary in the UI. Apps are the product, storage is plumbing. HexOS is "TrueNAS made friendly" + cloud-pairing for recovery; we're closed-by-default with no cloud recovery in v1. | Closest model for failure-mode UX; furthest from us in target audience. Study how they handle storage-anomaly states; do not adopt their NAS-first framing. |
| **Synology DSM** | Different category (closed) | **The gold-standard reference for failure-mode UX.** UI always reachable, drive issues surface as banners with guided walkthroughs, the box never bricks. Recovery is one-click from the dashboard, not "SSH in and read logs." | Closed-source, hardware-locked, proprietary. We're open and BYO. Synology is also weak on third-party app ecosystem (Docker is bolted on, not first-class); ours is the opposite. | When designing UX for failure modes, "what would Synology do?" is the right question. (See `HEALTH.md`.) |
| **Unraid** | Different category | Nothing direct. | Power-user storage features, paid OS, NAS vocabulary throughout. | Acknowledge as the "Unraid land" we are not entering. |
| **Proxmox** | Different category | Nothing. | Virtualization / cluster orchestration, sysadmin tool. | Out of category. |

**Pattern across the differences:** malmo sits in the Umbrel/ZimaOS/CasaOS category by *audience and pitch*, and borrows Synology's *failure-mode philosophy* (which TrueNAS also approximates). The combination is the position — "Umbrel-category product with Synology-grade graceful degradation."

**Two phrases worth internalizing:**

- *"We are not a NAS."* Storage is plumbing in service of apps. The user-visible model is "OS drive" and "data drive" — no pools, no vdevs, no parity-as-first-class concepts. See `STORAGE.md`.
- *"Files are first-class, apps are windows."* User content lives in `/home/<user>/Photos/`, not inside an app's opaque library. Uninstalling Immich never deletes the photos. This is the differentiator vs. Nextcloud/Photoprism-style apps that own their content. See `STORAGE.md`.

## Repo state

**Spec-phase only.** No code, no build, no tests. Everything here is markdown design docs. Do not scaffold code, package layouts, or tooling unless explicitly asked.

## Documents

- `SPEC.md` — top-level vision, distribution, local access model, monetization. Start here for context.
- `CONTROL_PLANE.md` — `host-agent` + `malmo-brain` (Go, single binary, SQLite) + Caddy + managed sidecars. Brain runs as a container, talks to Docker via socket-proxy. Architectural overview; topical specs split out into the docs below.
- `APP_LIFECYCLE.md` — how the brain installs / runs / updates / uninstalls apps on top of Docker. Compose-project unit, on-disk layout, reconciler pattern, install transaction, crash handling, slug allocation, Caddy timing.
- `BRAIN_UI_PROTOCOL.md` — wire-level contract between the dashboard and the brain. REST + SSE + future WebSocket. Sibling to `BRAIN_HOST_PROTOCOL.md`; same four-pattern shape.
- `WEB_UI.md` — dashboard codebase, stack (Vue 3 + Tailwind 4 + shadcn-vue + TanStack Query), deploy-model options.
- `APP_MANIFEST.md` — `manifest.yml` schema. One model, two doors (store apps vs. user-pasted compose). Compose file is held verbatim; malmo never rewrites it.
- `APP_STORE.md` — how malmo publishes the app catalog. Static signed JSON (`store.malmo.network/catalog.json`), git-repo source of truth, minisign/Ed25519, separate key from the release manifest. CI resolves image tags to digests so the signed catalog binds versions to specific bytes. v1 catalog is hand-curated by malmo; data model accommodates multiple catalogs from day one (UI shipped for one).
- `SERVICE_PROVISIONING.md` — three tiers: managed data services (Postgres/Redis), OS integrations (Tailscale/SMB), regular apps.
- `APP_ISOLATION.md` — runtime enforcement of manifest permissions; per-user Tier-3 instances; network model.
- `STORAGE.md` — ext4 + LUKS + TPM2 auto-unlock. v1 ships Levels 0–1 only (single OS drive, optional data drive). Mergerfs/SnapRAID are deliberate later additions.
- `FIRST_RUN.md` — installer → setup wizard → dashboard.
- `MALMO_NETWORK.md` — cloud-side surface. MVP slice: one URL scheme at a time via a global "Use secure URLs" toggle (`.local` HTTP by default, opt-in `<box-id>.malmo.network` HTTPS via Let's Encrypt). Deferred: mesh / remote access via Headscale + DERP, device pairing, sharing.
- `AUTH.md` — dashboard auth, sessions, roles, recovery, and where Tier-2 admin UIs live. Password-only in v1; server-side opaque cookies; admin recovery code; SSH separate and opt-in.
- `USERS_AND_GROUPS.md` — how dashboard roles map to Linux accounts and groups. Members unprivileged; admins in `sudo`. UI-first, SSH-as-rescue. Group reference table (`malmo` vs. `malmo-shared` vs. `sudo`). 5-minute UI elevation window.
- `BRAIN_HOST_PROTOCOL.md` — wire-level contract between the brain (in a container) and `host-agent` (on the host with root). HTTP/JSON over UNIX socket, two API patterns (sync + jobs), SSE for streams, lockstep versioning. Happy-path only — failure semantics deliberately deferred.
- `UPDATES.md` — box-side update model across the five streams (Debian base, host-agent, brain+UI, apps, managed services). Triggers, ordering, windows, rollback per stream. Defers control-plane release-manifest schema to `RELEASE_MANIFEST.md`.
- `RELEASE_MANIFEST.md` — control-plane release manifest schema + publishing pipeline. Static signed JSON at `releases.malmo.network/stable.json`, minisign/Ed25519, git-repo source of truth, PR-based promotion. Single `stable` channel in v1; phased rollout + beta deferred with explicit triggers.
- `BUILD.md` — ISO + `.deb` + image build pipeline. `live-build` for v1 (migrate to `mkosi` later), kiosk web installer, signing infrastructure shape.
- `BOOT.md` — boot sequence from kernel-up to dashboard. Owns the systemd-unit chain, `malmo-storage-ready.target` (storage assembly gate with positive canary verification), and the failure → `malmo-recovery.target` routing policy. Recovery dashboard UI itself is deferred (`RECOVERY.md` in `NEXT.md`).
- `TESTING.md` — three-lane test infrastructure: `systemd-nspawn` (fast, per-PR), QEMU+`swtpm` (medium, per-PR), soak + full ISO end-to-end (slow, nightly). Boot-ordering correctness is the load-bearing concern; the lanes are shaped around it.
- `HEALTH.md` — brain-level degraded-mode model. Typed set of health issues with `blocks_writes` / `blocks_apps` / `blocks_users` flags; remediation tiers (1 physical, 2 UI-driven, 3 console/SSH). Dashboard always comes up if the brain can run; systemd `malmo-recovery.target` shrinks to two cases (TPM unseal failure, host-agent crashloop). Closer to Synology than TrueNAS.
- `LOGGING.md` — appliance-grade logging: journald for operational logs (Docker daemon driver = journald, persistent journal on OS drive, ~1 GB cap), brain-SQLite `audit_events` table for user-meaningful events (forever-retained, in backups, append-only). Dashboard surfaces: per-app logs, system logs (admin), Activity view (audit), diagnostic bundle. Synology/Windows pattern with Linux primitives — no aggregator daemon, total footprint <20 MB.
- `DECISIONS.md` — evolution-of-thinking log. Captures what we changed our mind about and *why*. Read this before relitigating; add an entry when a load-bearing decision flips.
- `NEXT.md` — prioritized list of open design topics, with pointers back to the relevant doc for context. The "Open questions" section in each doc is now just a pointer to this file; never add open items to individual docs, add them here.

**Rule: this list is the canonical map of the spec.** Whenever a new top-level `.md` doc is added (or an existing doc's scope changes materially), update this list in the same change. One-line entry: filename, what it owns, and the headline locked decisions. If you find a doc in the repo that isn't listed here, that's a bug — fix it.

When working on a topic, read the relevant doc(s) end-to-end before proposing changes. The docs cross-reference each other heavily and decisions in one constrain the others.

## Load-bearing decisions (don't relitigate without cause)

- **Debian base, single-node, BYO x86, Docker apps, custom YAML manifest, ISO install.**
- **Subdomain routing** (`photos.malmo.local`), explicitly *not* path-based — browser same-origin policy is the reason. See `SPEC.md`.
- **Headscale + DERP (BSD-3)** for the mesh. Tailscale's coordinator is proprietary; NetBird's server is AGPLv3. Both rejected.
- **ext4 + LUKS, not ZFS.** ZFS forecloses mergerfs/SnapRAID upgrades and adds CDDL/kernel licensing pain.
- **Mergerfs from day 1** when a data drive is present (pool of one with one drive; `epmfs` placement). Enables zero-downtime drive addition. SnapRAID parity stays deferred.
- **User content at `/home/<user>/`** with macOS-style capitalized use-case folders (`Photos/`, `Music/`, `Movies/`, `Documents/`, `Notes/`, `Downloads/`). Data drive mounts at `/srv/malmo/` with bind mounts to `/home/` and `/var/lib/malmo/`.
- **Files are first-class, apps are windows.** User content lives in use-case folders; app state in `/var/lib/malmo/instances/<id>/`. Uninstalling an app never deletes user content. Manifests bind-mount use-case folders by declaration.
- **SMB shares via Samba** for cross-device access (Windows, macOS, iOS, Android, Linux). mDNS-advertised. TimeMachine-compatible.
- **One malmo password per user, PAM is the source of truth.** Dashboard, SSH, and SMB all authenticate against the same `/etc/shadow` entry. Brain has no password hash; it calls host-agent's `verify_password` on every login. Per-protocol opt-in (SSH and SMB off-by-account-by-default) is done via service allowlists, not separate credentials.
- **SSH and SMB scoped to LAN + mesh via nftables** — RFC1918 + the mesh interface. Public internet blocked structurally, not per-account.
- **Brain is one Go binary in a container**, not microservices. SQLite for malmo's own state; managed Postgres is for *apps*.
- **Closed by default for remote access** — no public exposure toggle in v1; identity-based mesh only.
- **Manifest schema versioned from day one**, public, two-major back-compat. Required fields are minimal (~7-line manifests valid).
- **Permissions are declared and enforced**, not metadata.
- **Dashboard role maps to Linux group membership.** Members unprivileged; admins in `sudo`. UI is the path for every privileged operation; SSH is rescue-only. 5-minute re-prompt window for destructive UI ops. See `USERS_AND_GROUPS.md`.

## Working style

- This is a spec; precision matters. When proposing a change, name the doc and section.
- Push back on tradeoffs; defer to product calls once made (per user preference).
- Open questions are tracked at the bottom of each doc — that's where genuinely unresolved items live. Don't invent answers; surface them.
- Don't add "future-proofing" abstractions to the spec. The docs are already explicit about what's deferred (e.g., fscrypt, ARM, snapshots, paid-app mechanics).
- Keep the "no NAS vocabulary in the UI" rule (`STORAGE.md`) in mind for any user-facing language.
