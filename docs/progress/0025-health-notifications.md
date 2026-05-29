# 0025 — Health raise/clear → dashboard notifications

- **Status:** done
- **Date:** 2026-05-29
- **Specs touched:** none (realizes `NOTIFICATIONS.md` as written; `HEALTH.md` # NOTIFICATIONS.md cross-reference already describes this)

First consumer of the notification seam (`NOTIFICATIONS.md`). The `notifications` table and an emitter now exist: on a health-issue raise *transition* the brain enqueues a notification routed to admins, coalesced by `dedup_key`; on clear it marks that notification resolved. This rides the per-issue transition keys [0024](0024-per-issue-health-audit.md) exposed — the same signal that drives per-issue audit records now also drives the bell.

## What was done

### `internal/notify` — the derivation + routing layer (new package)

A leaf package that maps health issues to persisted notifications. Imports `health` (for `health.Issue`); **does not** import `store` — it declares a consumer-side `NotificationStore` interface (`RaiseNotification`, `ResolveNotification`) that `store` implements, mirroring the `store`→`health` direction from [0022](0022-health-persistence.md).

- `Notifier` (like `audit.Recorder`): `HealthRaised(iss health.Issue)` and `HealthCleared(id, instanceKey string)`. Store errors are logged and swallowed — never propagated. The bell is a floor, not a gate (`NOTIFICATIONS.md` # Stance), so a notification write failing must not block the triggering operation.
- **Explicit curated allowlist** (`healthRules`, a `map[issueID]healthRule`) — the same discipline as HEALTH's `builtinDefinitions`. Registered issue IDs that `NOTIFICATIONS.md` lists: `data-drive-missing`, `data-drive-wrong`, `data-drive-readonly` (storage) and `canary-mismatch`, `mergerfs-assembly-failed` (system). Three have a live detector today (`internal/storageverify` emits `data-drive-missing`, `data-drive-wrong`, `canary-mismatch`); `data-drive-readonly` and `mergerfs-assembly-failed` are pre-registered ahead of their detectors, mirroring HEALTH's own pre-registration pattern — harmless, and they notify the moment their detector lands. Each carries its notification category and an `Open Storage` → `/settings/storage` action. Severity is copied verbatim from the issue (never reassigned). `HealthCleared` gates on the same allowlist so clearing a non-notifying issue is a true no-op.
- `dedup_key` = `health:<id>` (`+ ":"+instanceKey` when per-instance), stable across raise and clear so a clear resolves exactly the notification its raise created.

### `notifications` table + store methods — `internal/store/store.go`, `internal/store/notify.go`

- Table created in `migrate()` with the **full spec shape** (`NOTIFICATIONS.md` # The notification model): `id, ts, category, severity, source_kind, source_id, dedup_key, audience, user_id, variant, summary, body, action_label, action_route, read_at, dismissed_at, resolved_at`. The columns the write seam doesn't use yet (`read_at`, `dismissed_at`) exist so later slices add behavior without a migration. A partial unique index `notifications_active_dedup ON (dedup_key) WHERE dismissed_at IS NULL` enforces the coalescing invariant — at most one *active* notification per key — at the DB level.
- `RaiseNotification` coalesces: `UPDATE … WHERE dedup_key=? AND dismissed_at IS NULL` (bumps ts/severity/summary/body/action, clears `resolved_at`); on zero rows affected, `INSERT`. Writers are serialized (`SetMaxOpenConns(1)`), so update-then-insert is race-free. A re-raise after a resolve un-resolves the row (the drive-flap / cross-restart path).
- `ResolveNotification(dedupKey, at)` sets `resolved_at` on the active row; no-op when none matches — resolved, not deleted, so the timeline stays honest (`NOTIFICATIONS.md` # Clears).
- `ListNotifications()` returns rows newest-first, unfiltered — enough for tests; the audience/read-state-aware list the bell API needs lands with that slice.

### `health.Manager.Get` + `cmd/brain` wiring — `internal/health/health.go`, `cmd/brain/main.go`

- New `Manager.Get(id, instanceKey) (Issue, bool)` accessor: `ApplyStorageFindings` returns only the transitioned *keys*, but a raise notification needs the issue's severity/summary, so the emitter reads the live `Issue` by key. Returns `ok=false` for a cleared/never-raised key.
- `notifier := notify.New(st)` constructed in `main()`; threaded through `pullStorageHealth` and `storageHealthPollLoop`. A new `emitHealthNotifications(notifier, healthMgr, raised, cleared)` runs right after the existing `emitHealthTransitions` audit emission — `Get`-lookup + `HealthRaised` per raised key, `HealthCleared` per cleared key. A raised key no longer active at dispatch (cleared again before the call ran) is skipped, not nil-dereferenced.

### Spec sync — `NOTIFICATIONS.md`

This slice is the first to make the issue→notification severity mapping observable (severity is copied verbatim from the source issue, the locked principle), which surfaced two illustrative-table cells that disagreed with the binding HEALTH.md/code severities. Corrected the allowlist tables: `data-drive-missing` is `error` (not `critical` — it was grouped with `data-drive-readonly`/`data-drive-wrong`, which are critical), and `mergerfs-assembly-failed` is `error` (not `critical` — it was grouped with `brain-db-corrupt`/`canary-mismatch`). No decision flipped; the tables now match `HEALTH.md` # Taxonomy.

### Tests

- `internal/notify`: allowlisted raise produces a notification with the right category/severity/dedup_key/audience/variant/action; the system-category issue maps to `system`; **non-allowlisted IDs produce nothing** (`health-report-malformed`, `store-write-failed`, made-up IDs — the case the explicit allowlist defends that a "non-network storage" heuristic would get wrong); instance-key in dedup_key; clear resolves; non-allowlisted clear is a no-op; a store error is swallowed (no panic).
- `internal/store`: create, coalesce-on-re-raise (one row, refreshed), distinct keys → distinct rows, resolve sets `resolved_at` (row retained), resolve-of-missing no-op, re-raise-after-resolve un-resolves.
- `cmd/brain` (extends `main_test.go`): `emitHealthNotifications` resolves raised keys via `Manager.Get` and notifies, resolves cleared keys by dedup_key (`fakeNotifStore`); a raise of an inactive key is skipped.

## How it maps to the specs

- `NOTIFICATIONS.md` # Not a new event source: the notification is *derived* from the existing HEALTH raise/clear lifecycle — no parallel taxonomy.
- `NOTIFICATIONS.md` # Routing: health issues are box-wide → `audience: admins`, `variant: actionable`.
- `NOTIFICATIONS.md` # Lifecycle (coalescing): one notification per raise, keyed by `dedup_key`; resolved (not deleted) on clear; re-raise refreshes in place.
- `NOTIFICATIONS.md` # Storage note — mutable, unlike audit: the `notifications` table is separate from append-only `audit_events`, carries mutable `resolved_at`, and is prunable.
- `NOTIFICATIONS.md` # Locked decisions: curated, code-registered source allowlist bounded like HEALTH's issue set.
- CLAUDE.md # Go code discipline: consumer-side `NotificationStore` interface (in `notify`, not `store`); leaf package with no premature abstraction; `log/slog` only; tests in-package.

## Known gaps & deviations

- **Allowlist is explicit, not the heuristic the resume plan sketched.** An earlier plan note used "`category != network` AND `!NoPersist`"; the locked decision (`NOTIFICATIONS.md`: "curated source allowlist, registered in brain code, bounded like HEALTH's issue set") calls for an explicit list — and the heuristic would wrongly include `health-report-malformed` (a storage-category issue the spec deliberately omits from the bell). The explicit allowlist is strictly more faithful.
- **Write seam only — no read surface.** No bell UI, no `/api/v1/notifications` family (list/mark-read/dismiss/mute), no SSE `notification.created` / `notification.updated` kinds. A notification is written but nothing yet reads it back over the wire.
- **No per-recipient read state.** `read_at` / `dismissed_at` columns exist but are unused; the `notification_reads` join table (`NOTIFICATIONS.md` # Read state) is deferred. Coalescing currently keys on `dismissed_at IS NULL` (the active row) as the forward-compatible substitute for the spec's "while unread."
- **No member transparency variant.** Box-wide criticals notify admins only; the info-only member-facing `transparency` variant (`NOTIFICATIONS.md` # Member transparency variant) is deferred. `AudienceUser` is defined but unused.
- **No "all clear" resolved notification.** Clear marks the original resolved but does not emit the brief info "Data drive reconnected" follow-up (`NOTIFICATIONS.md` # Clears) — that pairs with the toast/SSE surface, deferred with it.
- **Storage category only.** Emission lives in `pullStorageHealth`; non-storage detectors (network/version/capacity) and the other spec sources (update outcomes, security audit actions, app lifecycle) wire their own hooks when they land. `body` is the raw issue `Details` for now — richer templated bodies come later.
- **No retention/pruning.** The capped-count / age policy (`NEXT.md`) isn't implemented; rows accumulate until that slice.

## What's next

- **Bell API + SSE.** `/api/v1/notifications` list (audience-scoped, newest-first, cursor), mark-read, dismiss, per-category mute; `notification.created` / `notification.updated` SSE kinds (`BRAIN_UI_PROTOCOL.md`). `ListNotifications` grows a filter then. *(Landed in [0026](0026-notification-read-surface.md) — list/unread-count/mark-read/mark-all-read/dismiss + both SSE kinds; per-category mute still deferred.)*
- **Per-recipient read state.** `notification_reads(notification_id, user_id)` join + unread-badge count per user. *(Landed in [0026](0026-notification-read-surface.md).)*
- **Member transparency variant.** Emit the info-only member copy for box-blocking criticals; the "all clear" resolved notification on clear.
- **Web UI.** Bell + dropdown inbox in the chrome, Pinia store, `useNotifications()` (`WEB_UI.md`).
