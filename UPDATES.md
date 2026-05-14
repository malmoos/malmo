# malmo Update Model

> Working spec for how a running malmo box stays current. Companion to `SPEC.md`, `CONTROL_PLANE.md`, `BUILD.md`, `SERVICE_PROVISIONING.md`, `APP_MANIFEST.md`.

This doc is **draft / option-survey**. Most sections present alternatives with a recommendation; locked decisions are pulled out at the bottom. The intent is to surface forks before committing.

## What this doc covers

A box has **five independent update streams**, each with its own cadence, risk profile, and delivery mechanism:

1. **Debian base** — kernel, system libraries, firmware. Slow.
2. **`host-agent`** — Debian package from our apt repo. Rare.
3. **Control plane (`malmo-brain` + `malmo-ui`)** — two container images sharing one release manifest. Frequent. UI usually moves more often than brain; only what changed recreates.
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
- **B — Brain orchestrates host-agent updates.** Brain detects a new version on `apt.malmo.network`, downloads, calls a host-agent self-update endpoint.
- **C — Admin-triggered only.** Settings → "Update malmo system."

### Recommendation: A — `unattended-upgrades` from our apt repo

- Boring, native, exactly what the apt machinery is for.
- host-agent shouldn't be updating itself while running — apt's preinst/postinst handle the systemd-unit restart cleanly.
- Brain orchestrating its own supervisor is a layering inversion we don't want.

Pros:
- Same plumbing as §1 — no new mechanism.
- apt's transactional model means partial-failure states are rare.

Cons:
- Coupled to apt cron schedule (typically nightly). New host-agent versions take up to 24h to roll out. Acceptable — it changes rarely.

---

## 3. Control plane — `malmo-brain` + `malmo-ui`

The control plane ships as **two container images** on **one release manifest**: `malmo-brain` (the daemon) and `malmo-ui` (the dashboard, per `WEB_UI.md`). Most weeks the UI moves and the brain doesn't; occasionally the brain moves and the UI doesn't; occasionally they move together (coordinated change requiring a new brain endpoint that the UI consumes).

This is the most user-visible update stream because the brain + UI together *are* malmo from the user's perspective.

**One channel, two artifacts.** The user sees a single "auto-update malmo" affordance. The updater pulls and recreates only what changed — UI-only ship recreates only `malmo-ui`; brain-only ship recreates only `malmo-brain`; coordinated ship recreates both as one transaction (pull both, recreate both, verify both healthy, commit; on failure, revert both).

### Options on update trigger

- **A — Auto-pull `latest` tag continuously.** Box polls registry, pulls when tag advances.
- **B — Release manifest.** Box polls a JSON manifest at `releases.malmo.network/stable.json` that lists the *current* stable version. Lets us stage rollouts, halt them if telemetry goes bad, gate by cohort/region.
- **C — Periodic prompt.** Box checks for updates, surfaces "malmo X.Y.Z available — update now?" in the UI.
- **D — Fully manual.** Admin clicks update.

### Decision: B — release manifest, admin-prompted

- Release manifest (not raw `latest` tag) because we need a kill switch. If we ship a bad version and 5% of boxes start crashlooping, we want to flip the manifest back and stop *availability* of that version *now*, before more boxes prompt their admin to install it.
- Admin-prompted (not auto-applied) because v1 has no A/B rollback at the OS level. Phone-OS-style auto-apply assumes hardware-backed rollback we don't have until A/B images land. Surfacing "malmo X.Y.Z is available" and waiting for the admin is the honest posture.
- The manifest carries per-version metadata: minimum host-agent version, minimum app manifest_version supported, rollout cohort, deprecated flags.

```json
{
  "channel": "stable",
  "brain": "1.4.2",
  "ui": "1.4.9",
  "minimum_host_agent": "1.0.0",
  "released_at": "2026-05-08T12:00:00Z",
  "rollout": [
    { "after": "0d",  "percent": 5 },
    { "after": "2d",  "percent": 25 },
    { "after": "4d",  "percent": 50 },
    { "after": "7d",  "percent": 100 }
  ],
  "rollback_to": null
}
```

The manifest names **both** versions. The updater compares each against what's currently installed:

- `brain` unchanged + `ui` advanced → recreate only `malmo-ui`. Brain keeps running; no API interruption.
- `brain` advanced + `ui` unchanged → recreate only `malmo-brain`. UI keeps serving; brief API gap during brain restart (the in-tab `426` safety net per `BRAIN_UI_PROTOCOL.md` covers stale tabs).
- Both advanced → coordinated transaction: pull both, recreate both, verify both healthy, commit. On failure of either, revert both to the previous pair.

`rollback_to` is a *paired* rollback (brain + UI both revert together when fired), since brain/UI version pairs are tested together before publication.

