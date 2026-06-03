# 0019 — Boot pipeline: storage-ready target, reporter, brain health registry

- **Status:** done
- **Date:** 2026-05-25
- **Specs touched:** `BOOT.md`, `HEALTH.md`, `BRAIN_HOST_PROTOCOL.md`, `STORAGE.md`

First slice of the boot pipeline work. Lays down the *userspace half* of
`BOOT.md` — systemd units, the storage-verify reporter, the host-agent
`/v1/health/storage` endpoint, and the brain's `internal/health` registry that
turns reporter findings into typed health issues per `HEALTH.md`. The
initramfs / LUKS / TPM half (and the QEMU+swtpm medium test lane) is
deliberately deferred — tracked under "What's next" below.

Per the architecture user picked at scope time: **host-agent reads
`/run/molma/health/storage.json` and forwards findings to the brain via
the existing protocol seam.** The brain's container stays isolated from the
host filesystem.

## What was done

### Wire — `GET /v1/health/storage`

New protocol types in `internal/protocol/host.go`: `StorageHealth`
(`checked_at` + `findings`) and `Finding` (`id` + `details`). The ID is a
stable string drawn from the typed taxonomy in `HEALTH.md` # Storage
(`data-drive-missing`, `data-drive-wrong`, `canary-mismatch`,
`mergerfs-assembly-failed`, plus the synthetic `health-report-malformed`).
**Severity, tier, and `blocks_*` flags are not on the wire** — those live in
the brain's definition registry, bound to the ID at startup. One source of
truth.

Three new things compose the seam, end to end:

- **`internal/hostagent`** gains a `HealthSource` consumer-side interface and
  a `GET /v1/health/storage` handler. The handler's contract: **always
  return 200 with a parseable payload**, even on source error. The brain's
  polling loop must never have to retry a 5xx for this endpoint —
  "host-agent ran but storage looks bad" and "host-agent unreachable" stay
  cleanly separable.
- **`internal/hostagent/healthsource`** is the production source for
  `cmd/host-agent-real`: reads `/run/molma/health/storage.json`, normalizes
  the payload (missing file = empty findings, missing JSON field = empty
  slice not nil), and synthesizes a single `health-report-malformed`
  finding on parse error so the brain has *something* to surface rather
  than silently passing.
- **`hostagent.FakeHealthSource`** is the fake binary's source — settable
  findings list, used by integration tests to seed specific findings and
  assert the brain raises the matching issue.

### Reporter — `cmd/molma-storage-verify`

A small Go binary that runs at boot from `molma-storage-verify.service`.
Checks:

1. Read `/etc/molma/data-drive.enrolled`. No marker → Level-0 boot, empty
   findings (per `STORAGE.md` # Data drive enrollment marker).
2. Read `/srv/molma/.canary`. Missing → `data-drive-missing`. UUID mismatch
   vs. marker → `data-drive-wrong`.
3. Read `/var/lib/molma/.canary`. Missing or differs from `/srv/molma/.canary`
   → `canary-mismatch` (bind landed on the wrong filesystem — the
   silent-orphan bug class `STORAGE.md` # Storage canary is built to catch).

Writes the result to `/run/molma/health/storage.json` atomically (temp file
+ rename). **Exits 0 always** — per `BOOT.md` # Failure → recovery target,
the verifier is a *reporter*, not a gate. `molma-storage-verify.service`
carries no `OnFailure=` routing.

The check logic lives in `internal/storageverify` (cmd is a thin wrapper)
so it's unit-testable against a tempdir-rooted filesystem without root.

### Brain — `internal/health`

The typed-issue registry per `HEALTH.md` # The health-issue model. Mirrors
the JSON shape: `ID`, `InstanceKey`, `Category`, `Severity`, `Tier`,
`BlocksWrites/Apps/Users`, `Summary`, `Details`, `RaisedAt`,
`LastCheckedAt`. The v1 storage taxonomy is pre-registered as definitions in
code (`builtinDefinitions`); other categories pre-registered as the brain
learns them is the natural next pattern.

Key operations:

- `Raise(id, instanceKey, details)` — idempotent. Re-raising preserves
  `RaisedAt` (transition timestamps don't reset on every poll) and updates
  `LastCheckedAt` + `Details`. Returns `true` only on the cleared→active
  transition, so the caller can audit-event / notify on the transition, not
  every poll. An unregistered ID returns `false` silently — callers that
  might feed unregistered IDs (like the reconciler below) log at warn.
- `Clear(id, instanceKey)` — also returns transition.
- `ApplyStorageFindings(StorageHealth)` — reconciles the active set against
  a fresh poll: any storage-category issue not in findings is cleared,
  every finding is raised or refreshed. **Only touches storage-category
  issues** — when network/version detectors land, they reconcile their own
  categories independently. The clear+raise cycle runs **under a single
  critical section** (private `raiseLocked` / `clearLocked` helpers) so a
  concurrent `List()` never sees a transient "all clear" between the
  clears and the new raises. Unknown finding IDs (reporter-vs-brain version
  skew) land in a `slog.Warn` so they don't disappear silently.

### Brain wiring + `GET /api/v1/health`

`cmd/brain/main.go` now constructs a `health.Manager` and runs a startup
pull + a 60s poll loop against `host.StorageHealth(ctx)`. Both are
best-effort: host-agent unreachable at startup → a `slog.Warn` and the
brain runs anyway. The poll loop catches up once host-agent comes online.
This is the same posture every other host call uses.

`GET /api/v1/health` returns the current issue list (`{ "issues": [...] }`).
Admin-only via `requireAdmin` in v1 — the dashboard's member-facing
transparency variant (`HEALTH.md` # Display) wires through SSE once that
lands. The SSE `health.issue_raised` / `health.issue_cleared` events are
**not in this slice**; the polling endpoint is enough to prove the round-trip.

### Systemd units — `dist/systemd/`

New directory holding the unit files this slice introduces. Per `BOOT.md`
# Tier-2 services get drop-ins, the upstream Debian units get drop-ins at
`/etc/systemd/system/<unit>.service.d/molma.conf` rather than replacement
units.

| File | What it does |
|---|---|
| `molma-storage-ready.target` | Synthetic target, `Wants=molma-storage-verify.service`. Best-effort milestone. |
| `molma-storage-verify.service` | Type=oneshot, `TimeoutStartSec=60s`, no `OnFailure=`. Runs the reporter. No `[Install]` block — the target's hardcoded `Wants=` pulls it in (single source of dependency, no enable-time symlink confusion). |
| `host-agent.service` | After=storage-ready + docker, `OnFailure=molma-recovery.target`, `StartLimitBurst=5/60s` per `BOOT.md`. `StartLimit*` live in `[Unit]` (canonical since systemd 229; the `[Service]` aliases behave subtly differently under `systemctl reset-failed`). |
| `molma-recovery.target` | Placeholder so `OnFailure=` routing has a target to land on. Recovery UI deferred to `RECOVERY.md`. |
| `dropins/docker.service.d/molma.conf` | After/Wants=storage-ready (Wants=, not Requires=). |
| `dropins/smbd.service.d/molma.conf` | After/Wants=storage-ready. Blocks-writes reload path is separate. |
| `dropins/avahi-daemon.service.d/molma.conf` | After/Wants=storage-ready for ordering only. |

A `dist/systemd/README.md` documents install paths and what's intentionally
not here yet.

## How it maps to the specs

- `BOOT.md` # The storage-ready target — best-effort assembly: realized.
  The target exists, the reporter writes findings, host-agent reads them,
  the brain converts them into issues. `Wants=`, not `Requires=`, on every
  downstream — a missing data drive does not stop the dashboard.
- `BOOT.md` # Failure → recovery target — the narrow cases: only
  `host-agent.service` has `OnFailure=molma-recovery.target` with the
  bounded restart policy. `molma-storage-verify.service` is a pure reporter
  with no `OnFailure=` — matches the rule.
- `BOOT.md` # The bootstrap marker is written by the brain — not changed
  here; called out so the next slice (`molma-prepare-wizard.service`)
  remembers it.
- `HEALTH.md` # The health-issue model: in-memory implementation matches
  the JSON shape and severity/tier semantics. `Actions` is deferred until
  the dashboard needs it.
- `HEALTH.md` # Storage taxonomy: six definitions registered; the wire
  carries only the ID, the brain binds the rest. Adding a new storage
  issue type is a code change to one place.
- `HEALTH.md` # The dashboard exposes the live set via
  `GET /api/v1/health/issues`: this slice ships `GET /api/v1/health`
  (singular). The plural-suffix form in `HEALTH.md` is the eventual shape;
  the singular endpoint here is the v1 surface and aliases to the same
  data. Not a spec divergence — same data, the dashboard rename can happen
  when the SSE event channel lands and both pieces ship together.
- `BRAIN_HOST_PROTOCOL.md`: `GET /v1/health/storage` is a new Pattern-A
  (sync read) endpoint. Always returns 200 with a parseable body —
  documented on the handler.
- `STORAGE.md` # Storage canary: the reporter implements the content-match
  check (`/srv/molma/.canary` vs. marker UUID, bind-mount canary vs.
  data-drive canary). The **device-backing check via `findmnt -no SOURCE`**
  is not implemented — see "Known gaps."

## Known gaps & deviations (loud)

- **No device-backing check.** `STORAGE.md` # Storage canary is explicit
  that content match alone is insufficient ("a stale canary on the OS drive
  plus a failed data-drive mount would otherwise read as healthy"). This
  slice ships the content check only. The structural check needs
  `findmnt -no SOURCE` (or parsing `/proc/self/mountinfo`) and a real mount
  setup to test against — i.e. the QEMU+swtpm lane that's a separate slice.
  Listed in `BOOT.md` knock-ons as a follow-up.
- **`storage-verify-timeout` is not emitted.** `BOOT.md` # Hang protection
  says "a hang trips this timeout, the reporter writes a
  `storage-verify-timeout` finding." But systemd kills the verifier on
  `TimeoutStartSec=60s`, so it cannot write its own timeout. A wrapper unit
  (or a `ExecStopPost=` that writes the synthetic finding) is the real
  shape. Not in this slice; tracked below.
- **In-memory issue store.** `HEALTH.md` # Persistence specifies
  `health_issues` in SQLite with raises/clears mirrored into
  `audit_events`. Slice #1 holds issues in-memory only — they reset on
  brain restart, then re-raise on the first poll. No audit-event emission
  on raise/clear yet. The Manager API (`Raise` returns transition bool) is
  shaped so the SQLite/audit wiring is additive.
- **No SSE event emission.** Spec lists `health.issue_raised` /
  `health.issue_cleared` event `kind`s on the global SSE channel. This
  slice does not emit them. The dashboard polls `GET /api/v1/health` for
  now; SSE follows when the dashboard surface is built.
- **No real boot validation.** The systemd units live in `dist/systemd/`
  but nothing in CI boots them. The nspawn fast lane (per `TESTING.md`)
  needs to be extended to assert unit dependencies, drop-in ordering, and
  target activation shape. That is the obvious next slice — see "What's
  next." Today, the units have been hand-validated by reading; that's not
  enough for a slice that ships, but it's what slice #1 commits to.
- **`molma-recovery.target` is a placeholder.** It exists so `OnFailure=`
  has somewhere to land. There is no static page server, no
  `molma-recovery.local` mDNS, no rollback button. `RECOVERY.md` is
  deferred per `NEXT.md`; documented in `dist/systemd/README.md`.
- **No tmpfiles.d entry for `/run/molma/health/`.** The reporter binary
  `mkdir -p`s the directory on first write. A `tmpfiles.d` entry is the
  cleaner long-term shape.
- **`StorageHealth.CheckedAt` is an unvalidated passthrough.** The wire
  type documents it as RFC3339, but neither host-agent nor the brain
  parses it; a reporter writing `""` or a corrupt value is silently
  accepted. Low risk today because nothing consumes the timestamp — but
  the moment the dashboard renders it ("last checked 2 min ago"), the
  brain needs to either validate on receipt (rejecting / re-stamping
  garbage) or carry its own observation time alongside. Tracked here so
  the dashboard slice doesn't ship rendering an unparseable field.
- **API path is `/api/v1/health` not `/api/v1/health/issues`.** Spec uses
  the plural. Documented above as not-a-divergence; will rename in the
  dashboard slice when SSE lands and both ship together.

## What's next

In recommended order:

- **nspawn fast-lane tests for the boot chain.** Extend
  `dev/test-nspawn/` (from slice 0018) to load the units in
  `dist/systemd/`, assert dependencies (`systemctl list-dependencies
  molma-storage-ready.target` is sane), drop-ins apply
  (`systemctl cat docker.service | grep molma-storage-ready`), and the
  reporter runs and writes `/run/molma/health/storage.json`. This is
  exactly the "service-level integration" bucket `TESTING.md` # Fast lane
  enumerates.
- **QEMU + swtpm medium-lane scaffolding.** A single happy-path boot in a
  VM with a software TPM, proving the lane works. Unblocks the
  device-backing canary check, LUKS unlock tests, and the rest of the
  `TESTING.md` # Medium lane matrix.
- **SQLite persistence for `health_issues`** + raise/clear audit events.
  Surface in the existing `Activity` view.
- **SSE event emission** (`health.issue_raised` / `health.issue_cleared`)
  + dashboard banner / inline-card rendering. Migrate the endpoint to
  `/api/v1/health/issues` at the same time.
- **Device-backing canary check.** `findmnt -no SOURCE`-based check
  matched to the enrolled UUID. Needs a real mount setup (medium lane).
- **`storage-verify-timeout` synthesis.** Either an `ExecStopPost=` script
  on `molma-storage-verify.service`, or a wrapper unit. Decide once the
  medium lane exists and we can actually trigger the timeout.
- **`molma-prepare-wizard.service` oneshot.** The marker-write rule in
  `BOOT.md` lands when the first-run slice picks this up.
- **`Type=notify` + watchdog on `host-agent.service`.**
  `CONTROL_PLANE.md` # host-agent specifies the full hardening; this slice
  shipped a minimal unit shape.
