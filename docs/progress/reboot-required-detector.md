# reboot-required health detector (locus B, /var/run/reboot-required)

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** docs/specs/HEALTH.md (locus-B "Built" note), docs/specs/BRAIN_HOST_PROTOCOL.md (`GET /v1/health/system` domain list)

Closes #40. Adds the `reboot-required` locus-B detector on the seam #34 built (`health-system-report.md`): host-agent stats Debian's `/var/run/reboot-required` flag (and `/var/run/reboot-required.pkgs` for the package list), the brain reconciles it under a **new `system` report category**. The **first `info`-severity** issue in the registry, and the last of the unblocked locus-B siblings off #34's `GET /v1/health/system` + `ApplyFindings(category, …)` seam (after `clock-not-synced` #39 and `ram-pressure` #38).

(#40 was labelled `blocked` on #34, which merged 2026-06-01 — the label was stale per the issue's own "drop the `blocked` label when it lands" instruction; dropped on pickup.)

**Built on `main`.** Unlike #38/#39, #40 was *not* a pure follow-up: the `ram-pressure` progress entry (`ram-pressure-detector.md` # What's next) flagged that reboot-required "needs a new `HealthCategory` protocol constant (none of `storage|drives|services|resources|time` fits a reboot flag), which is a locked-protocol decision to surface before building." That decision is taken here — see "Decisions surfaced" below. The change still lands additively: the brain's category-generic poller (`pullSystemHealth`) and `ApplyFindings(category, …)` reconcile need **no** change for a new category.

## What was done

- **Reporter** (`internal/hostagent/rebootrequired/`): stats `/var/run/reboot-required`; absent → nil (no reboot pending), present → one box-wide `reboot-required` finding. When present it also reads `/var/run/reboot-required.pkgs`, trims/dedupes/joins the package names into the finding's `Details` (the flag alone still raises — an unreadable/missing `.pkgs` yields empty `Details`, not a dropped finding). **Stateless / cacheless** like the ram-pressure reporter: a `stat` is cheap and the 60s `/v1/health/system` poll is well inside the spec's relaxed 1h cadence, so every `Read` re-stats. `parsePackages` (split → trim → order-preserving dedupe) is a pure, unit-tested helper; paths are injectable for tests. Fails open — an unexpected `stat` error (not `IsNotExist`) logs and returns nil rather than raising on a tooling gap.
- **Wiring** (`internal/hostagent/agent.go`): a new consumer-side `RebootReporter` interface (same `Read() []protocol.Finding` shape as `RAMReporter`/`ClockReporter`) + a `Reboot` field on `Agent`; the `systemHealth` handler emits the `system` category only when a reporter is wired (so a nil reporter reads as "not measured", never "no reboot pending"). `FakeRebootReporter` added to `fake.go` for tests.
- **Protocol** (`internal/protocol/host.go`): new `HealthCategorySystem = "system"` constant on the `HealthCategory` enum, pinned in `host_test.go`'s wire-value guard. The enum doc note records that `system` was added post-#34 for this detector's locus-B reclassification (`DECISIONS.md` 2026-05-31).
- **Brain registry** (`internal/health/health.go`): a new `SeverityInfo = "info"` severity (the first `info` issue; ranks below `warning` in `severityRank`) + one `reboot-required` `Definition` — display Category `capacity`, severity `info`, tier 2, no block flags, `ReportCategory: system`, `Debounce: false`. No reconcile-loop change. The summary string is **authored** plain-English ("A system update needs a reboot to finish.") in house style, matching how the sibling detectors reworded their dev-facing catalog text.
- **Real binary** (`cmd/host-agent-real/main.go`): `a.Reboot = rebootrequired.New()`.
- **Tests:** `rebootrequired` `Read` tests against a real flag file written under `t.TempDir` (absent → healthy; present-with-`.pkgs` → raises with the joined package list; present-without-`.pkgs` → still raises, empty `Details`) + a `parsePackages` trim/dedupe/order test; a host-agent handler test (`system` category present + package list when wired, absent when not — plus the no-sources test now asserts `system` absent); a brain reconcile test (`TestApplyFindings_RebootRequiredOneShot`) proving 1-shot raise (no debounce gate), capacity/info surfacing, `Details` carry-through, and clear-on-absent; a definition-metadata test (`TestList_RebootRequiredDefinition`).

## How it maps to the specs

- **HEALTH.md # Detector catalog** (`reboot-required` row, locus B): presence of `/var/run/reboot-required` (+ `.pkgs` for the message), 1h cadence, raise on file present, clear on file absent (self-clears on reboot — `/run` is tmpfs). Realized; the locus-B "Built" note now lists it. (The 60s poll is a tighter floor than the 1h cadence; the spec cadence is the *minimum* freshness, not a throttle.)
- **HEALTH.md # Capacity & informational**: display category `capacity`, `info` severity, tier-2, all block flags false ("a quiet card"). Realized — first `info` issue.
- **HEALTH.md # Cross-cutting detector policy**: deterministic, non-noisy values are 1-shot (the policy's `version-mismatch` carve-out). File presence is deterministic — apt creates the flag, a reboot clears it via tmpfs `/run` — so it can't flap; `Debounce: false`. Realized.
- **BRAIN_HOST_PROTOCOL.md # Health findings report**: the `GET /v1/health/system` domain list now includes `system` (and `time`, which had been omitted), and the pending-reboot flag is named in the physical-detection examples.

## Decisions surfaced (for review)

These three are the design calls this issue forced — none were already pinned in a spec, so they're called out rather than buried:

1. **New `system` report category.** The `HealthCategory` axis is strictly one-reporter-per-category for independent reconciliation; an OS-state flag fits none of `storage|drives|services|resources|time`. `system` is the minimal addition and the brain poller is category-generic, so it costs one constant + one pin. This is the "locked-protocol decision" the ram-pressure entry flagged before building.
2. **`SeverityInfo` is new.** reboot-required is HEALTH.md's first implemented `info`-severity issue; `health.Severity` previously had only warning/error/critical. The addition is spec-grounded (HEALTH.md uses `info` for `update-available`/`reboot-required`) and mirrors the `notify` package's existing `info` severity.
3. **`Debounce: false` (1-shot).** Unlike the three noisy locus-B siblings (service-down/clock/ram all debounce), a file-presence flag is deterministic and can't flap — so it raises on the first poll and surfaces the info banner one cycle sooner.

## Known gaps & deviations

- **Real-system verification is the maintainer's lane.** The dev box has no `/var/run/reboot-required`, so `Read` is tested against a real flag file written at a temp path. Exercising the live flag (e.g. after `apt` installs a kernel/libc update, or `touch /var/run/reboot-required`) on a real host / the QEMU medium lane is the confirming step. `cmd/host-agent-real` only compiles with `libpam0g-dev` present (CI / medium lane), not on a bare dev box — the change there is an import + one assignment that satisfies `RebootReporter` by construction.
- **No notify-center entry.** `reboot-required` surfaces via the health banner (`GET /api/v1/health`) only — intentionally not in notify's `healthRules` allowlist, matching the reference locus-B detectors. Out of scope for #40 (detector-only). An `info`-severity reboot notification could be a sensible follow-up once a "reboot now / schedule" action surface exists.
- **No remediation action.** HEALTH.md's Tier-2 action is "reboot now / schedule"; this issue only raises the finding. The reboot/schedule control is a Settings → System surface, not yet built — the issue is the prerequisite, not the action.

## What's next

- **Settings → System** surface that consumes this issue and offers the "reboot now / schedule" action (HEALTH.md # Capacity & informational, # Detector catalog) — frontend + a host-agent reboot op, not yet built.
- With `system` now a real category, any future OS/box-state flag (e.g. a degraded-boot marker) plugs into the same category as a pure follow-up.