**Cohort selection is deterministic and local.** Each box hashes `machine_id + version` into a 0–99 bucket and checks "is my bucket below today's percentage?" Same approach Ubuntu's phased apt updates use. No coordination with our servers required beyond fetching the manifest.

`rollback_to: "1.4.1"` is the kill-switch field — if set, the offer for `1.4.2` is retracted from all boxes that haven't yet applied it. Boxes already on `1.4.2` see a "downgrade available" prompt that recommends reverting (using the kept-for-7-days snapshot). Cheap insurance.

### Why time-based, not metric-gated

We considered telemetry-gated rollouts (advance only when crash rate stays under X%). Rejected for v1 on two grounds:

1. **Telemetry is opt-in** (`FIRST_RUN.md`). We cannot rely on data we don't have from users who declined.
2. **Scale.** At launch the total install base is in the hundreds. "5% of 200" is 10 boxes — too small for statistical confidence in either direction.

So the rollout schedule is **time-based**, the same shape Ubuntu has used at much larger scale for a decade. Telemetry, where present, is a **signal that lets us halt or roll back early** — not a precondition for advancing. If telemetry-enabled boxes start reporting crash loops on a new version, we flip `rollback_to` and freeze the rollout. Boxes with telemetry off get the same protection because the manifest applies to everyone.

For the silent (telemetry-off) population, our visibility comes from the same channels Ubuntu and Debian have always used: GitHub issues, support forum, beta-tester reports. Slower than real-time metrics, sufficient for the appliance's risk profile.

### Bake on beta before promoting to stable

A brain version is published to the **beta channel** first and bakes there for **7–14 days** before promoting to `stable`. Beta is opt-in (advanced Setting); the population is self-selected tinkerers who are tolerant of breakage and good at filing reports. Beta is, statistically, our pre-rollout signal — telemetry-on or telemetry-off.

Promotion criteria are deliberately soft for v1: no critical bugs filed, no auto-rollback triggers fired on beta boxes, subjective sanity check by us. We tighten this once we have data on what "healthy bake" actually looks like at our scale.

### Update mechanics

1. host-agent polls the release manifest hourly.
2. If a newer manifest applies to this box (rollout cohort, channel, host-agent compat), host-agent surfaces a "malmo update available — vX.Y.Z" notification in the dashboard. Current versions keep running.
3. When the admin clicks **Update**, host-agent runs the changed-only transaction:
   a. Pull each image whose version moved (`malmo-brain`, `malmo-ui`, or both).
   b. **If brain moved:** snapshot the brain's SQLite database to `/var/lib/malmo/brain-snapshots/<old-version>.db`. Cheap (SQLite is one file, single-digit MB at v1 scale).
   c. Recreate the changed containers in order: brain first (if changed), then UI. Brain restart is fast (~5–10s); UI container restart is faster.
   d. Wait up to 60s for `/healthz` on the brain and a simple HTTP probe on the UI.
4. **On health-check failure of either:** host-agent reverts **both** to the previous pair (revert images, restore SQLite snapshot if brain was changed), restarts. Surfaces the failure in the UI with a "rollback succeeded" status.
5. **On three consecutive failed update attempts to the same manifest:** host-agent pins to the last-known-good pair and stops re-prompting until the release manifest advances past the failing version (or `rollback_to` retracts it).

Keep the previous brain/UI image pair and SQLite snapshot for 7 days, then GC.

If the release manifest's `rollback_to` field retracts the currently-offered version before the admin has applied it, the prompt silently disappears. This is the kill switch in action — admin never sees an offer for a known-bad release.

### Update window

Control-plane updates are admin-triggered, so they apply when the admin clicks. There is no fixed window. Apps and managed-service patches still serialize to the 03:00–04:00 window (§4, §5).

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
- Widened value (`internet: false → true`, new entry in `shared_storage`, new entry in `devices`, `gpu: false → true`, etc.) → prompt.
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
2. Run `pre_update` hook (`APP_MANIFEST.md`).
3. If managed-service major version changed: take pre-migration backup (per `SERVICE_PROVISIONING.md`), spin up new major, `pg_dump | pg_restore`.
4. Stop old container, start new container with the same volumes.
5. Wait up to 120s for the app to respond on `main_port`.
6. **On failure:** revert to previous image. If managed-service was migrated, revert to the previous major and restore from the pre-migration dump. Notify admin.

**Keep the previous image for 7 days.** "App is broken since last night" is the realistic complaint and we want the rollback button to actually work.

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

Cross-major migrations are an app-update mechanic, not a managed-service-update mechanic. They live in §4.

**No user-visible toggle for managed-service updates.** The user didn't install Postgres; they installed Photos. Postgres patch updates are infrastructure, not a user concern.

---

## 6. Cross-cutting concerns

