# malmo Health & Degraded Mode

> Working spec for how the brain represents and surfaces things-that-are-wrong without refusing to run. The model that lets the dashboard always come up, explain the problem in plain language, and walk the user through the fix.
>
> Companion to `BOOT.md` (the boot chain produces the conditions this doc handles), `CONTROL_PLANE.md` (the brain owns the health-issue state), `BRAIN_UI_PROTOCOL.md` (the wire shape for surfacing issues to the UI), `WEB_UI.md` (how banners and disabled actions render), `AUTH.md` (admin vs. member visibility of issues).

## Stance

malmo's user is not a sysadmin. The failure-mode ranking for that audience is:

1. **Silent data corruption** — catastrophic, but rare, and they won't notice until backup-restore time.
2. **Box bricks (no UI, no obvious next step)** — also catastrophic, *and* they notice in 30 seconds. The "I'm returning it" moment.
3. **Box boots but something is degraded, with a banner explaining what** — annoying but survivable. They can act.

A naive hardening posture optimizes for (1) by halting at the systemd level on any anomaly. For malmo's audience, that ranking is wrong: a brick costs more reputation than the rare corruption case it prevents, especially because most "corruption" risks are recoverable if caught early — the files are usually still *there*, just inaccessible or in the wrong place.

**The principle: if the brain can possibly run, it runs. Anomalies become health issues the user sees in the dashboard, not refusals to boot. The systemd-level recovery target exists only for the cases where the brain genuinely cannot start.**

This is closer to Synology's model than TrueNAS's: one UI, three states (healthy / degraded / failed), with remediation surfaced inline. Not "drop to a separate recovery interface."

## The health-issue model

The brain maintains a set of **health issues**. Each issue is a typed record the brain emits when a condition holds, removes when the condition clears, and exposes via the API so the dashboard can surface and act on it.

### Issue shape

```
{
  "id":              "data-drive-missing",   // stable string, single issue type
  "instance_key":    null,                   // optional — e.g. app id, drive id, user id
  "category":        "storage",              // storage | state | network | version | capacity
  "severity":        "error",                // warning | error | critical
  "tier":            2,                      // remediation tier (1, 2, or 3) — see below

  "blocks_writes":   true,                   // refuse writes to user data?
  "blocks_apps":     true,                   // refuse to start / install apps?
  "blocks_users":    true,                   // refuse to create / modify users?

  "summary":         "Your data drive isn't connected.",
  "details":         "malmo expected the data drive enrolled on 2026-04-12 (UUID abc-123) but it isn't currently attached.",

  "actions": [
    { "label": "Try detecting it again",        "endpoint": "/api/v1/health/data-drive-missing/retry",       "tier": 1 },
    { "label": "Set up without this drive",     "endpoint": "/api/v1/health/data-drive-missing/forget",      "tier": 2, "destructive": true }
  ],

  "raised_at":       "2026-05-16T08:14:22Z",
  "last_checked_at": "2026-05-16T08:14:50Z"
}
```

The brain decides which flags apply per issue type. The UI does not infer; it reads.

### What the flags mean

- `blocks_writes` — the brain refuses any operation that would write user data (file uploads, share writes, app data writes). Reads stay available.
- `blocks_apps` — the brain refuses to start a stopped app, install a new app, or update an app. Already-running apps are not killed (that's a separate decision the user can take from the UI).
- `blocks_users` — the brain refuses to create new users, change passwords, change group membership.

A given issue may set zero, one, or several of these. The brain's operation gates are simply `!any(i.blocks_X for i in active_issues)`.

**Conservative-but-not-blunt:** when in doubt, block. But blocking everything is just "recovery mode with extra steps." The art is blocking the right things — see the taxonomy below.

## Remediation tiers

Every action attached to an issue declares a tier. Tiers describe **who does the work**:

- **Tier 1 — Physical action by the user, UI detects completion.** The UI explains what to do in plain language; the brain notices when the condition clears (typically via udev events, periodic checks, or push notifications from host-agent).
  Example: "Plug your data drive back in." → udev fires → storage re-verifies → issue clears, toast confirms reconnection.

- **Tier 2 — UI-driven remediation, one click.** The UI offers buttons; the brain (via host-agent) executes the fix. The user makes a decision; the box does the work.
  Example: "[Restore from backup (last good: 2 hours ago)]" → brain runs a job → on success, issue clears.

- **Tier 3 — SSH or console rescue.** Genuinely rare. These are the cases where the brain itself can't run, so there's no UI to drive a fix from. Tier 3 issues do **not** appear in the brain's health set (the brain isn't running); they're handled by `malmo-recovery.target` (`BOOT.md` # Failure → recovery target — the narrow cases) or by physical console access for LUKS unlock.

