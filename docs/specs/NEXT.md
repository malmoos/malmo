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

*(Last audit: 2026-05-29 — the dashboard-shell gap is now **resolved** by the new `DASHBOARD.md` (logged-in IA + the owner-scoped apps model; see `DECISIONS.md` 2026-05-29 # App instances are owner-scoped). That also resolved the apps model, the widgets yes/no (no), and the per-user-instance naming scheme. The file-manager gap below remains the one Tier-1 product-surface item. The infrastructure spine (boot/storage/health/updates/auth) is well-specified. Implementation slice queue lives in [`../progress/README.md`](../progress/README.md) # Up next.)*

### In-dashboard file manager — "files are first-class" has no in-product browse surface

"Files are first-class, apps are windows" is a load-bearing differentiator, but the only specced access path to a user's own `Photos/`, `Documents/`, `Shared/` is **SMB + the desktop OS file managers**. `DISCOVERY.md` even states the browse experience "is the dashboard, not the OS file manager" — yet no in-dashboard file browser exists. For the north-star audience (the Plex/Synology user who will *not* mount an SMB share), a web file manager is table stakes — it's Synology's flagship surface (File Station) and a core Umbrel module. The Tier-4 "data-import flows" line is a fraction of this.

**Proposed home:** a new `FILES.md` owning the in-dashboard file browser — view/upload/download/rename/move within the user's use-case folders and `Shared/`, per-user scoping (mirrors the FS permission model in `STORAGE.md`), and how it relates to (does not replace) SMB. Folds in the Tier-4 "data-import flows" item.
**Context:** `STORAGE.md` (use-case folders, `0750` per-user permissions, `Shared/`), `DISCOVERY.md` (the "dashboard is the browse surface" claim this makes good on), `APP_ISOLATION.md` (how apps see the same folders), `AUTH.md` (per-user access). Prior art: Synology File Station; Umbrel's `files` module.
**Why Tier 1:** without it, "files are first-class" is true on disk but invisible in the product for any user who hasn't set up SMB — exactly the user we're building for.

---

## Tier 2 — UX-shaping

### Off-box notification transports (`NOTIFICATIONS.md` # transport-agnostic seam)

The in-product notification center (the dashboard bell) is **decided and specced in `NOTIFICATIONS.md`** — v1 is dashboard-only. What remains open is the *off-box* delivery that actually reaches the user who isn't looking at the dashboard (the pantry-box case the bell deliberately doesn't solve): **email** and **mobile push**. Both slot in behind the transport-agnostic seam already designed in `NOTIFICATIONS.md` — no model rework, just new sinks + per-user/per-category/per-severity delivery preferences. Email is gated on **email-on-file** (separate Tier-2 item below); push is gated on the mobile app (deferred with the mesh). Also still open: quiet-hours / snooze and daily-digest, which only earn their keep once an off-box transport exists.

**Context:** `NOTIFICATIONS.md` (the model + seam), email-on-file entry below, `MALMO_NETWORK.md` (SMTP relay — own vs. transactional; residential-IP deliverability), mobile app (deferred, `MALMO_NETWORK.md` # mesh).
**Why Tier 2:** the bell closes the "I missed a transient banner" gap; it does **not** close the "nobody's looking" gap. Until an off-box transport lands, a degraded drive in a pantry goes unseen until someone next opens the dashboard.
**Prior art:** Synology DSM (push/email/SMS center) is the target shape; TrueNAS pairs an always-on in-UI bell with opt-in transport services — exactly the seam we built. v1 = bell only (Umbrel's level, done well); off-box transports move us toward Synology-lite.

### UPS, clean shutdown, power-event handling

The box is plugged into a wall and lives 24/7 in a pantry. Power blip → unclean LUKS shutdown → degraded mode at boot. v1 minimum: graceful shutdown on power-button press, a "Shut down / Restart" affordance in the dashboard, and (if a USB UPS is attached) low-battery hook via NUT. Affects `BOOT.md` (clean-shutdown path), `HEALTH.md` (a "running on battery" issue type), and the install/wizard story (do we detect a UPS at first run?).

**Context:** `BOOT.md`, `HEALTH.md`, `FIRST_RUN.md`, `AUTH.md` (who can shut down the box from the UI).
**Why Tier 2:** Synology/TrueNAS treat this as table stakes. Most boxes will never see a UPS, but "shut down from the dashboard" and "don't corrupt on power blip" are universal.

### i18n posture (v1 + schema door)

Two-part decision: (a) is v1 English-only? (very probably yes); (b) does the manifest schema *allow* localized fields (`name`, `description`, `category`) so a future translation pass doesn't require a breaking schema bump? Yunohost is fully multilingual and we'll get the ask. Decide the door now; defer the translations.

**Context:** `APP_MANIFEST.md`, `APP_STORE.md`, `WEB_UI.md`.
**Why Tier 2:** schema-shaping; literally one or two field-shape decisions in `APP_MANIFEST.md`.

### Custom container (Door 2) install flow

The actual paste-compose UX. Field-by-field interaction, main-port inference, what we ask vs. autodetect, name collisions, edit-after-install path.

**Context:** `APP_MANIFEST.md` ("Custom container — synthetic manifest").
**Why Tier 2:** Door 2 is the bridge to the "tinkerer adoption" audience. The synthetic-manifest mechanic is sketched; the UX isn't.

### Store catalog curation policy

`APP_STORE.md` pins the publish *mechanism* (signed catalog, PR-based, CI-validated). What's still open is the **content policy** the maintainer enforces in review: do we reject manifests that set `storage.app_managed_user_content: true`, or only label them with the absence of the `files_first_class` badge? Do we require apps to log to stdout/stderr (no `logging.driver:` overrides, no in-`command:` file redirects) so the dashboard Logs tab works (`LOGGING.md` # Apps are expected to log to stdout)? What other criteria gate inclusion (license, upstream maintenance signals, declared-vs-actual permission audit)?

**Context:** `APP_STORE.md` # v1 catalog is hand-curated by malmo, `APP_MANIFEST.md` # External-storage convention, `STORAGE.md` # Files are first-class, `LOGGING.md`.
**Why Tier 2:** the "files are first-class" principle only holds if curation defends it. Decided lazily, the catalog fills with opaque-library apps and the principle erodes. The mechanism is in place; the bar isn't written down.
**Prior art:** Yunohost's app integration-level (0–9) rating is a useful model for surfacing curation outcomes as a small badge set (e.g. `Files-first-class`, `Backup-aware`, `Multi-instance`, `Stdout-clean`) — a single number is too coarse, but the underlying idea of "show users the integration-quality signal without exposing manifest internals" maps well.

### Shared folder management UX

Who can add/rename subfolders under `~/Shared/`? How does an admin see what's in there vs. per-user? Does each user get a per-user view filtered to "things I have access to," or a flat view? Group management for `malmo-shared` (kick a user off shared content) — Settings surface.

**Context:** `STORAGE.md` # Permissions, `AUTH.md`.
**Why Tier 2:** the on-disk mechanics are pinned; the dashboard UX isn't. Households without this can still use Shared/ via SMB, but the in-dashboard view needs design before public release.

### First-run restore / migrate-from-old-box fork

`FIRST_RUN.md` is greenfield-only today: wipe → wizard → dashboard. There is no branch for "I'm replacing my old box" or "restore from backup." Even though off-site backup itself is deferred (Tier-3 "Backup architecture shape"), the *fork* in the wizard shapes first-run now — a "Set up as new" vs. "Restore from backup / migrate from another box" choice early in the flow. Reserve it now or retrofit the wizard later. Umbrel ships a data-export / migration path; cross-box migration is also tracked under Tier-4 "Backup & migration."

**Context:** `FIRST_RUN.md`, Tier-3 "Backup architecture shape," Tier-4 "Cross-box migration."
**Why Tier 2:** it's a structural branch in the first-run flow; cheap to reserve the fork now, expensive to wedge in after the happy-path wizard is built.

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

### Factory reset / repurpose / "start over"

Explicitly undocumented today — `USERS_AND_GROUPS.md` # Known gaps admits "we don't have a clean 'reset everything except user content' story yet; treat reinstall + restore from backup as the floor." This is an end-to-end lifecycle gap: resale, household handoff, "I broke it and want a clean slate." It has a security dimension beyond UX — securely destroying LUKS keyslots so the outgoing drive is unreadable — so it's not purely a dashboard flow. Both Synology and Umbrel treat reset/repurpose as standard. Open: scope (full wipe vs. reset-config-keep-content vs. reset-keep-nothing), where it lives in the UI (Settings → Advanced, gated by fresh password), and the key-destruction mechanics.

**Context:** `USERS_AND_GROUPS.md` # Known gaps, `STORAGE.md` (LUKS keyslot destruction), `AUTH.md` (elevation gate), `FIRST_RUN.md` (post-reset re-onboarding), `BUILD.md` (relationship to reinstall).
**Why Tier 3:** reinstall-from-ISO is the v1 floor, so this doesn't block bring-up; bites the first time someone resells or hands off a box. Pin the shape (esp. key destruction) before public release.

### Brain state-migration framework (SQLite schema + on-disk layout)

App-level and managed-service migration are well-specced (`SERVICE_PROVISIONING.md` cross-version auto-migrate, `UPDATES.md` pre-update snapshot). The **brain's own** SQLite schema + on-disk instance-layout migration across auto-updates is only *referenced* (LOGGING.md mentions "buggy migrations" defensively) — no doc owns the mechanism. For an auto-updating fleet this is load-bearing: a bad brain migration bricks boxes with no UI left to recover from. Umbrel carries dedicated `migration` and `startup-migrations` modules for exactly this. Pin the shape: versioned, forward-only, transactional, run-before-serving, tested in the boot lanes, and how it interacts with brain-version rollback (`RELEASE_MANIFEST.md` `rollback_to`).

**Context:** `CONTROL_PLANE.md` (brain lifecycle), `UPDATES.md` (brain+UI stream, rollback), `RELEASE_MANIFEST.md` (`rollback_to`), `LOGGING.md` (append-only audit triggers vs. migrations), `TESTING.md` (boot-lane coverage).
**Why Tier 3:** the brain schema is small and stable today, so it doesn't bite at current scale; becomes a fleet-bricking risk the moment a migration ships wrong. Pin the discipline before the schema grows.

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

**Context:** `APP_MANIFEST.md` (`permissions.folders`), `APP_ISOLATION.md`, `APP_LIFECYCLE.md`, `LOGGING.md` (audit).
**Why Tier 3:** install-time grant works for v1; revocation is the second-order feature users will ask for once they've lived with the box for a while.

### Hostname / box-name rename

Can the user change the box's display name after first-run? Cascades into mDNS hostname (`malmo.local` → `kitchen.local`?), Let's Encrypt cert SANs on the `MALMO_NETWORK` side, audit log historical naming, SMB advertisement. Easy to forbid; easy to allow with caveats; expensive to retrofit if we wire the name into too many places. Also the forced-rename path: when Avahi conflict-resolves us to `malmo-2.local` (`HEALTH.md` `hostname-conflict`), how aggressively does the dashboard prompt for a real fix vs. let the user ride it out?

**Context:** `FIRST_RUN.md`, `MALMO_NETWORK.md`, `STORAGE.md` (Samba), `LOGGING.md`, `DISCOVERY.md`, `HEALTH.md`.
**Why Tier 3:** not a v1 feature; pin the architectural separation between "box-id" (stable) and "display name" (mutable) before too much code depends on conflating them.

### Per-app Bonjour service records (`_http._tcp`)

`DISCOVERY.md` ships v1 with per-app A records only — apps don't appear in Finder's "Network" sidebar or Windows Explorer's network view as individually browseable services. Adding `_http._tcp` advertisements per app would surface them there, plus enable some "discover devices on your network" flows in other apps. Easy to add; the open question is whether the dashboard isn't already the right browse surface.

**Context:** `DISCOVERY.md`.
**Why Tier 3:** additive, can land any time. Default position: don't add it; revisit if real demand appears.

### URL-scheme unification

Two URL schemes, two access models in users' heads: `.local` HTTP on the LAN, `<box-id>.malmo.network` HTTPS via the toggle. The current model accepts the cognitive cost because the alternatives (always cloud, always `.local`, private CA + cert install on every device) each impose worse failure modes. Worth revisiting once we have real first-run analytics: how many users flip the toggle, how many are confused by the scheme switch, do Android households self-select toward enrolled boxes.

**Context:** `MALMO_NETWORK.md`, `DISCOVERY.md`, `FIRST_RUN.md`.
**Why Tier 3:** unification is a v2 question — needs operational data we don't have yet.

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

When a box has been offline long enough that `.malmo.network` certs expired: serve the expired cert with browser warning, transparently redirect to `.local`, or surface a banner in the dashboard. `DISCOVERY.md` makes the `.local` fallback well-defined (per-app records keep working without cloud reachability), so "redirect to `.local` + banner" is the leading option for desktop households — but it doesn't work for Android households, where `.local` URLs are unreachable. The open question is whether to special-case that audience (e.g., a static "your cert expired, plug in for an hour" page served on the LAN IP).

**Context:** `MALMO_NETWORK.md` ("Failure modes"), `DISCOVERY.md`.

### Phased rollout / cohort + beta channel activation

Both deferred from v1 (`DECISIONS.md` 2026-05-15). Shape is pinned in `RELEASE_MANIFEST.md` # Future work + # Channels — schema is additive, hash formula is `hash(machine_id || canonical(brain, ui))`, beta is a sibling `beta.json` file. What's still open: the **trigger conditions** in concrete terms (what fleet-size threshold, what auto-apply milestone, what bad-release detection latency forces our hand). Pre-decide so we don't dither when one of them fires.

**Context:** `RELEASE_MANIFEST.md`, `UPDATES.md` # 3.

### Settings → Storage UX (Level-1 walk-through, design pass)

The architecture and the install/wizard/add-drive/eject mechanics are locked (`STORAGE.md`, `FIRST_RUN.md`, `AUTH.md`, `BRAIN_HOST_PROTOCOL.md`, `HEALTH.md` # `disk-full`). What remains is design-time copy + screen-layout: card shape for OS drive vs. data drive at Level 0/1, where the "Show recovery passphrase" affordance lives under Advanced, eject-drive confirmation copy, disk-pressure banner copy + top-space-hogs enumeration, single-drive "add a data drive later" dashboard hint, and the file-access permission block on the app-install dialog ("Photos will read and write your Photos folder").

**Context:** `STORAGE.md`, `FIRST_RUN.md`, `HEALTH.md`, `APP_MANIFEST.md` # `permissions.folders`.
**Why Tier 3:** doesn't block bring-up — the brain endpoints and health-issue flags exist. UX iteration belongs with the designer and the first user-test pass, not the spec.

### Reboot scheduling UX

"Reboot tonight at 3am OK?" prompt vs. silent within window. Surface only when blocked vs. always.

**Context:** `UPDATES.md`.

### Member-visible Tier-1 app logs (manifest opt-in)

`LOGGING.md` defaults Tier-1 shared app logs to admin-only — stdout commonly leaks per-user behavioral signal (request paths, search queries). For apps whose stdout is genuinely uninteresting (a periodic sync daemon, an indexer), a manifest field `logs.member_visible: true` would let authors opt the app's logs into the per-member Logs tab. Default off; off remains the safe choice for the bulk of the catalog.

**Context:** `LOGGING.md` # Per-app logs, `APP_MANIFEST.md`.
**Why Tier 3:** doesn't block v1; the day a member can't debug a shared-app issue without flagging down an admin, this lands.

### Audit-log schema details

`LOGGING.md` pins the `audit_events` table shape, write path, and v1 action vocabulary. Open: the typed metadata schema per action type (free-form JSON works for v1 but becomes a UI rendering pain at scale), and whether to add a hash-chain / sequence-number integrity guarantee on top of the append-only invariant. Neither blocks v1; both become tech debt if left unresolved as the catalog grows.

**Context:** `LOGGING.md`, `AUTH.md`, `APP_LIFECYCLE.md`.
**Why Tier 3:** the UI works against free-form JSON in v1, but every new event type adds rendering work. Pin the metadata schema before the catalog grows.

### Recovery dashboard spec (`RECOVERY.md`)

`malmo-recovery.target` shrunk to two triggers in `DECISIONS.md` 2026-05-16: **TPM2 unseal failure on the root drive** (LUKS recovery passphrase at console — needs a printed-on-the-box / wizard-shown story), and **host-agent crashloop past `StartLimitBurst`** (static page on port 80 with one-click "roll back host-agent"). The page's actual content + UX, the rollback mechanism, and the mDNS discoverability story (`malmo-recovery.local`) are not specced. Most failure modes that the old strict-gate model routed here now flow through degraded mode (`HEALTH.md`) — recovery is now an honestly small surface.

**Context:** `BOOT.md` # Failure → recovery target — the narrow cases, `HEALTH.md` (the rest of what used to live here), `STORAGE.md` # Encryption posture, `AUTH.md` (recovery code vs. LUKS recovery passphrase distinction).
**Why Tier 3:** doesn't block v1 happy-path development; bites the moment a user hits one of the two genuinely-unrecoverable-from-UI cases. Pin the shape before public release.

### Threat-model re-pass when the mesh ships

`THREAT_MODEL.md` is scoped to v1 closed-by-default and explicitly names the trigger for a re-pass: remote access via the mesh (`MALMO_NETWORK.md` # Deferred) reshapes boundary **B1** (off-LAN reachability), introduces a new principal (a paired-but-non-household device — "grandma sees Photos"), and narrows "closed-by-default" to "closed except to identity-paired devices." When the mesh is picked up, the threat model gets a dedicated boundary pass + `DECISIONS.md` entries.

**Context:** `THREAT_MODEL.md` # When this model changes, `MALMO_NETWORK.md` # Deferred: remote access via mesh.
**Why Tier 3:** rides the mesh work, which is itself deferred. Pinned here so the "living document" claim isn't hollow.

### fscrypt rollout plan

Per-user encryption is deferred but on the roadmap. Key-loading model (Model A vs. B in `STORAGE.md`), interaction with background app work (`APP_ISOLATION.md`), password-recovery escape hatch.

**Context:** `STORAGE.md`, `APP_ISOLATION.md`.

### Caddy liveness self-heal (gated on brain-owned Caddy container lifecycle)

The `service-down`(caddy) detector (`HEALTH.md` # Detector catalog, locus C) can't be a passive banner — Caddy fronts `malmo.local`, so when it's down there's no dashboard to show the banner. The decided shape (`DECISIONS.md` 2026-05-31) is **bounded self-heal**: the brain restarts the Caddy container on failure, capped like host-agent's `StartLimitBurst` (≈5/60s), raising the issue only when the budget is exhausted. The blocker is that **the brain does not yet own Caddy's container lifecycle** — it manages Caddy's *routes* (`EnsureServer`/`EnsureCatchAll`, `internal/lifecycle`) but never starts/stops/restarts the Caddy *container*; in prod "brain-managed Caddy" is intent, not implementation (`dev/docker-compose.yml` runs it standalone). The real prerequisite is a brain-owned Caddy container lifecycle (start/stop/restart via the socket-proxy), which is partly host-integrated and needs the VM outer loop. Once that lands, the self-heal detector is a thin layer on top (probe = Docker container-state + admin-API reachability; restart-budget; raise-on-exhaustion; reuse `internal/caddy.Client`).

**Context:** `HEALTH.md` # Detector catalog (locus-C Caddy row), `CONTROL_PLANE.md` # Locked: Caddy runs as a container, `DECISIONS.md` 2026-05-31.
**Why Tier 3:** doesn't block v1 happy-path; a fully-down Caddy is already visibly broken (dashboard unreachable). Pin the self-heal shape now (done); build it after the brain owns Caddy's container lifecycle.

---

## Tier 4 — Smaller open items

Loose ends. Each is parked until it bites or a higher-tier topic pulls it in.

**Manifests & catalog**
- Exact `MALMO_SERVICE_*` variable schema per service type — `APP_MANIFEST.md`, `SERVICE_PROVISIONING.md`.
- `permissions.devices` syntax — paths vs. categories (`webcam`, etc.). `APP_MANIFEST.md`. *(GPU split out into its own `gpu: true` field — `DECISIONS.md` 2026-05-30; this item now covers only non-GPU device shorthand.)*
- **Store `permissions.capabilities` escape hatch (deferred).** A reviewed-at-submission list (`NET_ADMIN`, `SYS_TIME`) for the rare store app that legitimately needs one capability. Cut from the v1 schema (`DECISIONS.md` 2026-05-30 — store apps get `cap_drop: [ALL]`, no `cap_add`); capability needs go through Door-2 / Tier 2 today. Revisit if a curated app genuinely can't fit either path. `APP_MANIFEST.md`, `APP_ISOLATION.md`.
- **Door-2 vs. Door-1 admission asymmetry.** `APP_ISOLATION.md`'s trust-tier model says Door-2 custom compose may carry `privileged` / `cap_add` / the Docker socket ("the user wrote it"), but the implemented admission policy (`internal/admission`, run for *both* doors in `install()`) rejects all three uniformly. Decide whether Door-2 actually relaxes admission, and if so exactly which primitives — vs. holding the line and pushing those use cases to Tier 2 only. `APP_LIFECYCLE.md` # admission policy, `APP_ISOLATION.md` # Trust tiers.
- Manifest signing / provenance for third-party stores. `APP_MANIFEST.md`.
- App icon & screenshot handling — bundled vs. URL. `APP_MANIFEST.md`.
- Update-strategy declarations (in-place vs. needs-migration). `APP_MANIFEST.md` (folds into hooks).
- Typed install-time questions in the manifest (prior art: Yunohost's pre-install question schema — typed prompts for admin/domain/language captured at install time). We have nothing today; revisit when Door 2 / managed-config grows beyond env-var passthrough. `APP_MANIFEST.md`.
- App categories / tags taxonomy for the store browse UX. `APP_STORE.md`, `WEB_UI.md`.
- Per-app cron / scheduled tasks declared in manifest (distinct from the Tier-3 background-jobs service in `SERVICE_PROVISIONING.md`). Cron-on-host vs. a per-instance scheduler container. `APP_MANIFEST.md`.
- Per-app kill switch in `catalog.json` (distinct from `RELEASE_MANIFEST.md`'s `rollback_to`, which targets brain/UI versions). For "CVE dropped in app X, stop it everywhere on next catalog refresh." `APP_STORE.md`, `APP_LIFECYCLE.md`.
- Catalog removal / delisted-app behavior — installed instances when an app is pulled (keep-running-with-warning vs. force-uninstall vs. read-only). `APP_STORE.md`, `APP_LIFECYCLE.md`.
- App dependency model — pin "no" (managed services are the answer) so authors don't build the assumption. `APP_MANIFEST.md`.
- *(Resolved 2026-05-29 — user-driven multi-instance is **yes**: duplicate installs warn but don't block (`DASHBOARD.md` # warn, don't block). Each becomes a personal instance with its own owner/slug/data. The remaining sub-question — two instances owned by the *same* user — folds into the same machinery; pin it if it ever bites.)*
- Same-user repeat-install slug cap + error UX. `allocateSlug` (`internal/lifecycle`) tries `<slug>` (bare, first-come), `<slug>--<user>` (personal collision), `<slug>-2`, `<slug>-3`, then fails *inside the install job* with an opaque "no free slug" error. Two open bits: (a) the cap of effectively three slugs before exhaustion is arbitrary and tight for power users who install the same app multiple times; (b) exhaustion should surface as a clear pre-job `422`/`409`, not a mid-job failure. `APP_LIFECYCLE.md`, `DASHBOARD.md`.
- App publisher identity / verified-author badge surface (the *mechanism* folds into manifest signing above; this is the catalog-side UX). `APP_STORE.md`.
- Per-app HTTP health-probe declaration in the manifest (beyond Docker `HEALTHCHECK`), so the brain reports "responding" vs. "up but unresponsive." `APP_MANIFEST.md`, `HEALTH.md`.
- Container vulnerability scanning at catalog publish (Trivy/Grype in CI on every PR). `APP_STORE.md`.

**Networking & cloud**
- `box-id` allocation scheme — word-pair vs. random hex + check digit. `MALMO_NETWORK.md`.
- DNS provider for the apex — Cloudflare free tier vs. self-hosted PowerDNS. `MALMO_NETWORK.md`.
- ACME DNS-01 plugin path — Caddy generic vs. malmo-specific plugin. `MALMO_NETWORK.md`.
- Privacy doc surface — what we log (DNS queries, enrollment metadata) and retention. `MALMO_NETWORK.md`.
- **Multicast / discovery diagnostic-bundle probe — exact shape.** `LOGGING.md` and `DISCOVERY.md` commit to including a multicast self-test in the diagnostic bundle; the precise measurements (which queries we send, on which interfaces, how we present "responses: 0" to a support tech) need a pass. `LOGGING.md`, `DISCOVERY.md`.
- **Windows Bonjour detection in first-run.** `FIRST_RUN.md` points Windows users at the Bonjour installer; the trigger today is User-Agent, which is unreliable. Consider a JS-side mDNS probe (does the browser actually resolve `malmo.local` *from this client*?) so the prompt only fires on clients that need it. `FIRST_RUN.md`, `DISCOVERY.md`.
- Custom domain on the LAN — user owns `home.example.com` and wants the dashboard there. Caddy + ACME DNS-01 with their provider, or accept-cert-warning. `MALMO_NETWORK.md`.
- Local DNS resolver shape — host runs dnsmasq (container resolution + free Pi-hole-shape ad-blocking as a side effect) vs. pure systemd-resolved. `APP_ISOLATION.md`, `MALMO_NETWORK.md`.
- UPnP / port-forwarding stance — closed-by-default implies "no"; state it explicitly so a future "convenience" PR doesn't sleepwalk into it. `MALMO_NETWORK.md`, `SPEC.md`.
- `status.malmo.network` outage-comms surface — boxes show a banner from a cached status JSON when cloud is down. `MALMO_NETWORK.md`.
- Anti-clone check at enrollment — two boxes with the same `box-id` (cloned ISO) must not both enroll. `MALMO_NETWORK.md`.
- **Live-installer WiFi step.** A WiFi-only laptop has no ethernet, so the live ISO itself needs an SSID-picker before "Install to disk" (or be fully offline-installable). Also: WiFi credentials entered in the installer must survive into the installed system's NetworkManager config, not just the live environment. Driver coverage (Realtek/Broadcom non-free firmware) is the connected build-side concern. `BUILD.md`, `FIRST_RUN.md` # Step 1.
- **Dashboard Settings → Network panel UX.** The plumbing (NM-backed endpoints) is in `BRAIN_HOST_PROTOCOL.md`; the UX details (saved-networks list, signal/security indicators, switch-network "you may briefly lose this page" confirmation, static-IP form, multi-NIC priority controls) belong to `WEB_UI.md`. `BRAIN_HOST_PROTOCOL.md` # Network endpoints, `WEB_UI.md`.

**Isolation & runtime**
- **GPU + device capacity enforcement.** `permissions.gpu` and `permissions.devices` are parsed and (for devices) passed through, but the spec's "refuse at capacity check if the GPU/device is absent" (`APP_ISOLATION.md` # GPU, # Devices) is not honored — the brain has no host hardware-capability query, so an absent GPU/device currently fails at `docker compose up` instead of giving the specced capacity error, and `gpu: true` emits no runtime stanza at all. Needs a host capability endpoint (sibling of `/v1/identity/well-known`) the install transaction checks before generating the override. Deferred from the folder-enforcement slice (`docs/progress/install-permissions-enforcement.md`). `APP_ISOLATION.md`, `BRAIN_HOST_PROTOCOL.md`.
- GPU sharing across apps (MIG / time-slice / exclusive). `APP_ISOLATION.md`.
- macvlan on bonded / bridged host interfaces. `APP_ISOLATION.md`.
- Read-only root rollout as a catalog requirement. `APP_ISOLATION.md`.
- Egress allowlist for `internet: true`. `APP_ISOLATION.md`.
- Per-app firewall rules (apps as L4 endpoints). `APP_ISOLATION.md`.
- Author-declared default/hint for folder source (e.g. an `allow_shared`-style flag) so a manifest can bias the install-time personal-vs-shared toggle without removing the installer's choice. Resolved-for-now as fully installer-elected (`DECISIONS.md` 2026-05-30); revisit if catalog demand appears. `APP_MANIFEST.md` # `folders`.
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
- Account deletion flow — what happens to `/home/<user>/` and per-user Tier-3 instances when an account is removed. (Audit-row handling is settled: FK `SET NULL` on `audit_events.actor_user_id` keeps history with a null actor.) `USERS_AND_GROUPS.md`, `AUTH.md`.
- Account suspension — disable login without deleting data (kid grounded, ex-roommate archived). `AUTH.md`, `USERS_AND_GROUPS.md`.
- Multi-admin invitation flow — UI affordance for "make a second admin." Today implicit (admin creates a member then promotes them). `AUTH.md`, `USERS_AND_GROUPS.md`.
- Dashboard login brute-force throttling / lockout — `LOGGING.md` notes journald caps sshd spam, but the brain's own login endpoint has no rate-limit story. `AUTH.md`.

**Runtime & host**
- Container image / layer cleanup policy — `docker image prune` cadence + retention so old images don't fill the OS drive over time. `APP_LIFECYCLE.md`, `UPDATES.md`.
- Container runtime version pinning — which Docker engine version we ship, how it tracks Debian-base updates vs. upstream `docker-ce`. `BUILD.md`, `UPDATES.md`.
- Host kernel panic / coredump capture policy — what we keep, where, retention. Brain & host-agent process panics are covered by `TELEMETRY.md` (structured crash events when opt-in is on). Kernel panics are the remaining gap. `LOGGING.md`, `HEALTH.md`.
- Log rotation for non-journald files (Caddy access logs, anything that escapes the journal). `LOGGING.md`.

**Observability (user-facing)**
- Per-category mute vs. criticals — mute is implemented as a full read-time category filter (`NOTIFICATIONS.md` # Configuration), so muting `storage` also hides a `data-drive-readonly` critical. Spec-faithful and the user's explicit choice (defaults are everything-on), but a future "critical always rings through, mute only quiets info/warning" carve-out is plausible if support traffic shows muted criticals get missed. Deferred, not decided. `NOTIFICATIONS.md` # Configuration, # Severity.
- Role-filter the mute settings list — the Settings → Notifications toggle list (`NOTIFICATIONS.md` # Configuration, Surface) shows all of `notify.Categories` to every user, but per # Routing a member never *receives* the admin-only categories (`storage`, `system`, admin `updates`), so a member muting "Storage" writes a dead row. Faithful to the backend (which validates all categories regardless of role) and harmless, but the list could be trimmed to the categories a user can actually receive. Open question is whether per-role filtering is worth the role→category map it requires. Deferred, not decided. `NOTIFICATIONS.md` # Configuration, # Routing.
- Settings → System deep-view (admin route) — full 60-second graphs of CPU/RAM/net/disk with all interfaces and drives broken out, over the same `GET /api/v1/system/live` stream that powers the all-users top-bar dropdown. The dropdown ships first; this is the deeper admin surface. `LOCAL_ANALYTICS.md` # UI surfaces, `WEB_UI.md`.
- Per-container live monitor ("Activity Monitor" view) — sortable table of all containers with live CPU/RAM/net/disk. Host-level live view is specced (top-bar dropdown); per-container live is the deferred surface. Mechanism same as system-resources SSE. `LOCAL_ANALYTICS.md`, `WEB_UI.md`.
- App-level network bandwidth accounting (per-container veth stats). Useful for "which app is hammering my ISP" but expensive. `LOCAL_ANALYTICS.md`.
- Storage growth attribution — what *kind* of data grew ("Photos +50 GB this month, mostly RAW files"). Compound on top of the per-app storage trend already specced. `LOCAL_ANALYTICS.md`, `STORAGE.md`.
- "What's eating my disk" explorer — top-N folders/apps under `/srv/malmo`. Folds into Settings → Storage UX (Tier 3). `STORAGE.md`, `WEB_UI.md`.

**Time**
- Captive-network NTP fallback — reconsider `time.malmo.network` if user reports surface (networks that block external NTP). `TIME.md`.
- Per-user display TZ — browser-side `Intl.DateTimeFormat` covers the traveler case in v1; revisit if box-time-regardless requests appear. `TIME.md`.
- `last-known-time` rollback prevention — persist last-shutdown wall-clock so first-boot-no-network doesn't render 1970 in logs. Polish. `TIME.md`.

**Telemetry (project-side)**
- GeoIP source for country bucketing on the telemetry edge — which DB, refresh cadence, unresolved-IP placeholder. `TELEMETRY.md`.
- Crash stream split — v1 uses PostHog Cloud for both usage + crashes; split to self-hosted Sentry later if crash volume justifies it. `TELEMETRY.md`.
- `events` vs `audit_events` table unification in brain SQLite — single table with `category` discriminator, or two tables. Implementation choice; spec treats them as one logical store. `LOCAL_ANALYTICS.md`, `LOGGING.md`.

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
- *(Resolved 2026-05-29/31 — instance naming is first-come bare `<slug>` for any scope; `<slug>--<user>` on personal collision, `<slug>-2` on household collision. Flat single-label forced by wildcard-cert + mDNS constraints. See `DASHBOARD.md` # instance naming and `DECISIONS.md` 2026-05-29 + 2026-05-31.)*
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
- *(Resolved 2026-05-29 — v1 health-check enumeration is now specced as the `HEALTH.md` # Detector catalog; see `DECISIONS.md` 2026-05-29. Disk SMART, journal-disk-pressure, container-restart-loop, service-down, ram-pressure, reboot-required, auto-unlock-degraded landed in the taxonomy; per-app HTTP health-probe remains deferred — see the manifest item below. Implementation follows as brain `internal/health` registry additions + host-agent locus-B reporters + the generalized `/v1/health/system` report.)*

**Top-level**
- Redundancy implementation when Level 2 storage ships (btrfs vs. ZFS vs. mdadm). `SPEC.md`.
- ARM64 timeline. `SPEC.md`, `BUILD.md`.
- License for the OS itself. `SPEC.md`.
