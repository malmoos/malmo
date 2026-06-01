# Notification retention / pruning

- **Status:** done
- **Date:** 2026-05-31
- **Specs touched:** docs/specs/NOTIFICATIONS.md, docs/specs/NEXT.md

## What was done

Bounded the `notifications` table so it doesn't grow unbounded over the life of the box (`NOTIFICATIONS.md` # Storage note — *mutable, unlike audit*; `NEXT.md` # Observability). The notification store is the prunable counterpart to the append-only-forever `audit_events`; until now rows only ever accumulated. Backend only, no UI.

- **`store.PruneNotifications(now time.Time)`** (`internal/store/notify.go`). Two passes, **age-primary**:
  1. **Age pass** — `DELETE FROM notifications WHERE ts < ?` with the cutoff at `now − 90 days`. State-blind: `ts` is `NOT NULL` and monotonic, so this is a total order over the table. 90 days mirrors the session hard-expiry already in `store.go` — one retention number across the box.
  2. **Count pass** — only when the table is still over a **1000-row** ceiling: trim the oldest excess, `ORDER BY (resolved_at IS NOT NULL) DESC, ts ASC` so **resolved rows are dropped before active ones** (a cleared issue is history; a live issue is not), oldest-first within each group. The cap is a runaway ceiling, not a hard quota.
  - Per-recipient `notification_reads` children are removed via `ON DELETE CASCADE` (`store.go`; `PRAGMA foreign_keys=ON` is set per-connection in `store.Open`). `notification_mutes` is keyed by category, not notification, so it's untouched. The prune is **silent on the SSE bus** — aged/resolved/dismissed rows are already invisible in the bell and the notifier holds no in-memory state, so there's nothing to re-render.
  - **No transaction.** Writers are serialized (`SetMaxOpenConns(1)`) and each `DELETE` is independently correct and idempotent. The count pass reads `COUNT(*)` then deletes the excess in Go; the cap being soft, a benign ±1 skew from a concurrent raise self-corrects on the next run. (A plain `LIMIT ?` over a computed excess was chosen over a single-statement `LIMIT max(0, (SELECT count(*)…) − ?)` for readability and portability; same behavior.)
- **Boot + hourly loop** (`cmd/brain/main.go`). One prune at startup, then `notificationPruneLoop` on a ticker — structurally a copy of the existing `storageHealthPollLoop`, sharing the same `pollCtx` so it shuts down with the rest. Cadence is `MALMO_NOTIFY_PRUNE` (default `1h`); retention is housekeeping, not latency-sensitive. Every prune is best-effort: a failure logs at `warn` and the next tick retries — an unbounded table degrades gracefully (the bell just carries more history), so it never blocks boot or serving.

Tests (`internal/store/notify_test.go`, in-package): age cutoff (91-day row pruned, 89-day kept); cascade to `notification_reads` (asserts the child row is gone after prune — fails loud if the FK or pragma regresses); count-cap resolved-first (seeds one over the 1000-row cap with an equally-old resolved/active pair at the boundary and asserts the **resolved** row is the one dropped); no-op-within-caps + idempotency (a second prune changes nothing); and dedup-freed-for-re-raise (a pruned key re-raises cleanly with no `notifications_active_dedup` collision — proving the row was fully removed, not just hidden). The count-cap test seeds its 999 fillers in a single transaction so 1000+ rows stay sub-100ms. Full `internal/store` + `cmd/brain` suites green under `go vet` and `go test` (PAM-cgo `host-agent`/`pamverifier` excluded — they don't build in the sandbox).

## How it maps to the specs

- `NOTIFICATIONS.md` # Storage note — realizes "subject to retention (capped count / age)"; the sentence now names the implemented policy instead of deferring to `NEXT.md`.
- `NOTIFICATIONS.md` # Locked decisions — the existing "mutable, prunable `notifications` table" decision gains a concrete retention bullet (90-day age + 1000-row resolved-first ceiling, boot + hourly).
- `NEXT.md` # Observability — the "Notification retention / pruning" open item is resolved and removed. The adjacent "per-category mute vs. criticals" question is untouched (still deferred).
- Reuses the `ON DELETE CASCADE` from [notification-read-surface.md](notification-read-surface.md) and the `resolved_at` column from [notification-clears-transparency.md](notification-clears-transparency.md) unchanged.

## Known gaps & deviations

- **Cap is global, not per-recipient.** Retention bounds the shared `notifications` table; per-recipient `notification_reads` rows are bounded transitively via cascade. There's no per-user row budget — a single user can't be individually capped — which matches the one-row-many-recipients model (a box-wide notification is one row). The `NEXT.md` framing floated "per-recipient? global?"; global is the answer that fits the storage model.
- **Soft cap, eventually-consistent.** Between hourly runs the table can sit slightly over 1000 (and momentarily over by the raises since the last prune). Acceptable for a runaway ceiling; the bell already paginates.
- **No metrics surface.** A prune logs `aged`/`capped` counts at `info` when it deletes anything; there's no dashboard counter or health signal for "retention is doing work." Not needed in v1.
- **A pruned row can 500 a concurrent mark-read/dismiss (left as a known benign edge).** Those handlers check visibility with `GetNotification(id)` and then do a *separate* blind insert into `notification_reads`; if the cap pass hard-deletes that row in the gap between the two serialized statements (`SetMaxOpenConns(1)`), the insert trips the `foreign_keys=ON` constraint and the request 500s. The prune loop is the first concurrent deleter of live notification ids, so this TOCTOU is newly reachable — but only for a row that is resolved, beyond the 1000 newest, and acted on at the exact prune tick, and it self-heals on the next list refresh. Left undefended rather than wrapping the handlers in driver-specific FK-violation handling (`CLAUDE.md`: no error handling for impossible scenarios).

## What's next

- **Mute settings UI** (`WEB_UI.md`) — the remaining half of the notification follow-up item (per-category toggle list over the mute API from [notification-category-mute.md](notification-category-mute.md)); in flight separately.
- If retention ever needs to be visible/tunable by the user, the env knob (`MALMO_NOTIFY_PRUNE`) and the two constants (`notifyMaxAgeDays`, `notifyMaxRows`) are the seams — no UI for them in v1.
