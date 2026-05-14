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

*(no items — the most recent resolution is the web UI deploy model + versioning posture, `DECISIONS.md` 2026-05-14.)*

---

## Tier 2 — UX-shaping

### Release-manifest schema + publishing pipeline

The JSON shape is sketched in `UPDATES.md` § 3 (control plane). The build + signing infrastructure isn't — who publishes the manifest, where it lives, signature scheme, channel promotion (beta → stable) mechanics, manifest hash for the cohort calculation. Adjacent to `BUILD.md` § Signing.

**Context:** `UPDATES.md`, `BUILD.md`.
**Why Tier 2:** v1 ships an OS; the OS needs an update channel before any box leaves the lab.

### CI enforcement of additive-minor API discipline

`/api/v1` fields can be added, never removed or repurposed; event `kind` values added, never removed (`DECISIONS.md` 2026-05-14, web UI deploy). Needs concrete CI: diff `openapi.yaml` between commits, fail on field removal / type change / required-flag tightening; same for event-kind enum.

**Context:** `BRAIN_UI_PROTOCOL.md` § "API discipline."
**Why Tier 2:** discipline without enforcement decays. Land before the first external integrator depends on a v1 field.



### Custom container (Door 2) install flow

The actual paste-compose UX. Field-by-field interaction, main-port inference, what we ask vs. autodetect, name collisions, edit-after-install path.

**Context:** `APP_MANIFEST.md` ("Custom container — synthetic manifest").
**Why Tier 2:** Door 2 is the bridge to the "tinkerer adoption" audience. The synthetic-manifest mechanic is sketched; the UX isn't.

### App update UX

Auto vs. manual approve, batching, breakage signaling, "an update is available" affordances, what shows on the dashboard after an update applies. The mechanics are in `UPDATES.md`; the *interface* isn't.

**Context:** `UPDATES.md`, `APP_LIFECYCLE.md` (state machine on `updating` / `update_failed`).
**Why Tier 2:** user trust is built or destroyed here. A silent breaking update is the single fastest way to lose the audience.

### Dashboard at first arrival

Empty, "get started" suggestions, or a starter bundle. Tradeoffs: friction vs. opinionated push vs. discovery.

**Context:** `FIRST_RUN.md` (Phase 3).
**Why Tier 2:** first-impression UX; gates the wizard-to-steady-state hand-off.

### Storage Level 1 end-to-end walk-through

The data drive flow from installer choice → setup wizard → first app install → user-visible behavior. `STORAGE.md` has the architecture; the user-facing sequence is implicit.

**Context:** `STORAGE.md`, `FIRST_RUN.md`, `APP_MANIFEST.md` (`storage.user_facing_paths`).
**Why Tier 2:** every multi-disk box hits this on day one.

### Email-on-file for users

Required for password recovery, product comms, and any future cloud-account linking. Currently sidestepped because all three are deferred — but the *decision* of whether to collect at first-run shapes the wizard.

**Context:** `FIRST_RUN.md`.
**Why Tier 2:** decided now or it becomes a forced retrofit later. Likely answer: optional field at user creation, used for recovery only.

### OpenAPI codegen timing for the brain API

The brain↔UI API is hand-rolled Go ↔ TS types in v1 (`DECISIONS.md` 2026-05-14, brain↔UI API). The OpenAPI 3 spec + generated TS client lands later. Open: when — before the public store API ships, after the first external integrator asks, or on a fixed schedule? Generator choice (`oapi-codegen` for Go server, `openapi-typescript` for TS client) is straightforward; timing is the call.

**Context:** `BRAIN_UI_PROTOCOL.md` § "API discipline."
**Why Tier 2:** every week we ship without it, drift between hand-rolled types grows. Cheap insurance if we pin a trigger.

### Rate-limit / abuse posture for the public API

The brain↔UI API is public-callable from day one (third-party stores, CLI, external tools — `DECISIONS.md` 2026-05-14). v1 has no rate-limiting story. Open: per-session limits, per-IP for unauthenticated routes, separate budget for SSE stream count vs. request rate, what 429 messaging looks like.

**Context:** `BRAIN_UI_PROTOCOL.md`, `AUTH.md`.
**Why Tier 2:** needs to land before third-party stores can ship; not blocking v1.

---

## Tier 3 — Defer-able, but pin the shape

### Backup architecture shape

Off-site backup is paid + post-MVP, but the *interfaces* — manifest hints (data vs. cache volumes), restore path, bind-mount-only constraint, managed-service dump path — should be sketched now to avoid retrofitting once we ship.

**Context:** `APP_MANIFEST.md` (`storage.data_volumes` / `cache_volumes`), `SERVICE_PROVISIONING.md` (managed-service backups), `APP_LIFECYCLE.md`.

### Display-name rename UX + audit log story

Slug is stable; the rename mechanic is straightforward but "who is `cindy` if she renamed to `cynthia`?" needs design across audit log, sharing, and any future identity-bearing UI.

