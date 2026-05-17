# malmo — What's Next

> The single, prioritized list of design topics we still need to cover. Replaces the "Open questions" tail of every other doc. Companion to `DECISIONS.md` (what we figured out, and why).

## How to use this doc

- **Tier 1** = blocks other design work or implementation. Tackle these first.
- **Tier 2** = shapes UX or developer surface; not strictly blocking but compounds if delayed.
- **Tier 3** = can be deferred without retrofitting risk, but pinning the shape now is cheap insurance.
- **Tier 4** = bikesheds, point-decisions, or small open items. Park here so they aren't forgotten; pull into a higher tier when they bite.

Each entry: one-sentence shape, the doc it touches, and *why this tier*. The doc is the source of context — read it before opening the topic.

When a topic is **decided**, remove its entry here and add the rationale to `DECISIONS.md` (if it flipped a position) or just lock it in the relevant doc.

---

## Tier 1 — Blocking

### Telemetry / crash-reporting / phone-home posture

Do we collect anything from boxes — install success, app-catalog hit counts, crash dumps, none? Opt-in vs. opt-out, what's collected, retention, where it's published. Must land before the first public ISO ships: users who installed under "we collect nothing" feel betrayed when that changes, and the **first-run wizard needs the toggle (or its absence) baked in**. Touches the privacy story across the whole product.

**Context:** `FIRST_RUN.md`, `SPEC.md` (positioning), `MALMO_NETWORK.md` (cloud surface).
**Why Tier 1:** retrofit landmine — every shipped box without an opt-in screen forecloses adding one later without trust damage. Also a marketing/positioning decision against Umbrel (opt-in telemetry) and Yunohost (none).

### Time / timezone / NTP

Boring but load-bearing: Let's Encrypt fails on clock skew (`MALMO_NETWORK.md`), audit-event ordering needs monotonic time (`LOGGING.md`), cron-based maintenance windows (`UPDATES.md`) depend on TZ, future TOTP would too. First-run needs to set timezone; NTP source needs a default (Debian pool / malmo-operated `time.malmo.network` / user-configurable). Not specced anywhere today.

**Context:** `FIRST_RUN.md`, `MALMO_NETWORK.md`, `UPDATES.md`, `LOGGING.md`.
**Why Tier 1:** blocks first-run wizard finalization and the certificate-renewal happy path.

### Wifi + first-run network setup

The "old laptop in the pantry" case includes wifi-only machines with no ethernet jack. The live installer needs a wifi step; the dashboard needs a network panel (switch SSID, static IP vs. DHCP, see which NIC is active, multi-NIC). FIRST_RUN.md assumes the box comes up on the LAN — but it might not.

**Context:** `FIRST_RUN.md`, `BUILD.md` (installer).
**Why Tier 1:** blocks the target-audience install path. Tinkerers will ethernet-tether; the long-term audience won't.

### `malmo.local` / mDNS discoverability

We've assumed `http://malmo.local` Just Works. It doesn't on networks that block mDNS (some corporate, some ISP routers, Android pre-12 quirks). Also: multi-box on one LAN (`malmo-2.local`?), what URL the user actually types (vs. the `<box-id>.malmo.network` once `MALMO_NETWORK` is enrolled), captive-portal-style "we can't find your box" fallback. This is the first interaction with the product.

**Context:** `SPEC.md` # Local access, `MALMO_NETWORK.md`, `FIRST_RUN.md`.
**Why Tier 1:** first-touch UX. If the user can't reach the dashboard after install, nothing else matters.

---

## Tier 2 — UX-shaping

### Notifications & alerts (`NOTIFICATIONS.md`)

`HEALTH.md` and `UPDATES.md` assume the user *sees* events on the dashboard. They don't — boxes run unattended in a pantry. How does the user find out the drive is degraded, a login happened from a new device, an update applied overnight, the box rebooted? Decisions needed: transports (email-only v1? push via FCM/APNs later? in-product notification center?), per-event severity, user-vs-admin scoping, snooze, daily-digest. Deserves its own doc.

**Context:** `HEALTH.md`, `UPDATES.md`, `AUTH.md` (new-device login), `LOGGING.md` (audit events → notifications overlap), `MALMO_NETWORK.md` (email transport — own SMTP vs. relay).
**Why Tier 2:** HEALTH and UPDATES are half-features without it. A degraded-mode banner only the dashboard shows misses the user who's two rooms away. Email-on-file (Tier 2) is the prerequisite transport.
**Prior art:** Synology DSM's push/email/SMS notification center is the closest model; TrueNAS uses Apprise for a wide transport set; Umbrel has an in-product notification center only. Our shape is probably Synology-lite (email + in-product) for v1.

