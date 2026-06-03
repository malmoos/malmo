# Health detector: version-mismatch (locus C, brain↔host-agent lockstep)

- **Status:** done
- **Date:** 2026-06-01
- **Specs touched:** docs/specs/HEALTH.md (locus-C catalog row marked built)
- **Issue:** #37 (closes).

## What was done

Landed `version-mismatch` as a **locus-C** (brain-owned periodic) health detector. The brain reads host-agent's reported `agent_version` and reconciles a `version-mismatch` issue: raise when it differs from the version the brain expects, clear when they match. Brain-only — no host-agent change, no dependency on the `GET /v1/health/system` transport (#34).

Changes:
- **`internal/health/health.go`** — registered the `version-mismatch` definition in `builtinDefinitions()`: category `version`, severity `error`, Tier 2, `BlocksApps` only (not writes/users), per HEALTH.md # Version. No new Manager API — the detector reconciles through the existing generic `Manager.Raise`/`Clear`, exactly like the locus-C `store-write-failed` precedent.
- **`cmd/brain/main.go`** — added the detector:
  - `const expectedAgentVersion = "0.0.1-fake"` — the brain-side constant the reported agent version is compared against (mirrors `internal/hostagent.AgentVersion`).
  - `agentStatusReader` — a one-method consumer-side interface (`SystemStatus(ctx)`), satisfied by `*hostclient.Client`, so the check is unit-testable with a fake host-agent.
  - `checkAgentVersion(...)` — reads `SystemStatus`, compares `AgentVersion` to `expectedAgentVersion`, raises/clears `version-mismatch`, and emits the per-issue audit + notification fan-out (mirrors `pullStorageHealth`).
  - `versionCheckPollLoop(...)` — re-runs the check on the existing `MOLMA_HEALTH_POLL` cadence (60s default).
  - Wired into `main()`: one check at startup (the first handshake) + the poll loop, alongside the existing storage-health poll. Reuses `pollCtx`/`pollCancel`.

## How it maps to the specs

- HEALTH.md # Version (`version-mismatch` row): error / blocks apps / Tier 2. ✓
- HEALTH.md # Detector catalog, locus C ("brain goroutine timers over brain-owned state … the version it negotiated") and the locus-C table (`version-mismatch` | host-agent vs brain version on handshake | each handshake | not the lockstep pair). Marked `*(built)*` in the same change. ✓
- HEALTH.md # Lifecycle / LOGGING knock-on: each raise/clear writes one `health.issue.*` audit record (via the shared `emitHealthTransitions`). ✓
- HEALTH.md # Cross-cutting detector policy "last-checked is always fresh": a steady mismatch refreshes `last_checked_at` every poll without re-raising (existing `raiseLocked` behavior; covered by test). ✓

## Conservative interpretations (recorded per the issue — no new spec invented)

- **"Lockstep pair" = exact string equality against a brain-side expected version.** The brain holds `expectedAgentVersion` (the agent version it was built/released alongside) and raises when the reported `agent_version` is not equal to it. The issue's richer release-manifest lockstep model (`RELEASE_MANIFEST.md` / `UPDATES.md` brain+UI stream) is out of scope; the pair definition tightens when the release manifest lands. The constant mirrors `hostagent.AgentVersion` rather than importing it, so the brain stays decoupled from the agent's package and the future divergence (independently versioned binaries) is explicit — keep the two in lockstep until the release manifest replaces the constant.
- **"Each handshake" = each successful periodic `GET /v1/system/status` read.** There is no dedicated handshake RPC; the brain already reads `SystemStatus` once at startup (the first handshake) and the poll loop continues it on the 60s health cadence.
- **1-shot, no debounce.** A version string is a deterministic, authoritative value — it cannot flap like a threshold sample — so the detector raises/clears on the first definitive reading. (HEALTH.md's debounce default targets noisy samples; there is no noise in an equality check.) A transient unreachable host-agent neither raises nor clears, so the issue state survives a blip. (#34's debounce machinery, when it merges, need not apply here.)

## Known gaps & deviations

