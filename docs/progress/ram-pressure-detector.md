# ram-pressure health detector (locus B, PSI /proc/pressure/memory)

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** docs/specs/HEALTH.md (locus-B "Built" note)

Closes #38. Adds the `ram-pressure` locus-B detector on the seam #34 built (`health-system-report.md`): host-agent samples `/proc/pressure/memory` (PSI `some avg60`), the brain reconciles it under the report's `resources` category. The **first Capacity-category** issue in the registry, and a sibling of the `clock-not-synced` detector (#39) — both are pure follow-ups on #34's `GET /v1/health/system` + `ApplyFindings(category, …)` seam.

(#38 was labelled `blocked` on #34, which merged 2026-06-01 — the label was stale per the issue's own "drop the `blocked` label when it lands" instruction; dropped on pickup.)

**Built on `main`, independent of #39.** #38 and #39 (clock-not-synced) are siblings off the same #34 seam, not a stack — the `HealthCategory` enum reserved both slots up front (`resources` for #38, `time` for #39; `internal/protocol/host.go`, "#34 clarification"). #39 is still open (PR #81) when this landed, so #38 is built off `main` rather than stacked on it. The two touch the same five seam files (the `RAMReporter`/`ClockReporter` interface block, the `Resources`/`Time` Agent field, the `systemHealth` handler tail, the registry tail, the HEALTH.md "Built" note); whichever merges second takes a trivial additive rebase — each adds its own clause/block, neither rewrites the other's.

## What was done

- **Reporter** (`internal/hostagent/rampressure/`): reads `/proc/pressure/memory`, parses the `some` line's `avg60` (share of the last 60s during which at least one task stalled on memory), and emits one box-wide `ram-pressure` finding when it exceeds the threshold. A pure `parseSomeAvg60` (reads the `some` line, never `full`) keeps the parse unit-testable. Unlike the slower-cadence clock reporter, this one is **stateless / cacheless**: the ram-pressure cadence is 60s (HEALTH.md), which *is* the brain's `/v1/health/system` poll interval, so every `Read` re-reads `/proc`. `read` is injectable for tests.
- **Threshold = PSI `some avg60` > 20%**, a single named constant `pressureThreshold`. HEALTH.md leaves this as "sustained > threshold (tune at first soak)", and #38 explicitly delegates the choice ("Pick a conservative default… Do not invent a precise spec number"). Rationale: `some avg60` sits near zero on a box with adequate RAM even under heavy CPU/IO — it climbs only when tasks actually stall on reclaim/swap-in/refault. 20% sustained over a full minute, *and* across the brain's two-bad-sample debounce (~2 min of real contention), is the "swap thrashing" the summary describes without being trigger-happy on a brief reclaim spike. One constant → first-soak tuning is a one-line change.
- **Wiring** (`internal/hostagent/agent.go`): a new consumer-side `RAMReporter` interface (same `Read() []protocol.Finding` shape as `ServiceReporter`/`ClockReporter`) + a `Resources` field on `Agent`; the `systemHealth` handler emits the `resources` category only when a reporter is wired (so a nil reporter reads as "not measured", never "pressure healthy"). `FakeRAMReporter` added to `fake.go` for tests.
- **Brain registry** (`internal/health/health.go`): one `ram-pressure` `Definition` — display Category `capacity`, severity `warning`, tier 1, no block flags, `ReportCategory: resources`, `Debounce: true`. No reconcile-loop change: `pullSystemHealth` already iterates every reported category and `ApplyFindings` clears-absent by `ReportCategory`, so the registration is sufficient. The summary string is **authored** (plain-English, house style) — HEALTH.md's catalog text ("swap thrashing") is dev-facing and no spec carries a user-facing string, so it was reworded jargon-free and pointed at the per-container monitor, matching how `clock-not-synced` reworded TIME.md.
- **Real binary** (`cmd/host-agent-real/main.go`): `a.Resources = rampressure.New()`.
- **Tests:** `rampressure` parser tests against **verbatim** `/proc/pressure/memory` output (idle + thrashing — asserting it reads `some`, not `full`); threshold/boundary/fail-open (read error and parse error) `Read` tests; a host-agent handler test (`resources` category present when wired, absent when not); a brain reconcile test (`TestApplyFindings_RAMPressureDebounces`) exercising both states with the 2-bad/1-good debounce.

## How it maps to the specs

- **HEALTH.md # Detector catalog** (`ram-pressure` row, locus B): `/proc/pressure/memory` (PSI `some avg60`), 60s, raise on sustained > threshold, clear below, debounce. Realized; the locus-B "Built" note now lists it.
- **HEALTH.md # Capacity & informational**: display category `capacity`, warning, tier-1, all block flags false. Realized.
- **HEALTH.md # Cross-cutting detector policy**: the 2-bad/1-good debounce and `last_checked_at`-every-poll apply via `Debounce: true` + the existing `ApplyFindings` reconcile. Realized.
- **BUILD.md # Kernel cmdline**: `psi=1` is the runtime prerequisite (Debian ships `CONFIG_PSI=y` + `CONFIG_PSI_DEFAULT_DISABLED=y`); already landed there (#38 design clarification). The reporter fails open when PSI is absent — see below.

## Known gaps & deviations

- **Threshold is a conservative first-soak default, not a measured value.** 20% on `some avg60` is documented and trivially tunable (one constant). HEALTH.md explicitly defers the real number to first soak; this is that placeholder, not a spec figure.
- **Fail-open when PSI is unavailable.** If `psi=1` is missing from the kernel cmdline, `/proc/pressure/memory` is absent or reads zeros; the reporter returns nil (never fires) rather than raising on a tooling gap — a missing PSI is a build-config issue, not a memory-pressure finding. BUILD.md already documents `psi=1`; the medium lane should confirm PSI is live (`cat /proc/pressure/memory` shows changing `avg` under load) before trusting the detector.
- **Real-system verification is the maintainer's lane.** `/proc/pressure/memory` isn't meaningfully populated on the dev box, so the parser is tested against captured **real-format** output and a synthetic load is not driven here. Per #38's testing note, exercising the live PSI read under memory load on a real host / the QEMU medium lane is the confirming step. `cmd/host-agent-real` also only compiles with `libpam0g-dev` present (CI / medium lane), not on a bare dev box — the change there is an import + one assignment that satisfies `RAMReporter` by construction.
- **No notify-center entry.** `ram-pressure` surfaces via the health banner (`GET /api/v1/health`) only — intentionally not in notify's `healthRules` allowlist, matching the reference locus-B detectors (`service-down`, `clock-not-synced`). Out of scope for #38 (detector-only).
- **No per-container attribution.** The summary points the user at the per-container monitor ("which app is using the most"); the detector itself reports only the box-wide PSI value. Attribution lives in the live system-resources view (`LOCAL_ANALYTICS.md`), not in this finding.

## What's next

- The remaining now-unblocked sibling locus-B detector is **#40 reboot-required** (file presence) — but it needs a **new `HealthCategory` protocol constant** (none of `storage|drives|services|resources|time` fits a reboot flag), which is a locked-protocol decision to surface before building, not a pure follow-up like this one.
- **First-soak threshold tuning** for `pressureThreshold` once there's real soak data (HEALTH.md # Detector catalog).
- **Settings → System** capacity surface that consumes this issue alongside the live memory view — frontend, not yet built.