### UPS, clean shutdown, power-event handling

The box is plugged into a wall and lives 24/7 in a pantry. Power blip → unclean LUKS shutdown → degraded mode at boot. v1 minimum: graceful shutdown on power-button press, a "Shut down / Restart" affordance in the dashboard, and (if a USB UPS is attached) low-battery hook via NUT. Affects `BOOT.md` (clean-shutdown path), `HEALTH.md` (a "running on battery" issue type), and the install/wizard story (do we detect a UPS at first run?).

**Context:** `BOOT.md`, `HEALTH.md`, `FIRST_RUN.md`, `AUTH.md` (who can shut down the box from the UI).
**Why Tier 2:** Synology/TrueNAS treat this as table stakes. Most boxes will never see a UPS, but "shut down from the dashboard" and "don't corrupt on power blip" are universal.

### App resource limits (manifest field)

One runaway indexing job shouldn't OOM the box or starve the brain. Docker has `mem_limit`, `cpus`, `pids_limit`, `blkio` — we need a `resources:` block in the manifest with sane defaults (so authors can skip it), store-policy guidance on when authors *should* set it, and a dashboard view of per-app usage so users can see who the offender is. Cheap to add now; breaking change to add after the catalog grows.

**Context:** `APP_MANIFEST.md`, `APP_ISOLATION.md`, `APP_STORE.md` (curation policy — when must this be set).
**Why Tier 2:** schema-shaping; cleanest decided before the catalog is real.

### Threat model document

`AUTH.md`, `APP_ISOLATION.md`, `STORAGE.md`, `MALMO_NETWORK.md` all implicitly defend against an unwritten model. Writing it down once — "LAN attacker present; compromised app possible; physical access to a removed drive defended by LUKS; full-box physical attacker out of scope beyond at-rest encryption; malicious app authors filtered by curation, not by sandbox-proof isolation" — gives us a checking framework and a place to point when arguing edge cases. Doesn't change any decisions; clarifies all of them.

**Context:** `AUTH.md`, `APP_ISOLATION.md`, `STORAGE.md`, `MALMO_NETWORK.md`, `USERS_AND_GROUPS.md`.
**Why Tier 2:** unblocks future security arguments rather than enabling a feature. Cheap to write, expensive to keep relitigating without.

### i18n posture (v1 + schema door)

Two-part decision: (a) is v1 English-only? (very probably yes); (b) does the manifest schema *allow* localized fields (`name`, `description`, `category`) so a future translation pass doesn't require a breaking schema bump? Yunohost is fully multilingual and we'll get the ask. Decide the door now; defer the translations.

**Context:** `APP_MANIFEST.md`, `APP_STORE.md`, `WEB_UI.md`.
**Why Tier 2:** schema-shaping; literally one or two field-shape decisions in `APP_MANIFEST.md`.

### Custom container (Door 2) install flow

The actual paste-compose UX. Field-by-field interaction, main-port inference, what we ask vs. autodetect, name collisions, edit-after-install path.

**Context:** `APP_MANIFEST.md` ("Custom container — synthetic manifest").
**Why Tier 2:** Door 2 is the bridge to the "tinkerer adoption" audience. The synthetic-manifest mechanic is sketched; the UX isn't.

### Dashboard at first arrival

Empty, "get started" suggestions, or a starter bundle. Tradeoffs: friction vs. opinionated push vs. discovery.

**Context:** `FIRST_RUN.md` (Phase 3).
**Why Tier 2:** first-impression UX; gates the wizard-to-steady-state hand-off.

### Store catalog curation policy

`APP_STORE.md` pins the publish *mechanism* (signed catalog, PR-based, CI-validated). What's still open is the **content policy** the maintainer enforces in review: do we reject manifests that set `storage.app_managed_user_content: true`, or only label them with the absence of the `files_first_class` badge? Do we require apps to log to stdout/stderr (no `logging.driver:` overrides, no in-`command:` file redirects) so the dashboard Logs tab works (`LOGGING.md` # Apps are expected to log to stdout)? What other criteria gate inclusion (license, upstream maintenance signals, declared-vs-actual permission audit)?

**Context:** `APP_STORE.md` # v1 catalog is hand-curated by malmo, `APP_MANIFEST.md` # External-storage convention, `STORAGE.md` # Files are first-class, `LOGGING.md`.
**Why Tier 2:** the "files are first-class" principle only holds if curation defends it. Decided lazily, the catalog fills with opaque-library apps and the principle erodes. The mechanism is in place; the bar isn't written down.
**Prior art:** Yunohost's app integration-level (0–9) rating is a useful model for surfacing curation outcomes as a small badge set (e.g. `Files-first-class`, `Backup-aware`, `Multi-instance`, `Stdout-clean`) — a single number is too coarse, but the underlying idea of "show users the integration-quality signal without exposing manifest internals" maps well.

