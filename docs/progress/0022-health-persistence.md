# 0022 — SQLite persistence for health issues

- **Status:** done
- **Date:** 2026-05-27
- **Specs touched:** `HEALTH.md` # Persistence, `LOGGING.md`, `BOOT.md`

Closes the in-memory-only gap noted in 0019. Active health issues now survive brain restarts via a `health_issues` SQLite table. The store write-through is transparent to callers: `Raise` and `Clear` still return transition bools; the Manager persists behind the scenes. Boot-time `LoadFromStore()` restores the registry before the HTTP server starts, so the dashboard never sees a transient "all clear" after restart.

Note: 0021 labeled "0022" as the LUKS/TPM-unseal slice. That work is now **0023** (the next testing-lane slice). The health persistence slice took this number because it was the next pending implementation slice.

## What was done

### `health_issues` table — `internal/store/store.go`

Added to the `migrate()` block alongside the existing tables. Schema:

```
id TEXT, instance_key TEXT (DEFAULT ''), category, severity, tier INTEGER,
blocks_writes / blocks_apps / blocks_users INTEGER (0/1),
summary, details, raised_at INTEGER (epoch ms), last_checked_at INTEGER (epoch ms)
PRIMARY KEY (id, instance_key)
```

`INSERT OR REPLACE` handles the upsert pattern. No triggers — unlike `audit_events`, health issues are legitimately mutable (raise → refresh → clear cycles). `instance_key DEFAULT ''` avoids NULL-handling for box-wide issues.

### Store methods — `internal/store/health.go` (new file)

Three methods on `*Store`, all imported from `internal/health` for the `Issue` type (store imports health; health does not import store — layer boundary preserved):

- `UpsertHealthIssue(health.Issue) error` — INSERT OR REPLACE, updates `last_checked_at` on re-raise
- `DeleteHealthIssue(id, instanceKey string) error` — idempotent, no error on missing row
- `ListHealthIssues() ([]health.Issue, error)` — SELECT * ordered by raised_at, for boot-time load only

### `HealthStore` consumer-side interface — `internal/health/health.go`

```go
type HealthStore interface {
    UpsertHealthIssue(Issue) error
    DeleteHealthIssue(id, instanceKey string) error
    ListHealthIssues() ([]Issue, error)
}
```

Declared in the consumer package (`health`) so the Manager doesn't import `store` (CLAUDE.md layer-boundary rule). `*store.Store` satisfies it.

### Manager changes — `internal/health/health.go`

- `NewManager(store HealthStore) *Manager` — takes the store at construction time; nil means no persistence (all existing tests pass nil)
- `Raise()` — persists the issue to the store outside the lock (reads the issue snapshot while holding the lock, releases, then calls `UpsertHealthIssue`). Both new raises and refreshes are persisted to keep `last_checked_at` current in SQLite
- `Clear()` — calls `DeleteHealthIssue` after releasing the lock, only on actual transitions
- `ApplyStorageFindings()` — collects `toUpsert` and `toDelete` slices under the single lock (preserving the atomic reconcile invariant), then calls the store after `mu.Unlock()`. This keeps the lock-free-from-I/O discipline without splitting the critical section
- `LoadFromStore() error` — new method, reads `ListHealthIssues()` and populates `m.active` under the lock; no-op when store is nil; non-fatal error path in main.go

### Brain wiring — `cmd/brain/main.go`

- `health.NewManager(st)` — passes the real store
- `healthMgr.LoadFromStore()` called after store init, before the HTTP server starts; error is Warn-logged, not fatal
- `pullStorageHealth` and `storageHealthPollLoop` now take `*audit.Recorder`; emit `health.issue.raised` / `health.issue.cleared` audit records on transitions (count-level records, system actor)

### Audit action constants — `internal/audit/audit.go`

```go
ActionHealthIssueRaised  = "health.issue.raised"
ActionHealthIssueCleared = "health.issue.cleared"
```

### Tests

**`internal/store/health_test.go`** (7 new tests): upsert + list round-trip, idempotent re-upsert, delete, no-op delete on missing row, multiple rows, instance-key round-trip, bool-field round-trip. All use the real SQLite via `open(t)`.

**`internal/health/health_test.go`** (10 new tests): Manager with `fakeStore` stub — raise calls UpsertHealthIssue, re-raise also upserts (refresh), clear calls DeleteHealthIssue, clear-of-nothing does not delete, `LoadFromStore` restores active set, nil-store safety for all three paths, `ApplyStorageFindings` persists via store.

All 23 health tests + all 29 store tests pass.

## How it maps to the specs

- `HEALTH.md` # Persistence: "Active issues live in the brain's SQLite (`health_issues` table). They survive brain restarts." — now realized.
- `HEALTH.md` # Persistence: "The history of raises and clears is in the `audit_events` table." — wired at the `pullStorageHealth` call site with count-level system records.
- `BOOT.md` # Startup sequence: health registry is restored from SQLite before the brain begins serving requests.

## Known gaps & deviations

- **Audit records are count-level, not per-issue.** `pullStorageHealth` emits one `health.issue.raised` record with `{"count": N}` rather than one record per issue ID. Per-issue granularity would require `ApplyStorageFindings` to return affected IDs rather than counts — deferred to the slice that extends the reconciliation API. *(Resolved in [0024](0024-per-issue-health-audit.md).)*
- **No API changes.** `GET /api/v1/health` already reads `m.List()` from the in-memory Manager — no change needed; the manager is now the authoritative view whether populated from live detection or from SQLite.
- **No notification wiring.** Health raise/clear → notification center is its own slice per `NOTIFICATIONS.md`.

## What's next

- **0023: LUKS root + first-boot TPM enrollment + unseal verification** (originally labeled 0022 in 0021's "what's next" — renumbered here). Builds on the QEMU medium-lane scaffolding from 0021.
- **Done ([0024](0024-per-issue-health-audit.md)):** Per-issue audit records. `ApplyStorageFindings` now returns affected `IssueKey`s so audit records target `{kind: "health_issue", id: "<id>"}` instead of a bulk count record.
- **Notification wiring.** `NOTIFICATIONS.md` — on health issue raise (transition), emit a notification to admin users.