**Context:** `FIRST_RUN.md` ("Identity & display names").

### App-facing background-job service (Tier-1)

A managed queue + worker that apps can fire background work into (overnight re-encoding, ML indexing, etc.). Apps declare `services.jobs: { type: malmo-jobs }` in the manifest; brain provisions credentials + queue URL. Probable implementation: Redis Streams or NATS JetStream as the queue, a malmo-managed worker pool runs jobs during a configured idle window.

**Context:** `SERVICE_PROVISIONING.md` (Tier-1 catalog — would extend it), `APP_MANIFEST.md` (`services:` block).
**Why Tier 3:** completely separate from brain↔host-agent jobs (which are OS-level). This is an app-platform feature; the bet is that "apps can offload async work to malmo" is a real differentiator vs. Umbrel/CasaOS. Pin the shape now so we don't accidentally make decisions in `SERVICE_PROVISIONING.md` that close it off; full design post-MVP.

### Web terminal in the dashboard

`SPEC.md` § Local access promises a "virtual terminal in the web UI" — a shell without leaving the browser. Needs design across:
- Protocol: WebSocket over the brain↔host-agent UNIX socket via HTTP upgrade (already locked in `BRAIN_HOST_PROTOCOL.md` Pattern D as the future shape).
- Auth: root PTY = root on the host. Default to dropping to the dashboard-user's Linux account; explicit "open a root shell" gesture gated by re-typing the dashboard password.
- UX: where it lives in the dashboard, history persistence, multi-session behavior.

**Context:** `BRAIN_HOST_PROTOCOL.md`, `AUTH.md`, `SPEC.md`.
**Why Tier 3:** load-bearing affordance for tinkerers, but not on the v1 critical path. Pinning shape now keeps the protocol's WebSocket reservation honest.

### Hooks — concrete shape for return

Decided in principle: when hooks return, they're **one-shot container images**, not in-container scripts (`DECISIONS.md` 2026-05-13). The concrete schema, timeout/failure handling, log surfacing, and brain-side execution model aren't specced.

**Context:** `APP_MANIFEST.md` § F, `APP_LIFECYCLE.md` § "Deferred: lifecycle hooks".

### Cert-expired UX

When a box has been offline long enough that `.malmo.network` certs expired: serve the expired cert with browser warning, transparently redirect to `.local`, or surface a banner in the dashboard.

**Context:** `MALMO_NETWORK.md` ("Failure modes").

### Reboot scheduling UX

"Reboot tonight at 3am OK?" prompt vs. silent within window. Surface only when blocked vs. always.

**Context:** `UPDATES.md`.

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

**Networking & cloud**
- `box-id` allocation scheme — word-pair vs. random hex + check digit. `MALMO_NETWORK.md`.
- DNS provider for the apex — Cloudflare free tier vs. self-hosted PowerDNS. `MALMO_NETWORK.md`.
- ACME DNS-01 plugin path — Caddy generic vs. malmo-specific plugin. `MALMO_NETWORK.md`.
- Privacy doc surface — what we log (DNS queries, enrollment metadata) and retention. `MALMO_NETWORK.md`.

**Isolation & runtime**
- GPU sharing across apps (MIG / time-slice / exclusive). `APP_ISOLATION.md`.
- macvlan on bonded / bridged host interfaces. `APP_ISOLATION.md`.
- Read-only root rollout as a catalog requirement. `APP_ISOLATION.md`.
- Egress allowlist for `internet: true`. `APP_ISOLATION.md`.
- Per-app firewall rules (apps as L4 endpoints). `APP_ISOLATION.md`.
- Cross-user shared-pool access (`shared_storage_access`). `APP_ISOLATION.md`.

**Storage & first-run**
- Boot-PIN high-security mode. `STORAGE.md`.
- Recovery-passphrase storage assistance ("email this to me", USB shard). `STORAGE.md`, `FIRST_RUN.md`.
- Removable drives auto-mount UX. `STORAGE.md`.
- Filesystem on extra drives (ext4 vs. accept existing NTFS/exFAT). `STORAGE.md`.
- OS-drive-only swap with data drive intact. `STORAGE.md`.
- TPM-less hardware fallback. `FIRST_RUN.md`.
- First-run on a box with pre-existing malmo data. `FIRST_RUN.md`.

**Updates**
- Critical-security flag override for auto-update-off apps. `UPDATES.md`.
- Metered-connection mode for app updates. `UPDATES.md`.
- Concurrent emergency updates across streams. `UPDATES.md`.
- Per-region / per-cohort rollouts. `UPDATES.md`.
- Concrete "stable" promotion criteria. `UPDATES.md`.

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

**Top-level**
- Redundancy implementation when Level 2 storage ships (btrfs vs. ZFS vs. mdadm). `SPEC.md`.
- ARM64 timeline. `SPEC.md`, `BUILD.md`.
- License for the OS itself. `SPEC.md`.
