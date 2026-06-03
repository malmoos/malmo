# molma Update Model

> Working spec for how a running molma box stays current. Companion to `SPEC.md`, `CONTROL_PLANE.md`, `BUILD.md`, `SERVICE_PROVISIONING.md`, `APP_MANIFEST.md`.

This doc is **draft / option-survey**. Most sections present alternatives with a recommendation; locked decisions are pulled out at the bottom. The intent is to surface forks before committing.

## What this doc covers

A box has **five independent update streams**, each with its own cadence, risk profile, and delivery mechanism:

1. **Debian base** — kernel, system libraries, firmware. Slow.
2. **`host-agent`** — Debian package from our apt repo. Rare.
3. **Control plane (`molma-brain` + `molma-ui`)** — two container images sharing one release manifest. Frequent. UI usually moves more often than brain; only what changed recreates.
4. **Apps** — Docker images + manifest versions, per app. Frequent and varied.
5. **Managed services** — Postgres/Redis container versions. Triggered by app manifests, not by us.

This doc spells out the policy for each, plus the cross-cutting concerns: scheduling, rollback, dependency ordering, failure handling.

It does **not** cover the eventual A/B immutable migration mechanics — that's a v2 design once the product has traction (`SPEC.md`).

---

## 1. Debian base

The OS underneath us — kernel, libc, OpenSSL, firmware, Docker itself.

### Options

- **A — `unattended-upgrades`, security-only.** Debian's stock auto-update. Pulls patches from `*-security` only. Conservative.
- **B — `unattended-upgrades`, full stable.** Same mechanism, broader scope (`stable`, `stable-updates`, `stable-security`). More fixes, more change surface.
- **C — Manual / admin-triggered only.** Settings → System → "Check for OS updates." User decides.
- **D — No automatic OS updates at all.** Lock to whatever shipped with the ISO; users reinstall to get a newer base.

### Recommendation: A — security-only auto-updates

- The "pantry laptop that just works" pitch (`SPEC.md`) requires security updates to apply without intervention. Most non-technical users will never click an update button.
- Full-stable auto-updates is the territory where `apt` actually breaks things. We deliberately scope to `*-security` to minimize that risk while still keeping the box patched.
- Larger upgrades (Debian point releases, dist-upgrade) stay manual / admin-triggered until A/B images land.

Pros:
- Standard Debian mechanism, well-understood, audited.
- Security floor without admin attention.

Cons:
- A bad security update can still brick boot. SPEC.md already accepts this as a v1 risk we cure with A/B images later.
- `unattended-upgrades` has corner cases (kernel updates leave old initrd, disk-full mid-upgrade) — Debian-standard problems with Debian-standard mitigations.

---

## 2. `host-agent`

Tiny native binary, supervises the brain (`CONTROL_PLANE.md`). Updates are rare — anything that changes often lives in the brain instead.

### Options

- **A — Auto-update via `unattended-upgrades` from our apt repo.** Same mechanism as the Debian base, just one more source list.
- **B — Brain orchestrates host-agent updates.** Brain detects a new version on `apt.molma.network`, downloads, calls a host-agent self-update endpoint.
- **C — Admin-triggered only.** Settings → "Update molma system."

### Recommendation: A — `unattended-upgrades` from our apt repo

- Boring, native, exactly what the apt machinery is for.
- host-agent shouldn't be updating itself while running — apt's preinst/postinst handle the systemd-unit restart cleanly.
- Brain orchestrating its own supervisor is a layering inversion we don't want.

Pros:
- Same plumbing as #1 — no new mechanism.
- apt's transactional model means partial-failure states are rare.

Cons:
- Coupled to apt cron schedule (typically nightly). New host-agent versions take up to 24h to roll out. Acceptable — it changes rarely.

---

## 3. Control plane — `molma-brain` + `molma-ui`

The control plane ships as **two container images** on **one release manifest**: `molma-brain` (the daemon) and `molma-ui` (the dashboard, per `WEB_UI.md`). Most weeks the UI moves and the brain doesn't; occasionally the brain moves and the UI doesn't; occasionally they move together (coordinated change requiring a new brain endpoint that the UI consumes).

This is the most user-visible update stream because the brain + UI together *are* molma from the user's perspective.