### Shared folder management UX

Who can add/rename subfolders under `~/Shared/`? How does an admin see what's in there vs. per-user? Does each user get a per-user view filtered to "things I have access to," or a flat view? Group management for `malmo-shared` (kick a user off shared content) — Settings surface.

**Context:** `STORAGE.md` # Permissions, `AUTH.md`.
**Why Tier 2:** the on-disk mechanics are pinned; the dashboard UX isn't. Households without this can still use Shared/ via SMB, but the in-dashboard view needs design before public release.

### Email-on-file for users

Required for password recovery, product comms, and any future cloud-account linking. Currently sidestepped because all three are deferred — but the *decision* of whether to collect at first-run shapes the wizard.

**Context:** `FIRST_RUN.md`.
**Why Tier 2:** decided now or it becomes a forced retrofit later. Likely answer: optional field at user creation, used for recovery only.

### OpenAPI codegen timing for the brain API

The brain↔UI API is hand-rolled Go ↔ TS types in v1 (`DECISIONS.md` 2026-05-14, brain↔UI API). The OpenAPI 3 spec + generated TS client lands later. Open: when — before the public store API ships, after the first external integrator asks, or on a fixed schedule? Generator choice (`oapi-codegen` for Go server, `openapi-typescript` for TS client) is straightforward; timing is the call.

**Context:** `BRAIN_UI_PROTOCOL.md` # "API discipline."
**Why Tier 2:** every week we ship without it, drift between hand-rolled types grows. Cheap insurance if we pin a trigger.

### Rate-limit / abuse posture for the public API

The brain↔UI API is public-callable from day one (third-party stores, CLI, external tools — `DECISIONS.md` 2026-05-14). v1 has no rate-limiting story. Open: per-session limits, per-IP for unauthenticated routes, separate budget for SSE stream count vs. request rate, what 429 messaging looks like.

**Context:** `BRAIN_UI_PROTOCOL.md`, `AUTH.md`.
**Why Tier 2:** needs to land before third-party stores can ship; not blocking v1.

---

## Tier 3 — Defer-able, but pin the shape

### OS major-version upgrade commitment

