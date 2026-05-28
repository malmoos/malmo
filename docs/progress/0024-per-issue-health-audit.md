# 0024 — Per-issue health audit records

- **Status:** done
- **Date:** 2026-05-29
- **Specs touched:** `LOGGING.md` # v1 action vocabulary, `HEALTH.md` # cross-references

Closes the "audit records are count-level, not per-issue" gap noted in [0022](0022-health-persistence.md) # Known gaps. The storage-health reconciler now returns the exact issue keys that transitioned, and `cmd/brain` emits one audit record per raised/cleared issue targeting `{kind: "health_issue", id: "<id>"}` — so the Activity view answers "which issue raised?" instead of "N issues raised."

## What was done

### `ApplyStorageFindings` returns affected keys — `internal/health/health.go`

Signature changed from `(raised, cleared int)` to `(raised, cleared []IssueKey)`. The reconcile loop appends the transitioning `IssueKey{ID, InstanceKey}` instead of incrementing a counter; both slices are sorted by (ID, InstanceKey) before return so the per-issue audit records (and tests) see a stable order, matching `List()`'s existing convention. `IssueKey` already existed (exported, used by `BatchUpsertAndDelete`) — no new type. The atomic-reconcile critical section, the `BatchUpsertAndDelete` store write, and unknown-ID dropping are unchanged.

### Per-issue audit emission — `cmd/brain/main.go`

`pullStorageHealth` reconciles, then calls a new `emitHealthTransitions(ctx, auditor, raised, cleared)` helper that emits one `audit.Record` per issue with `Target{Kind: "health_issue", ID: k.ID}` and nil metadata, replacing the two bulk `{"count": N}` records. The reconcile log line uses `len(raised)` / `len(cleared)`. The emit loop was extracted into its own function so the headline behavior is directly testable (`cmd/brain` otherwise has no fake seam — `pullStorageHealth` takes a concrete `*hostclient.Client`).

### Spec sync — `LOGGING.md`, `HEALTH.md`

- `LOGGING.md` # v1 action vocabulary: added the `health.issue.raised` / `health.issue.cleared` rows that 0022 added in code but never recorded in the table (the doc's own rule: "Additions require a new entry here"). Added `health_issue` to the `target_kind` enumeration comment and a `cmd/brain` storage-health-poll bullet to the Write-path call-site list.
- `HEALTH.md` # LOGGING.md cross-reference: corrected the action name from `health.issue_raised` (underscore) to the dotted `health.issue.raised` / `health.issue.cleared` that the code uses and the rest of the vocabulary follows; noted one record per issue with `target_kind: health_issue`.

### Tests — `internal/health/health_test.go`, `cmd/brain/main_test.go`

- `internal/health`: updated the two tests that asserted on the int return (`RaiseAndClear`, `UnknownIDsAreDropped`) to assert on slice length and key IDs. Added `TestApplyStorageFindings_ReturnsAffectedKeys` — raises **three** issues in non-sorted input order and asserts the full sorted slice (three, not two, so the sort is load-bearing; with two keys Go's weak map-iteration randomization lets an unsorted impl pass too often to guard the contract), then a poll that drops two and asserts both cleared keys come back sorted. Added `TestSortIssueKeys_TieBreakOnInstanceKey` to pin the secondary InstanceKey sort directly (unreachable via `ApplyStorageFindings` today since `Finding` has no InstanceKey field).
- `cmd/brain` (new test file): `TestEmitHealthTransitions_OneRecordPerIssue` asserts one record per transitioned issue with the right action constant, `target_kind: health_issue`, `target_id`, `success=true`, and a system actor (no identity in ctx); `TestEmitHealthTransitions_NoTransitionsNoRecords` pins the steady-state no-op. Backed by a `fakeEventStore` satisfying `audit.EventStore`.

### Drive-by fix — `internal/api/health_test.go`

0022 changed `health.NewManager` to require a `HealthStore` argument but never updated this api test harness, so `internal/api`'s test package had not compiled since 0022 (`health.NewManager()` called with no args). Fixed to `health.NewManager(nil)` (in-memory, matching the "pass nil for tests that don't need persistence" convention). This unblocked the api-level health tests that exercise the reconcile path.

## How it maps to the specs

- `HEALTH.md` # Persistence: "The history of raises and clears is in the `audit_events` table" — now per-issue, so a support flow or diagnostic bundle can attribute each transition to a specific issue ID.
- `LOGGING.md` # Write path: `audit.Record` is called with a populated `Target{Kind, ID}`, mirroring how `user.*` / `app.*` records target their principal/app. `target_kind: health_issue` is now in the documented enumeration.
- CLAUDE.md # Go code discipline (standard structured fields): the audit target uses `target_kind` / `target_id` for the issue.

## Known gaps & deviations

- **Box-wide issues only, in practice.** `Finding` has no `InstanceKey` field yet, so every key returned today carries `InstanceKey == ""`. The return type and sort already handle the per-instance shape (`IssueKey` round-trips `InstanceKey`); when per-instance storage findings land, the audit `Target.ID` will still be the bare issue ID — surfacing the instance key in metadata is deferred to that slice.
- **Storage category only.** Audit emission lives in `pullStorageHealth`. When non-storage detectors (network/version/capacity) start raising issues through other call paths, each needs its own per-issue audit hook (or a shared emitter if one emerges). `Raise` / `Clear` already return transition bools for that.
- **No notification wiring.** Health raise/clear → dashboard bell is the next slice (`NOTIFICATIONS.md`); it will consume the same per-issue transition signal this slice now exposes.

## What's next

- **Notification wiring.** `NOTIFICATIONS.md` — first consumer of the notifications seam: on health-issue raise (transition), enqueue a notification routed to admin users; on clear, mark resolved. The per-issue keys this slice returns are exactly the input that emitter needs.