**One channel, two artifacts.** The user sees a single "auto-update molma" affordance. The updater pulls and recreates only what changed — UI-only ship recreates only `molma-ui`; brain-only ship recreates only `molma-brain`; coordinated ship recreates both as one transaction (pull both, recreate both, verify both healthy, commit; on failure, revert both).

### Options on update trigger

- **A — Auto-pull `latest` tag continuously.** Box polls registry, pulls when tag advances.
- **B — Release manifest.** Box polls a JSON manifest at `releases.molma.network/stable.json` that lists the *current* stable version. Gives us a kill switch (retract a bad release) and a place to gate rollouts if we later need pacing. See `RELEASE_MANIFEST.md` for the full schema + publishing pipeline.
- **C — Periodic prompt.** Box checks for updates, surfaces "molma X.Y.Z available — update now?" in the UI.
- **D — Fully manual.** Admin clicks update.

### Decision: B — release manifest, admin-prompted

- Release manifest (not raw `latest` tag) because we need a kill switch. If we ship a bad version and 5% of boxes start crashlooping, we want to flip the manifest back and stop *availability* of that version *now*, before more boxes prompt their admin to install it.
- Admin-prompted (not auto-applied) because v1 has no A/B rollback at the OS level. Phone-OS-style auto-apply assumes hardware-backed rollback we don't have until A/B images land. Surfacing "molma X.Y.Z is available" and waiting for the admin is the honest posture.
- The manifest names both `brain` and `ui` versions, plus `minimum_host_agent` and `rollback_to`. Full schema, signing (minisign / Ed25519), and publishing pipeline live in `RELEASE_MANIFEST.md`. v1 ships a single `stable` channel with no phased rollout — admin-prompting provides natural pacing at v1 scale.

The updater compares each named version against what's currently installed:

- `brain` unchanged + `ui` advanced → recreate only `molma-ui`. Brain keeps running; no API interruption.
- `brain` advanced + `ui` unchanged → recreate only `molma-brain`. UI keeps serving; brief API gap during brain restart (the in-tab `426` safety net per `BRAIN_UI_PROTOCOL.md` covers stale tabs).
- Both advanced → coordinated transaction: pull both, recreate both, verify both healthy, commit. On failure of either, revert both to the previous pair.

`rollback_to` is a *paired* rollback (brain + UI both revert together when fired), since brain/UI version pairs are tested together before publication. If set, the offer for the bad version is retracted from all boxes that haven't yet applied it; already-updated boxes see a "downgrade available" prompt that recommends reverting (using the kept-for-7-days snapshot). Cheap insurance. Full rollback semantics in `RELEASE_MANIFEST.md`.

For the silent (telemetry-off) population, our visibility comes from the same channels Ubuntu and Debian have always used: GitHub issues, support forum, direct reports. Slower than real-time metrics, sufficient for the appliance's risk profile at v1 scale.

### Phased rollout and beta channel — deferred

Both are deferred from v1 with explicit triggers documented in `RELEASE_MANIFEST.md` # "Future work" and # "Channels":

- **Phased rollout / cohorts** activates when A/B immutable images land and auto-apply becomes safe — admin-prompting no longer provides natural pacing. Schema is additive (`rollout` array + deterministic `hash(machine_id || canonical(brain, ui))` bucket).
- **Beta channel** reactivates when fleet growth outpaces direct-report detection, or when auto-apply lands. Additive — a new `beta.json` alongside `stable.json`, opt-in setting, no schema change.

### Update mechanics

1. host-agent polls the release manifest hourly.
2. If a newer manifest applies to this box (channel, host-agent compat), host-agent surfaces a "molma update available — vX.Y.Z" notification in the dashboard. Current versions keep running.
3. When the admin clicks **Update**, host-agent runs the changed-only transaction:
   a. Pull each image whose version moved (`molma-brain`, `molma-ui`, or both).
   b. **If brain moved:** snapshot the brain's SQLite database to `/var/lib/molma/brain-snapshots/<old-version>.db`. Cheap (SQLite is one file, single-digit MB at v1 scale).
   c. Recreate the changed containers in order: brain first (if changed), then UI. Brain restart is fast (~5–10s); UI container restart is faster.
   d. Wait up to 60s for `/healthz` on the brain and a simple HTTP probe on the UI.
4. **On health-check failure of either:** host-agent reverts **both** to the previous pair (revert images, restore SQLite snapshot if brain was changed), restarts. Surfaces the failure in the UI with a "rollback succeeded" status.
5. **On three consecutive failed update attempts to the same manifest:** host-agent pins to the last-known-good pair and stops re-prompting until the release manifest advances past the failing version (or `rollback_to` retracts it).