**Rule: every issue in the taxonomy carries the lowest tier its remediation can be expressed at. If a new issue lands in Tier 3, that's a design red flag — figure out whether you can pull it back to Tier 2 with better tooling. Tier 3 stays tiny.**

**Rule: every banner has a primary action.** Even Tier 1 issues offer a no-op-but-helpful "Retry detection" button. A banner without an action is a status display the user can only watch, which makes the UI feel broken. Banners with actions feel like tools.

## Taxonomy

These are the issue types the brain knows about at v1. New issues are added by code change (each is a registered type in the brain), not declared in config. Adding new types is cheap; the categories and flag semantics are the contract.

### Storage

| ID | Severity | Blocks | Tier | Summary |
|---|---|---|---|---|
| `data-drive-missing` | error | writes, apps, users | 1 | Data drive enrolled but not currently attached. |
| `data-drive-wrong` | critical | writes, apps, users | 2 | A drive is attached, but its UUID doesn't match the enrolled drive. |
| `data-drive-readonly` | critical | writes, apps | 1 | Kernel mounted the data drive read-only (filesystem error suspected). |
| `disk-nearly-full` | warning | (new installs only) | 1 | A drive is above its usage warning threshold (90% data, 85% OS). |
| `disk-full` | critical | writes, apps | 1/2 | A drive crossed its hard threshold (95% on either drive). Tier-1 action: free space by removing files (links to Files / app instance breakdown); Tier-2: eject-and-replace flow (Settings → Storage → Eject) for upgrading to a bigger drive. |
| `canary-mismatch` | critical | writes, apps | 2 | Storage assembly succeeded but the canary check failed — almost certainly a malmo bug. |
| `mergerfs-assembly-failed` | error | writes, apps | 2 | Mergerfs could not assemble the union across the data branches. |

### State

| ID | Severity | Blocks | Tier | Summary |
|---|---|---|---|---|
| `brain-db-corrupt` | critical | nearly all ops | 2 | SQLite integrity check failed; brain is operating in a minimal-functionality mode. |
| `bootstrap-state-mismatch` | critical | nearly all ops | 2 | The box thinks it's bootstrapped but the brain database is missing — typically a data-drive swap. |
| `schema-migration-failed` | critical | apps, writes | 2 | A brain schema migration did not complete; the previous brain version is still callable for rollback. |

### Version

| ID | Severity | Blocks | Tier | Summary |
|---|---|---|---|---|
| `version-mismatch` | error | apps | 2 | host-agent and brain versions are not the lockstep pair they should be. |
| `app-image-partial` | warning | (that app only) | 2 | An app image download was interrupted and is incomplete. |

### Network

| ID | Severity | Blocks | Tier | Summary |
|---|---|---|---|---|
| `mdns-down` | warning | nothing | 1 | Avahi isn't publishing — the box may not appear as `malmo.local`. |
| `hostname-conflict` | warning | nothing | 2 | Another device on the LAN already claims `malmo.local`; Avahi renamed our host (e.g. `malmo-2.local`). Admin picks a new hostname from Settings → System → Network. See `DISCOVERY.md`. |
| `lan-unreachable` | warning | nothing | 1 | network-online.target didn't reach in time; LAN reachability from other devices may be delayed. |
| `clock-not-synced` | warning | nothing | 2 | chrony hasn't synced in 6h or offset > 10s. Gates Let's Encrypt renewal within 7 days of expiry; surfaces in Settings → System → Time. See `TIME.md`. |

