# Health detector: brain-db-corrupt (locus C, PRAGMA integrity_check)

- **Status:** done
- **Date:** 2026-06-01
- **Specs touched:** docs/specs/HEALTH.md (locus-C catalog row marked built)
- **Issue:** #36 (closes).

## What was done

Landed `brain-db-corrupt` as a **locus-C** (brain-owned periodic) health detector. The brain runs SQLite's `PRAGMA integrity_check` against its own database and reconciles a `brain-db-corrupt` issue: raise when the result is anything other than `ok`, clear when it's `ok`. Brain-only — no host-agent change, no dependency on the `GET /v1/health/system` transport (#34). The brain owns this state directly (it *is* its database), so the check needs no host round-trip.

Changes:
- **`internal/health/health.go`** — registered the `brain-db-corrupt` definition in `builtinDefinitions()`: category `state`, severity `critical`, Tier 2, blocks writes **and** apps **and** users (HEALTH.md # State: "nearly all ops"). Persisted (not `NoPersist`). The detector reconciles through the existing generic `Manager.Raise`/`Clear`, exactly like the locus-C `store-write-failed` precedent — no new Manager API.
- **`internal/store/health.go`** — added `IntegrityCheck() (string, error)`: runs `PRAGMA integrity_check`, joins the result rows with newlines, returns `"ok"` for a sound database and a (possibly multi-line) corruption report otherwise. The SQLite query lives at the persistence boundary (the store), per the issue and CLAUDE.md's layer rule, not in the health package. Read-only; runs on the brain's single serialized connection.
- **`cmd/brain/main.go`** — added the detector:
  - `const dbIntegrityCheckPeriod = 6 * time.Hour` — the spec cadence as a constant, not an env knob (the value is pinned; nothing to tune per-deployment).
  - `integrityChecker` — a one-method consumer-side interface (`IntegrityCheck()`), satisfied by `*store.Store`, so the check is unit-testable with a fake.
  - `checkBrainDBIntegrity(...)` — runs the check, compares to `"ok"`, raises/clears `brain-db-corrupt` with the integrity report in `details`, and emits the per-issue audit + notification fan-out (mirrors `pullStorageHealth`).
  - `brainDBIntegrityLoop(...)` — runs the **boot check inside the goroutine** (not synchronously before serving), then re-checks every 6h.
  - Wired into `main()`: `go brainDBIntegrityLoop(...)` alongside the storage-health poll, sharing `pollCtx`.

## How it maps to the specs

- HEALTH.md # State (`brain-db-corrupt` row): critical / blocks nearly all ops / Tier 2. ✓
- HEALTH.md # Detector catalog, locus C (`brain-db-corrupt` | `PRAGMA integrity_check` | boot + 6h | result ≠ `ok`). Marked `*(built)*` in the same change. ✓
- HEALTH.md # Stance ("if the brain can possibly run, it runs"): the boot check is best-effort and **non-blocking** — it runs on its own goroutine *after* the brain is already serving, so a corrupt DB raises a banner but never gates startup. The brain-can't-boot path is `bootstrap-state-mismatch` / recovery, a distinct issue. ✓ (Design clarification recorded in the issue, 2026-05-31.)
- HEALTH.md # Lifecycle / LOGGING knock-on: each raise/clear writes one `health.issue.*` audit record (via the shared `emitHealthTransitions`). ✓
- HEALTH.md # Cross-cutting detector policy "last-checked is always fresh": a steady corruption refreshes `last_checked_at` every run without re-raising (existing `raiseLocked` + unconditional upsert; covered by test). ✓

## Conservative interpretations (recorded per the issue — no new spec invented)

- **Result parsing = exact equality against `"ok"`.** `PRAGMA integrity_check` returns one row `ok` when sound, or up to 100 rows of error text when not. `IntegrityCheck` joins the rows; the detector raises when the joined string ≠ `"ok"`. The report becomes the issue's `details` (and thus the diagnostic bundle's), so the technical specifics survive for support.
- **1-shot, no debounce.** Issue #36 mandates this ("A failing check is authoritative — no debounce"): a `PRAGMA integrity_check` verdict is definitive, not a noisy threshold sample, so the detector raises/clears on the first reading and keeps no consecutive-sample state. This is a deliberate **override** of HEALTH.md's cross-cutting locus-C debounce default ("raise on 2 consecutive bad samples") — *not* the locus-A/D authoritative-signal exception, which does not list locus C. The override is recorded as a per-row note in the HEALTH.md locus-C catalog, exercising the policy's own "these defaults apply … unless its row overrides them" clause, so spec and code agree. The `store-write-failed` precedent is the nearest sibling — a 1-shot brain-state check. A query *error* (the check couldn't run at all) is treated as inconclusive: it neither raises nor clears, so a transient I/O blip leaves the issue state intact — and corruption severe enough to break the `PRAGMA` query itself surfaces through the `store-write-failed` fallback (a failed health-row write), not through this detector.
- **Persisted, not `NoPersist`.** Unlike `store-write-failed` (which exists precisely when writes are broken), `integrity_check` commonly flags corruption that still permits a row write (a damaged index page, freelist, etc.), and the banner should survive a restart — so the issue persists like every other. The generic `store-write-failed` fallback inside `Manager.Raise` already covers the case where the corruption *also* breaks the upsert (both issues then surface). On the next boot, the boot check re-reconciles: still-corrupt re-raises (idempotent), repaired/replaced DB clears the restored stale issue.

## Known gaps & deviations

- **Notification allowlist entry deferred (not "undecided").** `brain-db-corrupt` surfaces today as a health **banner** (`GET /api/v1/health`), not a pushed dashboard notification, because it isn't in `internal/notify` `healthRules` yet. The policy is *not* open: `NOTIFICATIONS.md` # The notification list routes system criticals to Admin, and HEALTH.md # Knock-ons lists "storage + system criticals" as the allowlist — `brain-db-corrupt` qualifies. It's left unwired here on purpose, the documented incremental-wiring pattern (`disk-full`, `version-mismatch`, `schema-migration-failed` are likewise on the spec allowlist but absent from `healthRules` until wired). Wiring it is more than a map key: a `healthRule` carries user-facing notification copy + a Tier-2 action *route* (the "restore from backup" flow), which is notification-UX owned by the notification workstream, not this detector PR. `checkBrainDBIntegrity` already calls `emitHealthNotifications` (symmetric with `pullStorageHealth`), so the path is a no-op for `brain-db-corrupt` only until that `healthRules` entry lands — then it's live with no detector change.
- **No `brain-db-corrupt` actions wired.** The `Issue.Actions` list is deferred project-wide (see `internal/health` `Issue` doc comment); the Tier-2 "restore from backup" action lands with the backup surface (`STORAGE.md` backup architecture is itself deferred).
- **The corrupt path is tested with a fake, not a corrupted file.** Deterministically corrupting a live SQLite file to make `integrity_check` fail is flaky; the store-layer test asserts a known-good DB returns `ok`, and the raise/clear/refresh behaviour is driven at the `cmd/brain` layer with a `fakeIntegrityChecker`.

## Tests

- **`internal/store/health_test.go`** — `TestIntegrityCheck_HealthyDBReturnsOk`: a freshly-migrated store passes `integrity_check` and reports `"ok"` (#36 Done-when: store-layer good-DB test).
- **`internal/health/health_test.go`** — `TestList_BrainDBCorruptDefinition` pins the registered metadata (state / critical / Tier 2 / blocks writes+apps+users).
- **`cmd/brain/main_test.go`** — drives `checkBrainDBIntegrity` with a `fakeIntegrityChecker`:
  - `CorruptRaises` — a non-`ok` result raises `brain-db-corrupt`, writes one raised audit record, and carries the integrity output in `details` (#36 Done-when).
  - `OkClears` — an `ok` result clears a prior corruption and writes the clear record (#36 Done-when).
  - `OkNoIssueIsNoop` — the steady-healthy path raises nothing and audits/notifies nothing.
  - `SteadyCorruptRefreshesWithoutReaudit` — a persistent corruption raises once; the next check refreshes `last_checked_at` without re-raising/re-auditing, and leaves `raised_at` untouched.
  - `QueryErrorLeavesStateUnchanged` — a failed `IntegrityCheck` neither clears an active issue nor audits.

## Verification

- `gofmt -l` over the changed Go files: clean.
- `go vet` + `go test` over `internal/health`, `internal/store`, `cmd/brain`: pass.
- The only failure in a broader run is the pre-existing `internal/hostagent/pamverifier` build gap (`security/pam_appl.h` absent — no `libpam0g-dev` on this box), unrelated to this change. `make vet`/`make test-nopam` can't run as-is locally because `cmd/host-agent-real` transitively imports the PAM cgo package; this change touches no host-agent code, so the explicit non-PAM package set is the right gate here.

## What's next (follow-ups, not blockers)

- Wire the `brain-db-corrupt` → Admin notification that the allowlist already covers: add a `notify.healthRules` entry with its notification copy + Tier-2 "restore from backup" action route once the backup surface exists. (Belongs to the notification workstream, not this detector PR.)
- The remaining unblocked detector: `#35` container-restart-loop (locus D). The locus-B downstreams (`#38` ram-pressure, `#39` clock-not-synced, `#40` reboot-required) wait on #34's `GET /v1/health/system` transport merging.