Keep the previous brain/UI image pair and SQLite snapshot for 7 days, then GC.

If the release manifest's `rollback_to` field retracts the currently-offered version before the admin has applied it, the prompt silently disappears. This is the kill switch in action — admin never sees an offer for a known-bad release.

### Update window

Control-plane updates are admin-triggered, so they apply when the admin clicks. There is no fixed window. Apps and managed-service patches still serialize to the 03:00–04:00 window (#4, #5).

Impact at apply time depends on what moved:

- **UI only:** ~1s of dashboard unavailability while the UI container restarts. Open tabs hit the in-tab `426` path on the next request and prompt the user to refresh.
- **Brain only:** ~5–10s of API unavailability. App routing continues (Caddy stays up; only the brain's API endpoints are briefly absent). Open tabs see a brief network error and recover on retry.
- **Coordinated (both):** ~10–15s. Admin sees a "this will take ~30s" notice before confirming.

---

## 4. Apps

`SPEC.md` locked: **automatic by default, per-app toggle off.** This section spells out what "automatic" means concretely and the one carve-out where we prompt.

### Auto-apply unless permissions expand

The trigger for prompting is **permission expansion**, not version bumps. Concretely, the brain diffs the new manifest's `permissions:` block against the running version's:

- New permission key (e.g., `devices` newly present) → prompt.
- Widened value (`internet: false → true`, new entry in `folders`, new entry in `devices`, `gpu: false → true`, mode upgrade `read → write` on an existing folder, etc.) → prompt.
- Same or narrower permissions → auto-apply, no prompt.

This means a Photos `1.4 → 2.0` bump that doesn't touch permissions auto-applies. A Photos `1.4 → 1.5` bump that adds `devices: [/dev/dri]` for hardware-accelerated thumbnails prompts.

Reasoning:
- New permissions are a trust event. Auto-granting `lan: true` because an app's `2.0` manifest declares it is a security regression. The user opted into the *app at the trust level it had*, not into a permission expansion.
- Tying the prompt to a concrete manifest diff (rather than a fuzzy "major" judgment by the author) means the policy is enforceable without catalog reviewers having to relitigate what counts as a major bump.
- Cross-major managed-service migrations (Postgres 15 → 16, per `SERVICE_PROVISIONING.md`) are *infrastructure*, not user-facing trust events — they happen transparently in the update window. The pre-migration backup is the safety net.

### Who gets prompted

**The user who owns the instance**, not the admin (unless they're the same person).

Tier-3 apps run as per-user instances (`APP_ISOLATION.md`). Each instance is the property of one user — their data, their network exposure, their managed-service credentials. The permission-expansion prompt goes to that user the next time they log in. The admin has no special claim over another user's instance and is not notified.

Consequence: **two users on the same box can run different versions of the same app for a while.** Maria has accepted the `lan: true` expansion in Photos 2.0; Andrei hasn't, so his instance is still on 1.4. This is fine — instances are already fully isolated (separate containers, separate volumes, separate managed-service DBs per `APP_ISOLATION.md` "Managed services placement"). There is no coordination required.

A user who declines the prompt stays pinned to their current version. Their instance keeps running. The prompt re-surfaces if they dismiss without choosing; they can also accept later from the app's Settings page.

Tier-2 apps (Tailscale, SMB, DLNA) are box-wide and admin-installed. Permission changes for Tier-2 prompt the admin. Tier-2 update flow is otherwise covered in `SERVICE_PROVISIONING.md`.

### Update window

Same as brain: **03:00–04:00 local** by default. App updates serialize one at a time within the window — never two concurrent app updates.

### Update mechanics per app

1. Pull new image.
2. **Snapshot the app's state** — see "Pre-update snapshot" below.
3. Run `pre_update` hook (`APP_MANIFEST.md`) — deferred from MVP; the snapshot is the v1 safety net.
4. If managed-service major version changed: take pre-migration backup (per `SERVICE_PROVISIONING.md`), spin up new major, `pg_dump | pg_restore`.
5. Stop old container, start new container with the same volumes.
6. Wait up to 120s for the app to respond on `main_port`.
7. **On failure:** restore from the pre-update snapshot, revert to previous image. If managed-service was migrated, revert to the previous major and restore from the pre-migration dump. Notify admin.

**Keep the previous image and snapshot for 7 days.** "App is broken since last night" is the realistic complaint and we want the rollback button to actually work.

### Pre-update snapshot

The single biggest gap in image-only rollback is **app-managed schema migrations**. A new app version starts up, alters tables / rewrites data-volume files as part of its boot migration, then fails health check. Image rollback alone leaves the *old* code running against *migrated* data — broken in a way restoring the image doesn't fix.

Until lifecycle hooks return (`APP_MANIFEST.md` # F, `APP_LIFECYCLE.md` # Deferred: lifecycle hooks), the brain takes a brute-force snapshot before every app update:

1. **Tar the manifest's declared `data_volumes`** to `/var/lib/molma/instances/<id>/snapshots/pre-update-<old-version>.tar`. `cache_volumes` are excluded — that's literally what the data/cache split is for (`APP_MANIFEST.md` # C).
2. **If the app uses a managed service**, `pg_dump` (or equivalent for the service type) the app's logical database into the same snapshot dir. Cheap, well-bounded, runs in the 03:00 window when nothing else is going on. Applies whether or not the service version moved — protects against app-driven schema changes inside the same major.
3. **Retain alongside the previous image for 7 days**, then GC.

On health-check failure of the new container, the brain stops the new container, restores the tar (and the logical DB dump if present), and starts the previous image. Single-generation rollback — enough for the one-step-back UX, no n-deep history in v1.

Cost is bounded: `data_volumes` are author-declared and typically small (indexes, configs, app DBs); the bulk of app state usually lives in `cache_volumes` and is excluded. Snapshot happens during the 03:00–04:00 window when nothing else is running. Disk pressure surfaces as a `disk-full` health issue per `HEALTH.md`; if the box is too full to take a snapshot, the update is deferred and the user is told.

When hooks return, `pre_update` (author-provided, app-aware) replaces the tar for apps that ship one. The brain's snapshot stays the safety net for apps that don't.

**App-side rollback hooks are deferred.** A `post_update_rollback` hook fired only when `post_update` fails is the right long-term shape for apps that need bespoke recovery, but it pushes complexity onto every author for a case the snapshot already handles. Sketched in `APP_MANIFEST.md` # F; not in v1.

### Per-app auto-update toggle

- Default ON (`SPEC.md`).
- Off means: never auto-update. Admin sees "X update available" badge, clicks to apply.
- Off does **not** mean "freeze the version" — security-classified updates (we'll need a flag in the manifest) still apply auto if the catalog marks them critical. Open question: do we ship that flag in v1, or honor "off" strictly? Lean strict in v1 — fewer surprises.

---

## 5. Managed services

Postgres, Redis, etc. Per `SERVICE_PROVISIONING.md`, brain owns lifecycle.

### What triggers a managed-service update

- **Patch within a major** (Postgres 15.4 → 15.5): brain pulls and restarts on its own update window. App-transparent.
- **New major requested** (an app's manifest now wants Postgres 16, brain only has 15 running): triggered by app update, follows the cross-major migration path in `SERVICE_PROVISIONING.md`.
- **Major retired** (last app on Postgres 15 is uninstalled): grace period, then shutdown.

### Update mechanics

Patch updates serialize per major-version instance. Brain stops the container, pulls new image, starts it. App connections drop and reconnect — handled by client retry logic in the apps.

Cross-major migrations are an app-update mechanic, not a managed-service-update mechanic. They live in #4.

**No user-visible toggle for managed-service updates.** The user didn't install Postgres; they installed Photos. Postgres patch updates are infrastructure, not a user concern.

---

## 6. Dashboard update UX

The mechanics above describe *what* happens. This section describes how the dashboard surfaces it.

### Where updates appear

Three surfaces, one mental model:

- **Per-app tile.** A small badge on the app's tile in the dashboard when its version moved (auto-applied overnight, or pending user decision). Click → "What's new" panel with the upstream changelog (sourced from the manifest's `links.support` or a `changelog_url` field — small additive field).
- **Settings → Updates.** Single aggregate view: "X apps updated last night, Y waiting on you, Z failed." This is where the rollback affordance lives (using the kept-for-7-days image + snapshot per # 4).
- **No global "update now" button.** Auto-updates serialize in the 03:00–04:00 window. The dashboard does not pretend the user controls cadence beyond the per-app toggle and the permission-expansion accept (below).

### The permission-expansion prompt

The only case the box asks. Surfaces **on next login of the instance owner**, as a modal on first dashboard load — not a dismissible banner. The app stays on its current version until the user decides.

- **Diff shown in plain language.** "Photos wants new access: **read & write your Movies folder**." Same vocabulary as the install screen (`APP_MANIFEST.md` # E), so the user recognizes it.
- **Two buttons: Allow & update / Keep current version.** No third "remind me later" — closing the modal is dismissal, and the prompt re-surfaces on the *next* login (not every page load).
- **Allow & update applies immediately**, not at 03:00. The user just made a deliberate decision; making them wait until tomorrow morning is confusing. The ~minute of app unavailability is the cost of the choice they explicitly initiated.
- **Accept later** is available from the app's Settings page.

Consequence (already noted in # 4): two users on the same box may run different versions of the same Tier-3 per-user app for a while. By design — instances are already per-user isolated.

### Admin visibility into other users' versions

The instance-owner prompt model means an admin can't directly see *why* Cindy's Photos is on a stale version. They can wonder why disk usage diverges, or why behavior differs across accounts.

Settings → Users → `cindy` exposes "Apps Cindy hasn't accepted updates for: Photos 2.0 (pending permission: read & write Movies)." Read-only — the admin sees the fact, but cannot accept on Cindy's behalf. Tier-3 per-user instances are Cindy's to authorize.

### Failure signaling

Auto-rollback already happens (# 4). The dashboard surfaces it:

- **Per-app tile banner**, persistent until acknowledged: "Photos couldn't update to 2.0 last night. Rolled back to 1.4."
- **Settings → Updates** lists the failure with mode (image pull / health check / hook / snapshot restore) and a "view logs" link to the diagnostic bundle (`LOGGING.md`).
- **After 3 consecutive failures** to the same manifest (# 4 mechanics), the prompt stops and the banner changes to "Update is failing repeatedly. Paused. See logs." The retry button is still available; the box just stops trying on its own.

### Post-update toast

A small, auto-dismissing toast on the next dashboard visit after an overnight update batch: "3 apps updated overnight" → click for the list with per-app "what's new" snippets. Not modal, not blocking. Auto-dismisses after one view.

### Notification center

The update outcomes surfaced here also fan out to the dashboard notification center (`NOTIFICATIONS.md`), routed per the actionability + ownership rule: **OS / host-agent / brain+UI updates → admins only**; **app auto-update / permission-approval-pending / failed-rollback → the instance owner** (box-wide Tier-2 apps → admins), never broadcast to all users. The per-app tile badge, Settings → Updates view, and post-update toast remain the in-context surfaces; the notification is the durable, read-stateful copy for the user who wasn't looking when it happened.

### What lives in `APP_MANIFEST.md`

Additive fields the manifest grows to support this UX:

- **`changelog_url`** — optional pointer to a per-version changelog. If absent, the dashboard links to `links.support`.

No other UX-driven manifest fields in v1.

---

## 7. Cross-cutting concerns

### Update ordering

When multiple streams have updates pending in the same window:

```
host-agent  →  molma-brain  →  apps & managed services  →  Debian base
```

Reasoning:
- host-agent must support whatever brain version comes next (declared in the release manifest).
- Brain must support the manifest_version of any app coming next.
- Debian base updates last because they often want a reboot, and we'd rather reboot once at the end of the window than mid-flight.

### Reboots

Debian base updates set `/var/run/reboot-required` when applicable. Policy:

- **Reboot opportunistically in the update window** if the marker is set and no app is mid-update.
- **Otherwise wait.** Don't reboot during the day.
- After 7 days of a pending reboot, surface "your molma needs to restart" in the dashboard, but never force.

Reboot at v1 means roughly 30–60s of full unavailability. Acceptable nightly, hostile mid-day.

### Compatibility matrix

The release manifest (#3) carries `minimum_host_agent`. The brain carries `minimum_manifest_version` and `maximum_manifest_version` for apps. host-agent carries `minimum_brain_version`.

If an app update wants a manifest_version newer than the running brain supports, the brain refuses the update and surfaces "molma needs to update first" in the UI. The next brain update should resolve it; if it doesn't, the app stays pinned.

This means a misalignment never silently breaks something — it parks the update with a clear reason.

### Network requirements

All update streams require internet. **An offline box stays on its current versions indefinitely.** This is correct behavior, not a bug — local-first is a design property (`SPEC.md`).

When the box reconnects after an offline stretch, updates resume on the next scheduled window. We do **not** rush an immediate update on reconnect (avoids "I just plugged it in, why is it updating?").

### Telemetry and rollout health

When telemetry is enabled (`FIRST_RUN.md` opt-in), boxes report:
- Successful update completion per stream.
- Update failures with the failure mode (image pull, health check, hook).
- Crash counts per brain version.

Telemetry is a **signal that accelerates our reaction time**, not a gate. Boxes with telemetry off get the same updates and the same protection (manifest applies to everyone; `rollback_to` retracts a bad release fleet-wide) — they just don't contribute signal. When phased rollout activates post-v1, the schedule will be time-based — telemetry will let us halt or trigger `rollback_to` faster than we'd otherwise notice, not gate advancement.

### Rollback summary

| Stream | Rollback mechanism |
|---|---|
| Debian base | None in v1; A/B images later |
| `host-agent` | apt revert (manual, rare path) |
| `molma-brain` + `molma-ui` | Previous image pair + SQLite snapshot; revert as a pair, automatic on health-check fail of either |
| App | Previous image + pre-update tar of `data_volumes` (+ `pg_dump` of managed-service DB if any), automatic on health-check fail; keep 7 days |
| Managed service (patch) | Previous image; data is shared so this is a tag-flip |
| Managed service (major migration) | Pre-migration dump, automatic on app-update fail |

The Debian-base "no rollback" is the v1 hole we accept. Everything else has a defined revert path.

---

## Locked decisions

- **Five independent update streams**, each with its own policy.
- **Two-track posture, modeled after Android:** silent auto-apply for security patches; admin-prompted for anything that changes meaningful surface (brain, app permissions, OS major upgrades).
- **Debian base: `unattended-upgrades` security-only.** Full upgrades and Debian point-releases stay admin-triggered until A/B images.
- **`host-agent`: `unattended-upgrades` from our apt repo.**
- **Control plane (`molma-brain` + `molma-ui`): release-manifest-driven, admin-prompted.** Manifest carries `brain`, `ui`, `minimum_host_agent`, and `rollback_to` (full schema + signing + publishing pipeline in `RELEASE_MANIFEST.md`). v1 ships a single `stable` channel; phased rollout and beta channel are deferred (additive when triggers fire — see `RELEASE_MANIFEST.md` # Future work). Telemetry is a halt-fast signal, not a rollout gate. Updater recreates only what changed; brain+UI revert as a pair on failure.
- **Apps: auto-update by default** (per `SPEC.md`); **prompt the instance owner only when the manifest's `permissions:` block expands** (new key or widened value). Permission-neutral updates of any size auto-apply. Different users on the same box may temporarily run different versions of the same app — by design, since instances are already per-user isolated. Tier-2 apps prompt the admin (box-wide).
- **Pre-update snapshot of `data_volumes` (plus `pg_dump` of any managed-service DB)** is taken before every app update. Restored on health-check failure alongside the image revert. Hooks remain deferred; the snapshot is the v1 safety net for app-driven schema migrations. Kept 7 days.
- **Permission-expansion prompt surfaces on next login of the instance owner**, modal on first dashboard load, two buttons (Allow & update / Keep current version). Accept applies immediately, not at 03:00. Admin sees per-user pending-update facts in Settings → Users; cannot accept on another user's behalf.
- **Update surfaces in the dashboard:** per-app tile badge for available/applied/failed; Settings → Updates for the aggregate view and rollback affordance; auto-dismissing toast for overnight batches.
- **Managed services: brain-owned, no user toggle.** Patches in update window; cross-major migrations triggered transparently by app updates with a pre-migration backup.
- **Update window: 03:00–04:00 local** for apps, managed services, Debian base, reboots. Configurable, advanced setting. Brain has no fixed window — it's admin-triggered.
- **Update ordering: host-agent → brain → apps & managed services → Debian base.**
- **Reboots: opportunistic in window only.** Surface a dashboard nag after 7 days; never force.
- **Rollback: previous image + state snapshot kept for 7 days** for brain, apps, and managed-service patches. Debian base has no rollback in v1.
- **All updates require internet; offline boxes stay current at their last-applied versions.**

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