`UPDATES.md` covers the five streams under one Debian release. What about Debian 12 → 13? Options: in-place `do-release-upgrade` (Debian's blessed path, sometimes brittle), image-based A/B (HexOS / ChromeOS shape — clean rollback, doubles OS-drive footprint), or "reinstall + import data" (cheap to ship, terrible UX). The *commitment* (will we ever expect users to reinstall to get a new Debian major?) is a position to take now; the mechanism can wait.

**Context:** `UPDATES.md`, `STORAGE.md` (system dataset vs. data drive split makes image-based A/B more feasible), `BUILD.md`.
**Why Tier 3:** doesn't bite until Debian cuts the next stable (~2027). Pin the commitment now so design choices don't accidentally foreclose A/B.

### `malmoctl` — on-box CLI

A `malmoctl` for admins on the host: rescue operations, scripting hooks, listing apps, tailing logs, triggering updates. Today the host story is "SSH in and... do what?" — there are no commands beyond raw Docker. Either we ship one CLI that wraps the brain↔host-agent surface, or we declare that there is none and SSH is bash + journalctl + docker.

**Context:** `AUTH.md` (SSH as rescue), `BRAIN_HOST_PROTOCOL.md`, `CONTROL_PLANE.md`.
**Why Tier 3:** tinkerers will ask for it on day one; nothing on the v1 critical path depends on it.

### API tokens vs. cookie-only auth

Third-party stores, external tooling, and (later) `malmoctl` need non-interactive auth. `AUTH.md` ships cookie-only in v1. Open: do we add long-lived API tokens (user-scoped, listable, revocable in Settings), or push everything through a service-account model? Affects `BRAIN_UI_PROTOCOL.md` (header vs. cookie auth path) and the rate-limit posture (Tier 2).

**Context:** `AUTH.md`, `BRAIN_UI_PROTOCOL.md`, `APP_STORE.md` (third-party stores).
**Why Tier 3:** v1 ships without it; pin the shape before the first third-party integrator forces an ad-hoc answer.

### Permission revocation after install

A user granted Immich access to `~/Photos/`. Six months later they want to revoke it without uninstalling. Today the manifest declares permissions at install; nothing covers the *change* path. UX: per-app permissions screen mirroring iOS/Android's, with consequences spelled out ("Immich will no longer be able to see your photos"). Brain side: re-render compose, restart instance, audit-event.

**Context:** `APP_MANIFEST.md` (`permissions.user_folders`), `APP_ISOLATION.md`, `APP_LIFECYCLE.md`, `LOGGING.md` (audit).
**Why Tier 3:** install-time grant works for v1; revocation is the second-order feature users will ask for once they've lived with the box for a while.

### Hostname / box-name rename

Can the user change the box's display name after first-run? Cascades into mDNS hostname (`malmo.local` → `kitchen.local`?), Let's Encrypt cert SANs on the `MALMO_NETWORK` side, audit log historical naming, SMB advertisement. Easy to forbid; easy to allow with caveats; expensive to retrofit if we wire the name into too many places.

**Context:** `FIRST_RUN.md`, `MALMO_NETWORK.md`, `STORAGE.md` (Samba), `LOGGING.md`.
**Why Tier 3:** not a v1 feature; pin the architectural separation between "box-id" (stable) and "display name" (mutable) before too much code depends on conflating them.

### Documentation surface

Where do user docs and app-author docs live? In-product help drawer, `docs.malmo.network` (separate Mkdocs/Astro site), `README` files in the catalog repo, all of the above? Yunohost has extensive in-product help; Umbrel docs live on a separate site; TrueNAS has both. Affects the dashboard codebase (`WEB_UI.md`) and the catalog repo layout (`APP_STORE.md`).

**Context:** `WEB_UI.md`, `APP_STORE.md`, `SPEC.md`.
**Why Tier 3:** can ship v1 with a thin docs site and add in-product help later, but the *split* between the two needs deciding before either is written at scale.

### Backup architecture shape

Off-site backup is paid + post-MVP, but the *interfaces* — manifest hints (data vs. cache volumes), restore path, bind-mount-only constraint, managed-service dump path — should be sketched now to avoid retrofitting once we ship.

**Context:** `APP_MANIFEST.md` (`storage.data_volumes` / `cache_volumes`), `SERVICE_PROVISIONING.md` (managed-service backups), `APP_LIFECYCLE.md`.
**Prior art:** Yunohost ships per-app `backup`/`restore` scripts in each package — a useful reference for how the manifest declares "what's data vs. what's reconstructable" and where app-author logic lives in the dump/restore path.

### Display-name rename UX + audit log story

Slug is stable; the rename mechanic is straightforward but "who is `cindy` if she renamed to `cynthia`?" needs design across audit log, sharing, and any future identity-bearing UI.

**Context:** `FIRST_RUN.md` ("Identity & display names").

### App-facing background-job service (Tier-1)

A managed queue + worker that apps can fire background work into (overnight re-encoding, ML indexing, etc.). Apps declare `services.jobs: { type: malmo-jobs }` in the manifest; brain provisions credentials + queue URL. Probable implementation: Redis Streams or NATS JetStream as the queue, a malmo-managed worker pool runs jobs during a configured idle window.

**Context:** `SERVICE_PROVISIONING.md` (Tier-1 catalog — would extend it), `APP_MANIFEST.md` (`services:` block).
**Why Tier 3:** completely separate from brain↔host-agent jobs (which are OS-level). This is an app-platform feature; the bet is that "apps can offload async work to malmo" is a real differentiator vs. Umbrel/CasaOS. Pin the shape now so we don't accidentally make decisions in `SERVICE_PROVISIONING.md` that close it off; full design post-MVP.

### Web terminal in the dashboard

`SPEC.md` # Local access promises a "virtual terminal in the web UI" — a shell without leaving the browser. Needs design across:
- Protocol: WebSocket over the brain↔host-agent UNIX socket via HTTP upgrade (already locked in `BRAIN_HOST_PROTOCOL.md` Pattern D as the future shape).
- Auth: root PTY = root on the host. Default to dropping to the dashboard-user's Linux account; explicit "open a root shell" gesture gated by re-typing the dashboard password.
- UX: where it lives in the dashboard, history persistence, multi-session behavior.

**Context:** `BRAIN_HOST_PROTOCOL.md`, `AUTH.md`, `SPEC.md`.
**Why Tier 3:** load-bearing affordance for tinkerers, but not on the v1 critical path. Pinning shape now keeps the protocol's WebSocket reservation honest.

### Hooks — concrete shape for return

Decided in principle: when hooks return, they're **one-shot container images**, not in-container scripts (`DECISIONS.md` 2026-05-13). The concrete schema, timeout/failure handling, log surfacing, and brain-side execution model aren't specced.

**Context:** `APP_MANIFEST.md` # F, `APP_LIFECYCLE.md` # "Deferred: lifecycle hooks".

### Cert-expired UX

When a box has been offline long enough that `.malmo.network` certs expired: serve the expired cert with browser warning, transparently redirect to `.local`, or surface a banner in the dashboard.

**Context:** `MALMO_NETWORK.md` ("Failure modes").

### Phased rollout / cohort + beta channel activation

Both deferred from v1 (`DECISIONS.md` 2026-05-15). Shape is pinned in `RELEASE_MANIFEST.md` # Future work + # Channels — schema is additive, hash formula is `hash(machine_id || canonical(brain, ui))`, beta is a sibling `beta.json` file. What's still open: the **trigger conditions** in concrete terms (what fleet-size threshold, what auto-apply milestone, what bad-release detection latency forces our hand). Pre-decide so we don't dither when one of them fires.

**Context:** `RELEASE_MANIFEST.md`, `UPDATES.md` # 3.

### Settings → Storage UX (Level-1 walk-through, design pass)

The architecture and the install/wizard/add-drive/eject mechanics are locked (`STORAGE.md`, `FIRST_RUN.md`, `AUTH.md`, `BRAIN_HOST_PROTOCOL.md`, `HEALTH.md` # `disk-full`). What remains is design-time copy + screen-layout: card shape for OS drive vs. data drive at Level 0/1, where the "Show recovery passphrase" affordance lives under Advanced, eject-drive confirmation copy, disk-pressure banner copy + top-space-hogs enumeration, single-drive "add a data drive later" dashboard hint, and the file-access permission block on the app-install dialog ("Photos will read and write your Photos folder").

**Context:** `STORAGE.md`, `FIRST_RUN.md`, `HEALTH.md`, `APP_MANIFEST.md` # `permissions.user_folders`.
**Why Tier 3:** doesn't block bring-up — the brain endpoints and health-issue flags exist. UX iteration belongs with the designer and the first user-test pass, not the spec.

### Reboot scheduling UX

"Reboot tonight at 3am OK?" prompt vs. silent within window. Surface only when blocked vs. always.

**Context:** `UPDATES.md`.

### Member-visible Tier-1 app logs (manifest opt-in)

`LOGGING.md` defaults Tier-1 shared app logs to admin-only — stdout commonly leaks per-user behavioral signal (request paths, search queries). For apps whose stdout is genuinely uninteresting (a periodic sync daemon, an indexer), a manifest field `logs.member_visible: true` would let authors opt the app's logs into the per-member Logs tab. Default off; off remains the safe choice for the bulk of the catalog.

**Context:** `LOGGING.md` # Per-app logs, `APP_MANIFEST.md`.
**Why Tier 3:** doesn't block v1; the day a member can't debug a shared-app issue without flagging down an admin, this lands.

### Audit-log schema details

`LOGGING.md` pins the `audit_events` table shape and write path. Open: the controlled vocabulary of `action` strings (full enum, grouped by domain — auth.*, users.*, apps.*, shares.*, tier2.*), the typed metadata schema per action type (free-form JSON works for v1 but becomes a UI rendering pain at scale), and whether to add a hash-chain / sequence-number integrity guarantee on top of the append-only invariant. None block v1; all become tech debt if we leave them.

**Context:** `LOGGING.md`, `AUTH.md`, `APP_LIFECYCLE.md`.
**Why Tier 3:** the UI works against free-form JSON in v1, but every new event type adds rendering work. Pin the schema before the catalog grows.

### Recovery dashboard spec (`RECOVERY.md`)

`malmo-recovery.target` shrunk to two triggers in `DECISIONS.md` 2026-05-16: **TPM2 unseal failure on the root drive** (LUKS recovery passphrase at console — needs a printed-on-the-box / wizard-shown story), and **host-agent crashloop past `StartLimitBurst`** (static page on port 80 with one-click "roll back host-agent"). The page's actual content + UX, the rollback mechanism, and the mDNS discoverability story (`malmo-recovery.local`) are not specced. Most failure modes that the old strict-gate model routed here now flow through degraded mode (`HEALTH.md`) — recovery is now an honestly small surface.

**Context:** `BOOT.md` # Failure → recovery target — the narrow cases, `HEALTH.md` (the rest of what used to live here), `STORAGE.md` # Encryption posture, `AUTH.md` (recovery code vs. LUKS recovery passphrase distinction).
**Why Tier 3:** doesn't block v1 happy-path development; bites the moment a user hits one of the two genuinely-unrecoverable-from-UI cases. Pin the shape before public release.

### fscrypt rollout plan

Per-user encryption is deferred but on the roadmap. Key-loading model (Model A vs. B in `STORAGE.md`), interaction with background app work (`APP_ISOLATION.md`), password-recovery escape hatch.

**Context:** `STORAGE.md`, `APP_ISOLATION.md`.

---

## Tier 4 — Smaller open items

Loose ends. Each is parked until it bites or a higher-tier topic pulls it in.

**Manifests & catalog**
- Exact `MALMO_SERVICE_*` variable schema per service type — `APP_MANIFEST.md`, `SERVICE_PROVISIONING.md`.
- `permissions.devices` syntax — paths vs. categories (`gpu`, `webcam`). `APP_MANIFEST.md`.
- Manifest signing / provenance for third-party stores. `APP_MANIFEST.md`.
- App icon & screenshot handling — bundled vs. URL. `APP_MANIFEST.md`.
- Update-strategy declarations (in-place vs. needs-migration). `APP_MANIFEST.md` (folds into hooks).
- Typed install-time questions in the manifest (prior art: Yunohost's pre-install question schema — typed prompts for admin/domain/language captured at install time). We have nothing today; revisit when Door 2 / managed-config grows beyond env-var passthrough. `APP_MANIFEST.md`.
- App categories / tags taxonomy for the store browse UX. `APP_STORE.md`, `WEB_UI.md`.
- Per-app cron / scheduled tasks declared in manifest (distinct from the Tier-3 background-jobs service in `SERVICE_PROVISIONING.md`). Cron-on-host vs. a per-instance scheduler container. `APP_MANIFEST.md`.
- Per-app kill switch in `catalog.json` (distinct from `RELEASE_MANIFEST.md`'s `rollback_to`, which targets brain/UI versions). For "CVE dropped in app X, stop it everywhere on next catalog refresh." `APP_STORE.md`, `APP_LIFECYCLE.md`.
- Catalog removal / delisted-app behavior — installed instances when an app is pulled (keep-running-with-warning vs. force-uninstall vs. read-only). `APP_STORE.md`, `APP_LIFECYCLE.md`.
- App dependency model — pin "no" (managed services are the answer) so authors don't build the assumption. `APP_MANIFEST.md`.
- User-driven multi-instance of the same app (distinct from Tier-3 per-user instances) — "two Nextclouds for testing." Pin "no" or design. `APP_LIFECYCLE.md`, `APP_MANIFEST.md`.
- App publisher identity / verified-author badge surface (the *mechanism* folds into manifest signing above; this is the catalog-side UX). `APP_STORE.md`.
- Per-app HTTP health-probe declaration in the manifest (beyond Docker `HEALTHCHECK`), so the brain reports "responding" vs. "up but unresponsive." `APP_MANIFEST.md`, `HEALTH.md`.
- Container vulnerability scanning at catalog publish (Trivy/Grype in CI on every PR). `APP_STORE.md`.

**Networking & cloud**
- `box-id` allocation scheme — word-pair vs. random hex + check digit. `MALMO_NETWORK.md`.
- DNS provider for the apex — Cloudflare free tier vs. self-hosted PowerDNS. `MALMO_NETWORK.md`.
- ACME DNS-01 plugin path — Caddy generic vs. malmo-specific plugin. `MALMO_NETWORK.md`.
- Privacy doc surface — what we log (DNS queries, enrollment metadata) and retention. `MALMO_NETWORK.md`.
- mDNS service-advertisement set — `_smb._tcp`, `_adisk._tcp` (Time Machine), `_http._tcp`, anything else (Bonjour-discoverable printer / DLNA?). `SPEC.md`, `STORAGE.md` (Samba).
- Custom domain on the LAN — user owns `home.example.com` and wants the dashboard there. Caddy + ACME DNS-01 with their provider, or accept-cert-warning. `MALMO_NETWORK.md`.
- Local DNS resolver shape — host runs dnsmasq (container resolution + free Pi-hole-shape ad-blocking as a side effect) vs. pure systemd-resolved. `APP_ISOLATION.md`, `MALMO_NETWORK.md`.
- UPnP / port-forwarding stance — closed-by-default implies "no"; state it explicitly so a future "convenience" PR doesn't sleepwalk into it. `MALMO_NETWORK.md`, `SPEC.md`.
- `status.malmo.network` outage-comms surface — boxes show a banner from a cached status JSON when cloud is down. `MALMO_NETWORK.md`.
- Anti-clone check at enrollment — two boxes with the same `box-id` (cloned ISO) must not both enroll. `MALMO_NETWORK.md`.

**Isolation & runtime**
- GPU sharing across apps (MIG / time-slice / exclusive). `APP_ISOLATION.md`.
- macvlan on bonded / bridged host interfaces. `APP_ISOLATION.md`.
- Read-only root rollout as a catalog requirement. `APP_ISOLATION.md`.
- Egress allowlist for `internet: true`. `APP_ISOLATION.md`.
- Per-app firewall rules (apps as L4 endpoints). `APP_ISOLATION.md`.
- Tier-3 per-user app access to household-shared content (`/srv/malmo/shared/`). Tier-1 apps reach it via `shared_folders`; per-user instances do not at MVP. `APP_ISOLATION.md`, `APP_MANIFEST.md`.
- fscrypt coverage for per-user app state under `/var/lib/malmo/instances/<id>/`. When per-home fscrypt lands, does it extend to managed-service data (per-user Postgres, etc.)? `APP_ISOLATION.md` # Managed services placement, `STORAGE.md` # Future: per-user encryption.

**Storage & first-run**
- UTF-8 filename normalization (NFC vs. NFD) across SMB clients — macOS uses NFD on the wire, Linux stores bytes verbatim; "files-first-class" makes this user-visible. `STORAGE.md`.
- Data-import flows — bulk-copy from USB stick / network share into `~/Photos/` via the dashboard, or "just use SMB" as the only path. `STORAGE.md`, `FIRST_RUN.md`.
- Boot-PIN high-security mode. `STORAGE.md`.
- Stronger TPM2 sealing policy (PCR 7+11 with signed PCR policy, or PCR 7+14). v1 seals against PCR 7 only — works across kernel updates, weaker against an attacker who can subvert Secure Boot. Upgrade is non-destructive (additional LUKS slot, re-enroll). `STORAGE.md`, `BOOT.md`.
- Recovery-passphrase storage assistance ("email this to me", USB shard). `STORAGE.md`, `FIRST_RUN.md`.
- Removable drives auto-mount UX. `STORAGE.md`.
- Filesystem on extra drives (ext4 vs. accept existing NTFS/exFAT). `STORAGE.md`.
- OS-drive-only swap with data drive intact. `STORAGE.md`.
- TPM-less hardware fallback. `FIRST_RUN.md`.
- First-run on a box with pre-existing malmo data. `FIRST_RUN.md`.

**Users & groups**
- TPM-fail-and-admin-forgot-password rescue path. With `PermitRootLogin no` and no console root password, the LUKS recovery passphrase boots the box but leaves no clear next step. `USERS_AND_GROUPS.md` # Known gaps, `STORAGE.md`.
- Demotion doesn't kill live `sudo` capability — existing SSH sessions retain group membership until logout. Acceptable for the household trust model; revisit if threat model changes. `USERS_AND_GROUPS.md` # Known gaps.
- `malmo-shared` membership management UI — how an admin removes a user from household-shared content without deleting the account. Folds into shared-folder management UX (Tier 2). `USERS_AND_GROUPS.md`, `STORAGE.md`.
- Account deletion flow — what happens to `/home/<user>/`, per-user Tier-3 instances, audit-event rows that reference the user (tombstone vs. purge). `USERS_AND_GROUPS.md`, `AUTH.md`, `LOGGING.md`.
- Account suspension — disable login without deleting data (kid grounded, ex-roommate archived). `AUTH.md`, `USERS_AND_GROUPS.md`.
- Multi-admin invitation flow — UI affordance for "make a second admin." Today implicit (admin creates a member then promotes them). `AUTH.md`, `USERS_AND_GROUPS.md`.
- Dashboard login brute-force throttling / lockout — `LOGGING.md` notes journald caps sshd spam, but the brain's own login endpoint has no rate-limit story. `AUTH.md`.

**Runtime & host**
- Container image / layer cleanup policy — `docker image prune` cadence + retention so old images don't fill the OS drive over time. `APP_LIFECYCLE.md`, `UPDATES.md`.
- Container runtime version pinning — which Docker engine version we ship, how it tracks Debian-base updates vs. upstream `docker-ce`. `BUILD.md`, `UPDATES.md`.
- Crash-dump / coredump capture policy — host kernel panics, brain process crashes; what we keep, where, retention. Pairs with the Tier-1 telemetry decision. `LOGGING.md`, `HEALTH.md`.
- Log rotation for non-journald files (Caddy access logs, anything that escapes the journal). `LOGGING.md`.

**Observability (user-facing)**
- Per-app disk usage display ("Immich is using 240 GB"). Pairs with HEALTH `disk-full`. `HEALTH.md`, `WEB_UI.md`.
- Resource graphs — last-24h CPU/RAM/disk-IO. Yes / no / lite position. `WEB_UI.md`, `HEALTH.md`.
- "What's eating my disk" explorer — top-N folders/apps under `/srv/malmo`. Folds into Settings → Storage UX (Tier 3). `STORAGE.md`, `WEB_UI.md`.

**Backup & migration**
- On-box / local backup to external USB drive — pre-cloud, v1-shaped: snapshot scheduling, retention, restore UI. Distinct from the off-site architecture entry above. `STORAGE.md`, `APP_LIFECYCLE.md`.
- Cross-box migration — "I bought a new laptop, move my stuff." Same plumbing as restore-from-backup but a different flow (pair source + destination, switch identity). `STORAGE.md`, `MALMO_NETWORK.md`.
- Backup verification / restore-test cadence — untested backups aren't backups. `STORAGE.md`.

**Developer / app-author surface**
- Local lint / test tool (`malmo manifest lint`, `malmo install --local`) — authors today can't validate a manifest without making a PR. `APP_MANIFEST.md`, `APP_STORE.md`.
- Catalog PR template + author-facing docs surface (subset of the Tier-3 "Documentation surface" entry). `APP_STORE.md`.
- Manifest changelog discipline — when schema v1 → v2 ships, how authors find out. Revisit once we have a `v2` candidate. `APP_MANIFEST.md`.

**Web UI**
- Browser support matrix — minimum Chromium/Firefox/Safari versions, mobile-browser commitment level (works / works well / PWA-grade). `WEB_UI.md`.
- Accessibility target — WCAG AA, keyboard navigation, screen-reader pass, reduced-motion. State the position so it's in the build pipeline from day one, not bolted on. `WEB_UI.md`.

**Updates**
- Critical-security flag override for auto-update-off apps. `UPDATES.md`.
- Metered-connection mode for app updates. `UPDATES.md`.
- Concurrent emergency updates across streams. `UPDATES.md`.
- Per-region / per-cohort rollouts. `UPDATES.md`.
- Concrete "stable" promotion criteria. `UPDATES.md`.
- CI signature-verification check on every `releases` PR (covered in `RELEASE_MANIFEST.md` # Promotion; tracked here so the implementation isn't forgotten when CI is stood up). `RELEASE_MANIFEST.md`.
- Signing-key custody + rotation runbook (deferred per `RELEASE_MANIFEST.md` # Signing — "until we have a release to sign"). `RELEASE_MANIFEST.md`, `BUILD.md`.

**Services**
- Per-app DB resource quotas. `SERVICE_PROVISIONING.md`.
- Backup frequency / retention defaults for managed-service dumps. `SERVICE_PROVISIONING.md`.
- Restore-to-different-version semantics for managed services. `SERVICE_PROVISIONING.md`.
- Whether DLNA stays in v1 or gets cut. `SERVICE_PROVISIONING.md`.

**Control plane**
- Per-user instance hostname strategy — `<slug>-<user>.malmo.local` vs. `<user>.<slug>.malmo.local`. `APP_LIFECYCLE.md`.
- Re-import path for archived ("keep data") instances after uninstall. `APP_LIFECYCLE.md`.

**Build & distribution**
- Signing infrastructure for apt repo, registry images, ISO. `BUILD.md`.
- ISO size budget. `BUILD.md`.
- Installer shares code with `malmo-brain` vs. clean-sheet. `BUILD.md`.
- Kiosk-installer failure-mode UX ("stuck at 73%"). `BUILD.md`.
- Hardware-compatibility list process. `BUILD.md`.

**Testing**
- Boot-test assertion harness language — Go (matches the codebase) vs. Python (richer QEMU/swtpm tooling). Either works; pick before the harness is built. `TESTING.md`.
- `live-build` vs. `mkosi` revisit weighted by test-story. `mkosi`'s `mkosi qemu` integration materially improves the medium/slow test lanes; if the build choice is being relitigated, this is a non-trivial weight. `BUILD.md`, `TESTING.md`.

**Health**
- v1 health-check enumeration — the typed issue set in `HEALTH.md` is locked, but the concrete *list* of checks (disk SMART, journal-disk-pressure, container-restart-loops, time-drift, cert-near-expiry, …) isn't. Study Yunohost's `yunohost diagnosis` check taxonomy before enumerating — closest neighbor with a mature, opinionated set. `HEALTH.md`.

**Top-level**
- Redundancy implementation when Level 2 storage ships (btrfs vs. ZFS vs. mdadm). `SPEC.md`.
- ARM64 timeline. `SPEC.md`, `BUILD.md`.
- License for the OS itself. `SPEC.md`.