### Capacity & informational

Issues that exist to be visible without blocking anything: `update-available`, `backup-overdue`, `tls-cert-near-expiry`. Same shape; all flags false; just surfaced as informational banners.

**The set is intentionally bounded.** ~15 well-named issues at v1, not 60. New failure modes map onto an existing issue or earn a new entry here; both paths go through a code change to the brain.

## Lifecycle of an issue

Issues are *facts the brain holds* — they're created by detection, kept until the underlying condition clears, and may have actions taken against them.

### Raise

Issues are raised by:

- **Boot-time reporters.** `malmo-storage-verify` (`BOOT.md`) and similar boot-chain services write findings to a known path (e.g., `/run/malmo/health/storage.json`). The brain reads this at startup and converts findings into issues.
- **Periodic checks.** The brain runs a small set of background checks (SQLite integrity on a timer, disk usage every N minutes, certificate expiry daily). Anomalies become issues.
- **Reactive signals.** udev events for drive add/remove, host-agent push events for service state, container events from Docker.

Detection is deliberately conservative: the brain does not raise an issue on transient noise (e.g., one failed health probe). A retry-with-backoff or N-of-M policy lives inside each detector.

### Display

The brain exposes the live set via `GET /api/v1/health/issues` and streams transitions over the global SSE event channel (`BRAIN_UI_PROTOCOL.md`). The dashboard subscribes once at mount and surfaces:

- A **global banner** in the dashboard chrome when any `critical` or `error` issue is active. Click → dedicated Issues view.
- **Inline cards** in the relevant section (Storage page for storage issues, Updates page for version issues, per-app card for `app-image-partial`, etc.).
- **Disabled action affordances** with explanatory tooltips wherever a blocked operation lives ("Install app" greyed out → tooltip: "Disabled because: data drive isn't connected").

The dashboard does not pop a modal for any issue. Issues surface in-place; the user reads them when they look.

### Clear

Each issue has a clearing condition the brain re-checks. For most issues this is the inverse of the raise condition (drive present → clear `data-drive-missing`; disk usage drops below threshold → clear `disk-nearly-full`). For issues where the user invoked a Tier-2 action, the action's success is the clearing event.

When an issue clears, the dashboard shows a brief toast ("Data drive reconnected") so the user *sees* the transition — silent auto-recovery is avoided. Toasts are the only place the brain interrupts the user; banners and inline cards wait to be looked at.

### Persistence

Active issues live in the brain's SQLite (`health_issues` table). They survive brain restarts. The history of raises and clears is in the `audit_events` table (`LOGGING.md`) — useful for diagnostic bundles and support flows.

## What the brain refuses to do

Across all active issues, the brain blocks operations whose preconditions don't hold:

- `blocks_writes=true` (any issue) → all write APIs to user-data paths return `409 Conflict` with `{code: "blocked-by-health-issue", issue_id: "...", message: "..."}`. SMB writes are refused at the share level (share present, ACL denies writes). Reads stay available.
- `blocks_apps=true` (any issue) → `POST /api/v1/apps`, `POST /api/v1/apps/:id/start`, `POST /api/v1/apps/:id/update` all return `409`. `POST /api/v1/apps/:id/stop` stays available — the user can always reduce activity.
- `blocks_users=true` (any issue) → `POST /api/v1/users`, password change, group membership changes return `409`. Login remains available.

**No override path on critical blocks.** The user cannot "install this app anyway" through a confirm dialog while `data-drive-missing` is active. The block is the protection; an override is an option the user will use, blame malmo for, and remember. They'll resent the block once; they'll thank us the second time.

## What stays available in every degraded state

The non-negotiable invariant — the dashboard is the user's tool to recover the box, so it works no matter what:

- **The dashboard loads.** Always. Even with `brain-db-corrupt`, a minimal "your brain database is corrupt — here are your options" view comes up.
- **Login works** in all degraded states. PAM is independent of brain state (`AUTH.md`).
- **System logs are viewable** by admins. Required for any support flow.
- **A "diagnostic bundle" download** is available — zipped logs + state + issue set + storage view, for offline support.
- **The user's files** are reachable in whatever way is still safe. SMB stays up read-only if the data drive went read-only. Files-browsing in the UI stays up.
- **One-click actions for raised issues** are always callable (subject to the usual auth — admins only for system-level actions).

