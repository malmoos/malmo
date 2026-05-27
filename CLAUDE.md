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

**Spec + early implementation.** The markdown design docs remain the source of truth; a walking-skeleton implementation now lives alongside them. The skeleton proves the spine end-to-end (UI → brain → docker compose → Caddy route → SSE → uninstall) and runs natively on a dev box — no VM. The real host-integrated parts (Avahi, LUKS/TPM, boot ordering, auth) are still stubbed or unbuilt.

Implementation layout:
- `cmd/brain/` + `internal/` — `malmo-brain` (Go, huma API + SQLite, `docker compose` CLI driver). Packages: `api`, `lifecycle`, `catalog`, `manifest`, `store`, `caddy`, `hostclient`, `events`, `protocol`, `auth`, `audit`, `admission`.
- `cmd/host-agent/` — **fake** host-agent: real `BRAIN_HOST_PROTOCOL.md` wire format over a real UNIX socket, canned host ops (no Avahi/LUKS/apt yet).
- `web-ui/` — Vue 3 + Vite + TanStack Query dashboard (Tailwind/shadcn-vue deferred; plain CSS for now).
- `catalog/` — hand-written sample manifests (currently `whoami`).
- `dev/` + `Makefile` — dev orchestration: Caddy container + `make` targets. Inner loop is all-native; `make help` lists targets.

When extending the implementation, keep it faithful to the locked specs and update the relevant doc if a decision flips. Do not build out host-integrated subsystems (storage, boot, networking) without the VM outer loop the specs assume.

## Documents

