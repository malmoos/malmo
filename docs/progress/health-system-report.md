# Generalize health reporting: GET /v1/health/system + ApplyFindings + service-down

- **Status:** done
- **Date:** 2026-05-31
- **Specs touched:** docs/specs/HEALTH.md, docs/specs/BRAIN_HOST_PROTOCOL.md (knock-on already written), docs/specs/DECISIONS.md (2026-05-29, referenced)
- **Issue:** #34 (closes). Unblocks #35 container-restart-loop, #36 brain-db-corrupt, #37 version-mismatch, #38 ram-pressure (`resources`), #39 clock-not-synced (`time`).

## What was done (production code — compiles, gofmt-clean)

Generalized the single-purpose storage health pipeline into one cross-category locus-B report, and landed `service-down` as the first non-storage detector exercising the per-category reconcile.

**Two-axis category model (key design point).** Per onel's 2026-05-31 issue clarification, the *report/reconcile* taxonomy is a **separate axis** from the brain's *issue* taxonomy:
- `protocol.HealthCategory` (wire enum, pinned in full): `storage | drives | services | resources | time`. Partitions the locus-B report so each domain reconciles independently. Only `storage` + `services` are populated today; `drives`/`resources`/`time` are reserved so #38/#39/disk-smart land as pure follow-ups.
- `health.Category` (issue display/nature enum, unchanged): `storage | state | network | version | capacity`. `service-down` is display-Category `state` (HEALTH.md # State) but report-Category `services`.

Changes:
- **`internal/protocol/host.go`** — added `HealthCategory` typed enum (5 values) + `SystemHealth{checked_at, categories: map[HealthCategory][]Finding}` (the GET /v1/health/system wire shape). Added `InstanceKey` to `Finding` (service-down is per-unit). Kept `StorageHealth` as the on-disk `/run/malmo/health/storage.json` boot-artifact shape (host-agent folds it into the report's storage category — the boot reporter and `malmo-storage-verify` are unchanged).
- **`internal/health/health.go`** — `ApplyStorageFindings(StorageHealth)` → `ApplyFindings(category protocol.HealthCategory, findings []protocol.Finding)`. Clear-absent now scopes by `Definition.ReportCategory == category` (replaced the `iss.Category` scope). Added two `Definition` fields: `ReportCategory protocol.HealthCategory` (empty ⇒ brain-owned, never cleared by a system poll — this is what protects `store-write-failed`, which shares display-Category `state` with `service-down`) and `Debounce bool`. Added `service-down` definition (Category state, ReportCategory services, Debounce true, no block flags). All storage defs got `ReportCategory: storage`. Added `pending map[issueKey]int` for debounce (raise on 2nd consecutive bad sample; 1 good sample resets). `last_checked_at` stays refreshed every poll for active issues (existing raiseLocked behavior, preserved).
- **`cmd/brain/main.go`** — `pullStorageHealth`/`storageHealthPollLoop` → `pullSystemHealth`/`systemHealthPollLoop`. Polls `host.SystemHealth`, iterates `sh.Categories` (sorted for stable audit/log order), calls `ApplyFindings` per category, accumulates raised/cleared, emits audit + notifications once. Added `sort` import.
- **`internal/hostclient/hostclient.go`** — `StorageHealth()` → `SystemHealth()` (GET /v1/health/system).
- **`internal/hostagent/agent.go`** — route `GET /v1/health/storage` → `GET /v1/health/system`; handler `storageHealth` → `systemHealth` builds the category-keyed payload. Added `ServiceReporter` consumer-side interface + `Agent.Services` field. Storage category always present (empty when no source); services category present only when a reporter is wired (so the brain doesn't read "not measured" as "all up").
- **`internal/hostagent/servicehealth/servicehealth.go`** (new) — locus-B `service-down` detector: `systemctl is-active` over `CoreUnits` allowlist (`docker`, `caddy`, `avahi-daemon`, `chrony`, `smbd`; host-agent omitted — can't report on itself), one `service-down` finding per non-active unit with the unit as `instance_key`. `isActive` is injectable for tests.
- **`internal/hostagent/fake.go`** — added `FakeServiceReporter` (settable, for brain integration tests).
- **`cmd/host-agent-real/main.go`** — wired `a.Services = servicehealth.New()`; updated doc comment.

## How it maps to the specs

- HEALTH.md # Detector catalog "transport decision (locus B)": one `GET /v1/health/system` carrying findings across domains; `ApplyStorageFindings` → `ApplyFindings(category, …)` with per-category clear-absent/raise-present/atomic-batch. ✓
- HEALTH.md # Cross-cutting detector policy: debounce (2 bad / 1 good; locus-B service-down debounces, storage stays 1-shot = no behavioral change); last-checked always fresh. ✓
- HEALTH.md service-down row + # State: `systemctl is-active` over core-unit allowlist, per-unit instance_key, no block flags. ✓
- BRAIN_HOST_PROTOCOL.md already describes `/v1/health/system` (spec was ahead of impl — now realized). ✓
- Issue #34 clarification (2026-05-31): report category enum enumerated in full incl. `time`; service-down → `services`. ✓

## Known gaps & deviations

- **`store-write-failed` protection** is via the empty-`ReportCategory` filter, not display-Category. Confirmed correct: a `services` poll reconciles only `ReportCategory==services` issues, so `store-write-failed` (no ReportCategory) survives. Needs a regression test (see below).
- host-agent-real cannot be built locally without `libpam0g-dev` (pre-existing PAM cgo dep, unrelated to this change). The new lines (`servicehealth` import + `a.Services = servicehealth.New()`) are trivial; verify via `make check` on a box with PAM headers or the nspawn/qemu lanes. `make vet`/`make test-nopam` both transitively build `cmd/host-agent-real` (it imports `pamverifier`), so locally they were run as an explicit non-PAM package list instead — see Verification.

## Tests

Existing tests migrated to the new seam; the `TestApplyStorageFindings_*` set is renamed `TestApplyFindings_*` to track the method rename. New coverage for the issue's done-when:

- **`internal/health/health_test.go`** — migrated all `ApplyStorageFindings(StorageHealth{…})` call sites to `ApplyFindings(HealthCategoryStorage, …)`. `OnlyTouchesStorageCategory` → `OnlyTouchesItsReportCategory` (scoping is now ReportCategory-based; the `mdns-down` issue has no ReportCategory and survives). Added: (a) `ServiceDownDebounces` — no raise on the 1st bad sample, raise on the 2nd, clear on 1 good; (b) `DebounceResetsOnGoodSample` — an intervening good sample restarts the counter; (c) `StoragePollLeavesServiceDownAlone` — the locked cross-category isolation property; (d) `ServicesPollLeavesStoreWriteFailedAlone` — brain-owned (empty-ReportCategory) issue survives a services poll that clears service-down; (e) `RefreshesLastCheckedWithoutTransition` — last-checked-always-fresh.
- **`internal/protocol/host_test.go`** (new) — `TestHealthCategoryWireValues` pins the wire string of every `HealthCategory` constant (a rename is a breaking protocol change).
- **`internal/hostagent/servicehealth/servicehealth_test.go`** (new) — injected `isActive`: non-active unit → one `service-down` finding (instance_key=unit, state in details); all-active → nil; one finding per down unit; `New()` watches `CoreUnits` and never host-agent itself.
- **`internal/hostagent/agent_test.go`** — `/v1/health/storage` tests → `/v1/health/system` decoding `protocol.SystemHealth`. Storage category always present/non-nil (incl. on source error); services category present only when a reporter is wired; new `ServicesFromReporter` asserts the seeded service-down flows through.
- **`internal/api/health_test.go`** — `pull()` now polls `SystemHealth` and reconciles per category; harness wires a `FakeServiceReporter`. New `ServiceDownDebouncesThenSurfaces` drives the first cross-category detector end to end over the production wire (debounce → raise as state-category → clear on recover).

## Verification

- `gofmt -l` over all tracked Go files: **clean**.
- `go vet` + `go test` over the explicit non-PAM package set (protocol, health, store, hostclient, hostagent, hostagent/servicehealth, hostagent/healthsource, api, cmd/brain, cmd/host-agent, cmd/malmo-storage-verify, storageverify): **all pass** (`VET_EXIT=0`, `TEST_EXIT=0`). `make vet`/`make test-nopam` can't run as-is locally because `cmd/host-agent-real` transitively imports the PAM cgo `pamverifier` (no `libpam0g-dev` on this box) — the explicit list excludes only the two PAM packages.
- Real `systemctl is-active` over the core units on this systemd box confirmed the detector's contract: `is-active` prints the state on stdout and exits non-zero for non-active units, which `systemctlIsActive` reads while ignoring the exit code. Observed: `docker` active (exit 0, skipped); `caddy`/`chrony`/`smbd` inactive (exit 4) → each yields `service-down{instance_key=<unit>, details="<unit> is inactive"}`.
- Adversarial multi-lens review (correctness / spec / tests / discipline, each finding refutation-verified): 7 findings raised, 5 refuted, 2 confirmed — both the same doc-comment slip in `systemHealth` calling the wire bucket the "state category" instead of "services" (the exact report-vs-display blur this change exists to prevent). Fixed at `agent.go` plus a sibling in `servicehealth.go`'s package doc. No correctness or test-coverage gaps found.

## What's next (follow-ups, not blockers)

- Build/run `cmd/host-agent-real` (the `a.Services = servicehealth.New()` wiring) on a PAM-equipped box or the nspawn/qemu lane.
- Downstream detectors that now plug in as pure follow-ups against the reserved categories: `#38` ram-pressure (`resources`), `#39` clock-not-synced (`time`), disk-smart (`drives`), `#35` container-restart-loop, `#36` brain-db-corrupt, `#37` version-mismatch.