### Update ordering

When multiple streams have updates pending in the same window:

```
host-agent  →  malmo-brain  →  apps & managed services  →  Debian base
```

Reasoning:
- host-agent must support whatever brain version comes next (declared in the release manifest).
- Brain must support the manifest_version of any app coming next.
- Debian base updates last because they often want a reboot, and we'd rather reboot once at the end of the window than mid-flight.

### Reboots

Debian base updates set `/var/run/reboot-required` when applicable. Policy:

- **Reboot opportunistically in the update window** if the marker is set and no app is mid-update.
- **Otherwise wait.** Don't reboot during the day.
- After 7 days of a pending reboot, surface "your malmo needs to restart" in the dashboard, but never force.

Reboot at v1 means roughly 30–60s of full unavailability. Acceptable nightly, hostile mid-day.

### Compatibility matrix

The release manifest (§3) carries `minimum_host_agent`. The brain carries `minimum_manifest_version` and `maximum_manifest_version` for apps. host-agent carries `minimum_brain_version`.

If an app update wants a manifest_version newer than the running brain supports, the brain refuses the update and surfaces "malmo needs to update first" in the UI. The next brain update should resolve it; if it doesn't, the app stays pinned.

This means a misalignment never silently breaks something — it parks the update with a clear reason.

### Network requirements

All update streams require internet. **An offline box stays on its current versions indefinitely.** This is correct behavior, not a bug — local-first is a design property (`SPEC.md`).

When the box reconnects after an offline stretch, updates resume on the next scheduled window. We do **not** rush an immediate update on reconnect (avoids "I just plugged it in, why is it updating?").

### Telemetry and rollout health

When telemetry is enabled (`FIRST_RUN.md` opt-in), boxes report:
- Successful update completion per stream.
- Update failures with the failure mode (image pull, health check, hook).
- Crash counts per brain version.

Telemetry is a **signal that accelerates our reaction time**, not a gate that the rollout schedule depends on. The rollout is time-based (§3); telemetry just lets us halt or trigger `rollback_to` faster than we'd otherwise notice. Boxes with telemetry off get the same updates on the same schedule and the same protection — they just don't contribute signal.

### Rollback summary

| Stream | Rollback mechanism |
|---|---|
| Debian base | None in v1; A/B images later |
| `host-agent` | apt revert (manual, rare path) |
| `malmo-brain` + `malmo-ui` | Previous image pair + SQLite snapshot; revert as a pair, automatic on health-check fail of either |
| App | Previous image, automatic on health-check fail; keep 7 days |
| Managed service (patch) | Previous image; data is shared so this is a tag-flip |
| Managed service (major migration) | Pre-migration dump, automatic on app-update fail |

The Debian-base "no rollback" is the v1 hole we accept. Everything else has a defined revert path.

---

## Locked decisions

- **Five independent update streams**, each with its own policy.
- **Two-track posture, modeled after Android:** silent auto-apply for security patches; admin-prompted for anything that changes meaningful surface (brain, app permissions, OS major upgrades).
- **Debian base: `unattended-upgrades` security-only.** Full upgrades and Debian point-releases stay admin-triggered until A/B images.
- **`host-agent`: `unattended-upgrades` from our apt repo.**
- **Control plane (`malmo-brain` + `malmo-ui`): release-manifest-driven, admin-prompted.** Manifest carries `brain`, `ui`, `minimum_host_agent`, a time-based `rollout` schedule, and `rollback_to`. Cohort selection is deterministic from `machine_id + manifest hash` — Ubuntu-style phased rollout, no server coordination. Bake on beta channel for 7–14 days before promoting to stable. Telemetry is a halt-fast signal, not a rollout gate. Updater recreates only what changed; brain+UI revert as a pair on failure.
- **Apps: auto-update by default** (per `SPEC.md`); **prompt the instance owner only when the manifest's `permissions:` block expands** (new key or widened value). Permission-neutral updates of any size auto-apply. Different users on the same box may temporarily run different versions of the same app — by design, since instances are already per-user isolated. Tier-2 apps prompt the admin (box-wide).
- **Managed services: brain-owned, no user toggle.** Patches in update window; cross-major migrations triggered transparently by app updates with a pre-migration backup.
- **Update window: 03:00–04:00 local** for apps, managed services, Debian base, reboots. Configurable, advanced setting. Brain has no fixed window — it's admin-triggered.
- **Update ordering: host-agent → brain → apps & managed services → Debian base.**
- **Reboots: opportunistic in window only.** Surface a dashboard nag after 7 days; never force.
- **Rollback: previous image + state snapshot kept for 7 days** for brain, apps, and managed-service patches. Debian base has no rollback in v1.
- **All updates require internet; offline boxes stay current at their last-applied versions.**

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