**All spec docs live in `docs/specs/`.** Filenames below (and the bare-filename cross-references throughout these docs) are relative to that directory. Implementation progress lives in `docs/progress/`, developer how-to in `docs/dev/`; `docs/README.md` is the full map.

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
- `DISCOVERY.md` — LAN discovery & mDNS. Avahi as the publisher; per-app A records (`<slug>.malmo.local`) published by the install reconciler via Avahi DBus `EntryGroup.AddAddress`, sibling to Caddy site blocks. LAN-only interface scoping. `.local` works on macOS/iOS/Windows-with-Bonjour/Linux; **Android browsers don't resolve `.local`**, which is the real reason the secure-URL path in `MALMO_NETWORK.md` exists. No wildcards, no CNAME tricks, no private CA for `.local`.
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
- `TELEMETRY.md` — project-side telemetry (data that *leaves the box*). Off by default; single first-run checkbox covers usage + crash streams. One endpoint we control (`telemetry.malmo.network`), no third-party SaaS endpoints. Strict allowlist of fields, rotating weekly install ID, no IP storage (country-bucketed). Backend = PostHog Cloud in v1 (disclosed on first-run; revisit if scale or positioning shifts). Crash *bundles* never auto-upload — only structured `brain_panic` / `host_agent_panic` events with paths scrubbed. Telemetry is halt-fast signal, never a gate.
- `TIME.md` — NTP, timezone, RTC. chrony (not systemd-timesyncd) with NTS-first source list (Cloudflare NTS primary, Debian pool fallback, NETNOD NTS backup); no `time.malmo.network` in v1. RTC in UTC always. System TZ auto-detected at first-run; containers inherit via `/etc/localtime` bind-mount + `TZ` env, manifest can opt out. `clock-not-synced` typed health-issue (warning, gates Let's Encrypt renewal within 7 days of expiry). Dashboard never blocks on NTP — runs degraded.
- `NOTIFICATIONS.md` — the in-product dashboard notification center (the bell). v1 is **dashboard-only — no email, no mobile push, no webhook**; the always-available local floor, off-box transports deferred behind a transport-agnostic seam. Notifications are *derived* from existing events (HEALTH issue raise/clear, UPDATES outcomes, allowlisted LOGGING audit actions), not a new taxonomy. Routing = actionability + ownership: system/box-wide → admins (OS/system updates admin-only); personal/per-instance → owning user (app updates go to the instance owner, never broadcast). Box-wide criticals emit an info-only transparency variant to members. Stored in a mutable, prunable `notifications` table (distinct from append-only `audit_events`); coalesced one-per-raise; all network issues stay off the bell in v1.
- `LOCAL_ANALYTICS.md` — user-facing analytics that *never leave the box*. Two surfaces: (1) per-user historical events + metrics in brain SQLite (app-opens, sign-ins, install/update history, per-app/per-user storage with downsampled retention, drive-fill forecast); (2) real-time system resources (CPU/RAM/network/disk-IO) as on-demand SSE in a top-bar dropdown available to all users. Privacy = per-user-only; admins see aggregate + sign-in history (security). No opt-in — data about your own hardware on your own hardware.
- `THREAT_MODEL.md` — the security lens for the whole spec. Owns **no mitigations** — it indexes what we defend and (loudly) what we don't, pointing to the doc that owns each defense. Root assumption: **household trust model** (members trust each other; adversaries are the network, a compromised app, and a removed drive). Eight trust boundaries, each with a threat/mitigation/residual table; STRIDE used as a checklist, not a matrix. Scope is **v1 closed-by-default** (deferred mesh is explicitly out, gets its own pass). Named non-goals: whole-box theft, member-vs-member-at-root (no fscrypt in v1), malicious author past curation, nation-state, side-channels, supply-chain-as-adversary, DoS, doubly-lost. Living doc — revisited when a boundary moves (mesh/remote access).
- `DECISIONS.md` — evolution-of-thinking log. Captures what we changed our mind about and *why*. Read this before relitigating; add an entry when a load-bearing decision flips.
- `NEXT.md` — prioritized list of open design topics, with pointers back to the relevant doc for context. The "Open questions" section in each doc is now just a pointer to this file; never add open items to individual docs, add them here.

**Rule: this list is the canonical map of the spec.** Whenever a new doc is added to `docs/specs/` (or an existing doc's scope changes materially), update this list in the same change. One-line entry: filename, what it owns, and the headline locked decisions. If you find a doc in `docs/specs/` that isn't listed here, that's a bug — fix it.

When working on a topic, read the relevant doc(s) end-to-end before proposing changes. The docs cross-reference each other heavily and decisions in one constrain the others.

## Documentation discipline

Every change ships with documentation — a code change is not complete until its docs are written in the same change.

- **Three doc homes.** Design source of truth → `docs/specs/`. Implementation progress → `docs/progress/`. Developer how-to (running locally, code-level architecture) → `docs/dev/`.
- **Every unit of work gets a progress entry.** Add a numbered `docs/progress/NNNN-<slug>.md` (ADR-style, sequential) that records **what was done** and **what's next**, following the template in `docs/progress/0001-walking-skeleton.md`. Update the entry's "what's next" as follow-ups land.
- **Keep specs and reality in sync.** When the implementation realizes or diverges from a spec, update the matching `docs/specs/` doc in the same change (and add a `DECISIONS.md` entry if a locked decision flips).
- **The doc map is load-bearing.** Keep `docs/README.md` and `docs/progress/README.md` (the indexes) current in the same change. A doc not linked from the map is a bug.
- **Root `README.md`** is the front door (pitch + quickstart); keep its quickstart accurate when the dev workflow changes.
- **No line wrapping in markdown.** Use continuous lines of text, not ~70 character breaks. Markdown viewers handle reflowing; hard-wrapped lines make diffs harder to read and are unnecessary.

## Go code discipline

Small set of rules. Codified now so we don't have to back them out later.

- **Consumer-side interfaces.** Interfaces live in the package that *uses* them, not the package that implements them. `lifecycle.DockerDriver` lives in `internal/lifecycle/`, not in a hypothetical `internal/docker/`. Provider packages export concrete types only. Exception: a single interface shared by three or more consumers can move to the provider, but default to consumer-side until that's true.
- **Layer boundaries.** `internal/lifecycle` is the transaction owner; only `cmd/brain` and `internal/api` may import it. `internal/store` is the persistence boundary; only `internal/lifecycle`, `internal/api`, and `cmd/brain` may import it. Anything else reaching in is breaking the model — push the call through the right seam instead.
- **`log/slog` is the only logger.** No `"log"` imports, no `fmt.Println` for diagnostics. Structured fields, not interpolated strings: `slog.Info("app installed", "instance_id", id)`, not `slog.Info(fmt.Sprintf("installed %s", id))`. The default handler is set in `cmd/brain/main.go`; use `slog.Default()` (i.e. the package-level functions) — don't thread `*slog.Logger` through constructors.
- **Standard structured fields.** Use these key names so journalctl/jq filters stay stable: `instance_id`, `manifest_id`, `slug`, `service`, `image`, `host`, `upstream`, `step`, `err`, `output`, `user_id`, `username`, `role`, `action`, `actor_user_id`, `target_kind`, `target_id`. Adding a new recurring field? Add it here.
- **Typed errors at boundaries, not everywhere.** Define a sentinel/typed error only when a *consumer* needs to discriminate (HTTP status, retry decision, UI text). `store.ErrNotFound` exists because the API maps it to 404. Don't pre-declare error types speculatively.
- **No premature abstraction.** Don't introduce an interface, factory, or DI container until at least two concrete consumers exist. CLAUDE.md says this generally; it bites hardest in Go where every extra interface is import-graph weight.
- **`internal/` for everything except `cmd/`.** No `pkg/`. Anything inside `internal/` is private to this module by Go's own rules — no public API surface to maintain.
- **Tests in the same package by default.** Use `package foo_test` only when the test genuinely needs to exercise the public surface (catches accidental privacy regressions). Fakes live in `*_test.go` until a second package needs them; then promote to `internal/foo/footest`.
- **Elevation-class mutations audit success *and* failure.** Any handler that creates / deletes / role-changes / password-changes a principal (or installs / uninstalls / changes permissions on an app) emits an `audit.Record(..., success=false)` on every observable failure path — host 502, store 500, conflict 409, guard rejection (last-admin, self-delete) — in addition to the success case. Mirrors `login.failure`; lets the Activity view answer "did someone unauthorized try to mutate X?" symmetrically with login attempts. Pure reads and validation 422s don't audit.
- **Brain commits first, host is reconstructible.** State mutations that span brain SQLite + host-agent commit to brain first, then call host. On host failure, the brain row is rolled back so the two sides stay aligned (`USERS_AND_GROUPS.md` # Roles: "if either side fails, both roll back"). The reverse order leaves orphan host state with no brain row to clean it up from. Established by `/setup`, `createUser`, `updateUserRole`, `deleteUser` — keep the pattern.

## Load-bearing decisions (don't relitigate without cause)

- **Debian base, single-node, BYO x86, Docker apps, custom YAML manifest, ISO install.**
- **Subdomain routing** (`photos.malmo.local`), explicitly *not* path-based — browser same-origin policy is the reason. See `SPEC.md`.
- **Headscale + DERP (BSD-3)** for the mesh. Tailscale's coordinator is proprietary; NetBird's server is AGPLv3. Both rejected.
- **ext4 + LUKS, not ZFS.** ZFS forecloses mergerfs/SnapRAID upgrades and adds CDDL/kernel licensing pain.
- **Mergerfs from day 1** when a data drive is present (pool of one with one drive; `epmfs` placement). Enables zero-downtime drive addition. SnapRAID parity stays deferred.
- **User content at `/home/<user>/`** with macOS-style capitalized use-case folders (`Photos/`, `Music/`, `Movies/`, `Documents/`, `Notes/`, `Downloads/`). Data drive mounts at `/srv/malmo/` with bind mounts to `/home/` and `/var/lib/malmo/`.
- **Files are first-class, apps are windows.** User content lives in use-case folders; app state in `/var/lib/malmo/instances/<id>/`. Uninstalling an app never deletes user content. Manifests bind-mount use-case folders by declaration.
- **SMB shares via Samba** for cross-device access (Windows, macOS, iOS, Android, Linux). mDNS-advertised. TimeMachine-compatible.
- **Avahi as the LAN publisher; per-app `.local` records owned by the reconciler.** No mDNS wildcards exist; each app slug is a real announced name, published by host-agent via Avahi DBus `EntryGroup.AddAddress` alongside the Caddy site block. LAN interfaces only (not mesh, not Docker bridges). `.local` is HTTP-only by definition (no public DNS → no Let's Encrypt) and Android browsers don't resolve it — secure URLs are the compatibility path. See `DISCOVERY.md`.
- **One malmo password per user, PAM is the source of truth.** Dashboard, SSH, and SMB all authenticate against the same `/etc/shadow` entry. Brain has no password hash; it calls host-agent's `verify_password` on every login. Per-protocol opt-in (SSH and SMB off-by-account-by-default) is done via service allowlists, not separate credentials.
- **SSH and SMB scoped to LAN + mesh via nftables** — RFC1918 + the mesh interface. Public internet blocked structurally, not per-account.
- **NetworkManager owns every network interface** (ethernet, WiFi, future bridges/VPN); not systemd-networkd. WiFi is first-class in first-run because the "old laptop in the pantry" install includes WiFi-only machines. host-agent drives NM over DBus; the primary connection is the one with `connection.required-for-network-online=true`. See `BOOT.md`, `BRAIN_HOST_PROTOCOL.md`, `FIRST_RUN.md`.
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