- **Notification allowlist entry deferred (not "undecided").** `version-mismatch` surfaces today as a health **banner** (`GET /api/v1/health`), not a pushed dashboard notification, because it isn't in `internal/notify` `healthRules` yet. The policy is *not* open: `NOTIFICATIONS.md` # The notification list (v1 source allowlist) explicitly routes `version-mismatch` (error) to **Admin**, so per spec it *should* push an admin notification. It is left unwired here on purpose — exactly the documented incremental-wiring pattern in `notify.go` (`disk-full`, `brain-db-corrupt`, `schema-migration-failed` are likewise on the spec allowlist but absent from `healthRules` until wired). Wiring it is more than a map key: a `healthRule` carries user-facing notification copy + a Tier-2 action *route* (an Updates/System page), which is notification-UX that belongs to the notification workstream, not this detector PR. `checkAgentVersion` already calls `emitHealthNotifications` (symmetric with `pullStorageHealth`), so the path is a no-op for `version-mismatch` only until that `healthRules` entry lands — then it is live with no detector change. **Spec tension to flag for the maintainer:** `HEALTH.md` # Locked decisions summarizes the notification allowlist as "storage + system criticals," but `NOTIFICATIONS.md:120` lists error-severity System/state issues (`schema-migration-failed`, `version-mismatch`) too — NOTIFICATIONS.md owns the taxonomy, so it's authoritative, but the HEALTH.md summary reads as if it excludes them.
- **No `version-mismatch` actions wired.** The `Issue.Actions` list is deferred project-wide (see `internal/health` `Issue` doc comment); the Tier-2 "update the lagging component" action lands with the Updates surface.

## Tests

- **`internal/health/health_test.go`** — `TestList_VersionMismatchDefinition` pins the registered metadata (version / error / Tier 2 / blocks apps only).
- **`cmd/brain/main_test.go`** — drives `checkAgentVersion` with a `fakeStatusReader` (a fake host-agent reporting a chosen `agent_version`):
  - `MismatchRaises` — a differing version raises `version-mismatch` and writes one raised audit record (#37 Done-when).
  - `MatchClears` — a matching version clears a prior mismatch and writes the clear record.
  - `MatchNoIssueIsNoop` — the steady happy path raises nothing and audits nothing.
  - `SteadyMismatchRefreshesWithoutReaudit` — a persistent mismatch raises once; the second poll refreshes `last_checked_at` without re-raising or re-auditing, and leaves `raised_at` untouched.
  - `UnreachableLeavesStateUnchanged` — an unreachable host-agent neither clears an active mismatch nor audits.

## Verification

- `gofmt -l` over the changed Go files: clean.
- `go vet` + `go test` over `internal/health` and `cmd/brain`: pass.
- Broader non-PAM run (`protocol`, `health`, `store`, `hostclient`, `hostagent`, `api`, `notify`, `audit`, `lifecycle`, `cmd/brain`, `cmd/host-agent`): pass. The only failure is the pre-existing `internal/hostagent/pamverifier` build gap (`security/pam_appl.h` absent — no `libpam0g-dev` on this box), unrelated to this change. `make vet`/`make test-nopam` can't run as-is locally because `cmd/host-agent-real` transitively imports the PAM cgo package; this change touches no host-agent code, so the explicit non-PAM set is the right gate here.

## What's next (follow-ups, not blockers)

- Replace `expectedAgentVersion` with the release-manifest lockstep definition once `RELEASE_MANIFEST.md` / `UPDATES.md` land (the issue flagged this as the place the pair definition tightens).
- Wire the `version-mismatch` → Admin notification that `NOTIFICATIONS.md:120` already allowlists: add a `notify.healthRules` entry with its notification copy + Tier-2 action route once the Updates/System surface exists. (Belongs to the notification workstream, not this detector PR.)
- The remaining unblocked locus-C/D detectors: `#36` brain-db-corrupt (locus C), `#35` container-restart-loop (locus D). The locus-B downstreams (`#38` ram-pressure, `#39` clock-not-synced, `#40` reboot-required) wait on #34's `GET /v1/health/system` transport merging.
