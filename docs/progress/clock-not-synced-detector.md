# clock-not-synced health detector (locus B, chronyc tracking)

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** docs/specs/HEALTH.md (locus-B "Built" note)

Closes #39. Adds the `clock-not-synced` locus-B detector on the seam #34 built (`health-system-report.md`): host-agent samples `chronyc tracking`, the brain reconciles it under the report's `time` category. The third locus-B detector after storage (`boot-pipeline-units.md`) and `service-down` (`health-system-report.md`), and the **first Network-category** issue in the registry.

(#39 was labelled `blocked` on #34, which merged 2026-06-01 — the label was stale per the issue's own "drop the `blocked` label when it lands" instruction; dropped on pickup.)

## What was done

- **Reporter** (`internal/hostagent/clockhealth/`): runs `chronyc tracking`, parses **Ref time** (last source update) and **Last offset**, and emits one box-wide `clock-not-synced` finding when the last sync is older than **6h** OR the offset exceeds **10s** (thresholds fixed by spec — TIME.md). A pure `parseTracking` (first-colon split — chrony keys never contain a colon, so the time's own colons fall in the value) keeps the parse unit-testable; `Reporter.Read` wraps it with a **lazy 5-minute cache** so chrony is re-queried at most every 5 min even though the brain polls every 60s. `now`/`runTracking` are injectable. Mirrors `servicehealth`.
- **Wiring** (`internal/hostagent/agent.go`): a new consumer-side `ClockReporter` interface (same `Read() []protocol.Finding` shape as `ServiceReporter`, named per the established per-category convention `HealthSource`/`ServiceReporter`) + a `Time` field on `Agent`; the `systemHealth` handler emits the `time` category only when a reporter is wired (so a nil reporter reads as "not measured", never "healthy"). `FakeClockReporter` added to `fake.go` for tests.
- **Brain registry** (`internal/health/health.go`): one `clock-not-synced` `Definition` — display Category `network`, severity `warning`, tier 2, no block flags, `ReportCategory: time`, `Debounce: true`. Summary verbatim from TIME.md. No reconcile-loop change: `pullSystemHealth` already iterates every reported category, and `ApplyFindings` clears-absent by `ReportCategory`, so the registration is sufficient.
- **Real binary** (`cmd/host-agent-real/main.go`): `a.Time = clockhealth.New()`.
- **Tests:** `clockhealth` parser tests against **verbatim** `chronyc tracking` output (synced + never-synced/epoch), threshold/boundary/cache/resample/fail-open `Read` tests; a host-agent handler test (`time` category present when wired, absent when not); a brain reconcile test (`TestApplyFindings_ClockNotSyncedDebounces`) exercising both states with the 2-bad/1-good debounce.

## How it maps to the specs

- **HEALTH.md # Detector catalog** (`clock-not-synced` row, locus B): `chronyc tracking`, raise on >6h-since-sync OR offset >10s, clear when synced and offset <10s, debounce. Realized; the locus-B "Built" note now lists it.
- **HEALTH.md # Network** + **TIME.md # Drift monitoring**: display category `network`, warning, no block flags, tier-2 (admin-driven), exact summary string. Realized.
- **TIME.md**: host-agent (not the containerized brain) owns every `chronyc` interaction; 5-min sampling cadence. Realized via the lazy cache.

## Known gaps & deviations

- **No notify-center entry.** `clock-not-synced` surfaces via the health banner (`GET /api/v1/health`) only — it is intentionally not added to notify's `healthRules` allowlist, matching the reference locus-B detector `service-down`, which also has no rule. Out of scope for #39 (which is detector-only).
- **5-min cadence via a lazy cache, not a background sampler.** In the pull architecture the brain polls every 60s; the reporter re-runs `chronyc` at most every 5 min and returns the cache between samples. Trade-off: clear latency after the clock resyncs can be up to ~5 min + one poll — acceptable for a warning.
- **Real-system verification is the maintainer's lane.** `chronyc` isn't installed on the dev box, so the parser is tested against captured **real-format** output. Per #39's testing note, parsing live `chronyc tracking` on a real host / the QEMU medium lane is the confirming step. `cmd/host-agent-real` also only compiles with `libpam0g-dev` present (CI / medium lane), not on a bare dev box — the change there is an import + one assignment that satisfies `ClockReporter` by construction.
- **"Force sync now" (`chronyc -a makestep`) is a sibling, not here.** TIME.md's tier-2 action needs its own host-agent action endpoint — explicitly carved out of #39.

## What's next

- The remaining now-unblocked sibling locus-B detectors follow this exact pattern: **#40 reboot-required** (file presence; needs a display-category decision — not in the reserved enum) and **#38 ram-pressure** (PSI; threshold is "tune at first soak", an open design value to settle before building).
- **"Force sync now"** action endpoint (TIME.md # What stays available) — separate issue.
- **Settings → System → Time** surface (TIME.md) — consumes this issue + the offset/last-sync display; frontend, not yet built.