## What's deliberately out of scope here

- **The recovery dashboard UI itself.** Recovery mode (the static page served by `malmo-recovery.target` when the brain can't run at all) is its own surface, specced in the deferred `RECOVERY.md` (`NEXT.md`). HEALTH.md owns the brain-is-running-but-unhappy story; RECOVERY.md owns the brain-can't-run story.
- **Auto-remediation.** The brain detects, blocks, and surfaces. It does not autonomously format, restore, or roll back. Every Tier-2 action is user-initiated. The single exception is host-agent's bounded restart policy (5 starts in 60s, then route to recovery) — that's a systemd-level safety net, not "remediation."
- **Severity escalation over time.** A warning doesn't become an error just because it's been raised for a week. Severities are properties of the condition, not of its age.
- **Cross-issue interactions.** Issues are independent. If two issues both set `blocks_apps`, the resulting block is the union; there's no priority among issues.

## Knock-ons to other docs

- **`BOOT.md`** — `malmo-storage-ready.target` is *best-effort assembly*, not a strict gate. Individual storage-unit failures don't activate `malmo-recovery.target`; they write findings to `/run/malmo/health/` which the brain reads on startup. `malmo-recovery.target` shrinks to two cases: TPM unseal failure on the root drive (brain can't start because the system can't boot), and host-agent itself broken (brain can't start because nothing launches it).
- **`STORAGE.md`** — the canary check and enrollment-marker mismatch are *reporters* now, not gatekeepers. Their findings become health issues, not boot failures.
- **`CONTROL_PLANE.md`** — host-agent's systemd ordering uses `After=malmo-storage-ready.target Wants=malmo-storage-ready.target`, not `Requires=`. host-agent always starts (so the brain always starts), reads storage findings, and acts accordingly.
- **`BRAIN_UI_PROTOCOL.md`** — new `/api/v1/health/issues` endpoint family and new event `kind`s (`health.issue_raised`, `health.issue_cleared`, `health.issue_updated`).
- **`WEB_UI.md`** — banners, inline cards, disabled-action affordances. Pinia store for active issues; SSE subscription wires updates; `useHealth()` composable.
- **`AUTH.md`** — admin-only vs. member-visible issues. v1: members see all banners (transparency), but Tier-2 actions on critical issues require admin elevation (existing 5-minute window).
- **`LOGGING.md`** — issue raises/clears land in `audit_events`, one record per issue (`action: health.issue.raised` / `health.issue.cleared`, `target_kind: health_issue`, `target_id` = the issue ID). Diagnostic-bundle endpoint includes the current issue set + recent transitions.
- **`NOTIFICATIONS.md`** — issue raise/clear is the primary trigger for the dashboard notification center. The issue-raise path additionally emits a notification for allowlisted issue types (storage + system criticals; box-wide criticals also emit a member-facing transparency variant). No change to the issue model.

## Locked decisions

- **The brain comes up if it can.** Degraded mode is the default response to anomalies; halting at the systemd level is reserved for the brain genuinely can't run.
- **Health issues are a typed, bounded set** registered in the brain code. Not config, not free-form.
- **Same dashboard in all states** — banners + inline cards + disabled actions, never a separate degraded UI.
- **Every banner has a primary action.** No actionless status displays.
- **Issues block operations via declared flags** (`blocks_writes`, `blocks_apps`, `blocks_users`). The brain consults these uniformly; no per-call ad-hoc gating.
- **No override path on critical blocks.** Users cannot click through `blocks_writes` or `blocks_apps` for critical issues.
- **No silent auto-recovery.** Cleared issues surface a toast.
- **Remediation tiers (1/2/3) are declared per action**, and Tier 3 is reserved for cases where the brain can't run.
- **Diagnostic-bundle download is always available**, regardless of which issues are active.

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
