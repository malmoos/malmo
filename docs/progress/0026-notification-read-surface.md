# 0026 — Notification read surface: bell API + SSE + per-recipient read state

- **Status:** done
- **Date:** 2026-05-29
- **Specs touched:** `NOTIFICATIONS.md` (read-state implementation note — uniform `notification_reads` join; no decision flipped)

The read half of the bell. [0025](0025-health-notifications.md) landed the write seam (the `notifications` table + the health raise/clear emitter) but nothing read it back over the wire. This slice adds the `/api/v1/notifications` API family, the `notification_reads` join that makes read/dismiss state per-recipient, and the SSE `notification.created` / `notification.updated` kinds so the dashboard bell can update live. Backend only — the Vue bell is still deferred.

## What was done

### `notification_reads` join table — `internal/store/store.go`

`migrate()` gains a `notification_reads(notification_id, user_id, read_at, dismissed_at, PRIMARY KEY (notification_id, user_id))` table, both foreign keys `ON DELETE CASCADE`. Read/dismiss state for **every** recipient lives here uniformly — including `audience: user` rows, which take the same join rather than a per-row fast path. The decision (over the spec's sketched row-column shortcut for `audience: user`) is one code path instead of two; the read query joins regardless, so the shortcut bought nothing. The row-level `read_at` / `dismissed_at` columns on `notifications` (created in 0025) stay reserved/unused; row `dismissed_at` remains only the coalescing-index marker. `NOTIFICATIONS.md` # Read / unread / dismiss is updated to describe the uniform join.

### Audience-scoped, read-state-aware store queries — `internal/store/notify.go`

- `NotificationFilter{UserID, IsAdmin, AfterID, Limit, IncludeDismissed}` and `notificationVisibilityClause(isAdmin)` — the SQL predicate that scopes rows to a recipient (`NOTIFICATIONS.md` # Routing): an admin sees `audience='admins'` plus their own `audience='user'` rows; a member sees only their own. Each branch binds exactly one parameter (the caller's user id), mirroring `listAudit`'s member-vs-admin pattern.
- `ListNotificationsForRecipient(f)` — newest-first, audience-scoped, with this caller's `read_at`/`dismissed_at` `LEFT JOIN`ed in; excludes dismissed rows unless `IncludeDismissed`; cursor by `id < AfterID`. `scanNotification` reads the 17-column row shape (notification columns + the joined per-recipient state).
- `CountUnreadNotifications(userID, isAdmin)` — the bell badge: visible rows where the join's `read_at IS NULL AND dismissed_at IS NULL`.
- `GetNotification(id)` — single-row fetch (→ `ErrNotFound`), used by the per-id mutating handlers to confirm visibility before recording state.
- `MarkNotificationRead` / `DismissNotification` — UPSERT into `notification_reads` with `ON CONFLICT … DO UPDATE SET … = COALESCE(existing, excluded)`, so a repeat call preserves the first-read / first-dismiss timestamp (idempotent). Per-recipient: one admin dismissing a box-wide notice doesn't dismiss it for another.
- `MarkAllNotificationsRead(userID, isAdmin, at)` — one `INSERT … SELECT … ON CONFLICT DO UPDATE` over every still-unread visible row.
- **Re-raise re-surfaces unread.** `RaiseNotification`'s coalesce branch now `DELETE`s `notification_reads` for the active row, so a genuine cleared→active flap clears per-recipient read/dismiss state and the badge re-lights (`NOTIFICATIONS.md` # One notification per raise: "while unread").

### `/api/v1/notifications` family — `internal/api/notifications.go` (new), `internal/api/api.go`

`registerNotifications` (wired into `Handler()` after `registerHealth`):

- `GET /api/v1/notifications` — the caller's inbox, newest-first, cursor (`limit` default 50 / cap `maxNotificationLimit=100`, `after_id`).
- `GET /api/v1/notifications/unread-count` — the badge.
- `POST /api/v1/notifications/{id}/read` — mark one read (204).
- `POST /api/v1/notifications/read-all` — mark all visible read (204).
- `POST /api/v1/notifications/{id}/dismiss` — drop one from the active inbox (204).

Not admin-gated like `/health` — every authenticated user sees the notifications addressed to them (admins also see box-wide ones). `NotificationDTO` folds this caller's read state into a `read` bool and exposes only what the client renders — routing fields (`audience`, `variant`, `user_id`) and source identifiers stay server-side. The shared `notificationRecipient` guard answers **404 (never 403)** for both a missing id and a row the caller can't see, so the inbox leaks nothing about which ids exist or who else they address. Mutations publish `events.NotificationUpdated` onto the bus.

### SSE kinds + bus wiring — `internal/events/events.go`, `internal/notify/notify.go`, `cmd/brain/main.go`

- New `events.Kind`s `notification.created` / `notification.updated` (`BRAIN_UI_PROTOCOL.md`).
- `notify` gains a consumer-side `Publisher` interface (`Publish(kind, data)`); `events.Bus` implements it. `New(store, pub)` takes it (nil disables emission — the bell is a floor, not a gate). `HealthRaised` publishes `notification.created` (advisory payload: `dedup_key`, `category`, `severity`) **after** a successful raise; `HealthCleared` publishes `notification.updated` (`dedup_key`) after resolve. `cmd/brain` passes the existing bus: `notify.New(st, bus)`.
- The SSE payload is an advisory **refetch trigger, not a data channel** — the global `/api/v1/events` bus is unfiltered, so the client re-reads its own audience-scoped list on the nudge rather than receiving notification bodies over a shared bus (`WEB_UI.md`: SSE is a cache-invalidation channel). This is what keeps per-recipient scoping correct without per-subscriber bus filtering.

### Tests

- `internal/store` (extends `notify_test.go`): audience scoping (admin sees admins + own, not another member's; member sees only own; unrelated sees none), excludes-dismissed, cursor, unread count, mark-read preserves first-read timestamp, **per-recipient** dismiss, mark-all-read, **re-raise clears read state**, `GetNotification` not-found.
- `internal/api` (new `notifications_test.go`): the full surface behind the auth fence returns 401 unauthenticated; audience scoping over the wire (admins-audience reaches every admin but no member; user-audience reaches only its owner, not other members, not admins); mark-read drops the badge and flips `read` while keeping the row; dismiss removes it from the active inbox; mark-all-read zeroes the count; **404 (not 403)** for a missing id, a foreign user row, and a member reaching an admins-audience row; `limit` query binding honored.
- `internal/notify` (extends `notify_test.go`): a `fakePublisher` asserts `notification.created` on raise and `notification.updated` on clear, and **no** publish when the store errors or the issue isn't allowlisted.

## How it maps to the specs

- `NOTIFICATIONS.md` # Read / unread / dismiss: per-recipient read state via the `notification_reads` join; unread badge; dismiss ≠ resolve (the underlying condition stays).
- `NOTIFICATIONS.md` # Routing: the list/count visibility predicate is audience + ownership — box-wide → admins, personal → owner.
- `NOTIFICATIONS.md` # Surfaces: live updates over the existing global SSE channel; the new `notification.created` / `notification.updated` kinds.
- `NOTIFICATIONS.md` # One notification per raise: a re-raise re-surfaces unread (read state cleared on coalesce).
- `BRAIN_UI_PROTOCOL.md`: the `/api/v1/notifications` endpoint family and the two SSE kinds.
- CLAUDE.md # Go code discipline: consumer-side `Publisher` interface (in `notify`, not `events`); the layer-boundary rule (`api` → `store`); `log/slog` only; tests in-package.
- CLAUDE.md # Elevation-class mutations audit: mark-read / dismiss are per-user **view-state** toggles, not principal/app mutations, so they are deliberately **not** audited (pure-read-class, like the bell badge itself).

## Known gaps & deviations

- **Backend only — no Web UI.** No bell, dropdown inbox, Pinia store, or `useNotifications()` composable yet (`WEB_UI.md`). The API + SSE this slice ships is what that UI will consume.
- **No per-category mute.** `GET`/mark/dismiss exist; the per-user, per-category mute (`NOTIFICATIONS.md` # Configuration) is deferred — it's a preferences surface, orthogonal to read state.
- **Member transparency variant still deferred.** Box-wide criticals notify admins only; the info-only member `transparency` copy (`NOTIFICATIONS.md` # Member transparency variant) hasn't landed (carried over from 0025). `AudienceUser` is now exercised by tests but no producer emits it yet.
- **No "all clear" resolved notification.** Clear still marks the original resolved without the brief info "Data drive reconnected" follow-up (`NOTIFICATIONS.md` # Clears) — carried over from 0025.
- **SSE payload is advisory, not a stream of bodies.** Clients refetch on the nudge. There's no replay buffer and no per-stream cap on the bus (`BRAIN_UI_PROTOCOL.md` defers both); a client connecting after an event missed the nudge re-syncs on its next poll / reconnect.
- **No retention/pruning.** Carried over from 0025 — the capped-count / age policy (`NEXT.md`) is still unimplemented; `notifications` and `notification_reads` rows accumulate.

## What's next

- **Web UI.** Bell + dropdown inbox in the chrome, Pinia store, SSE subscription on the two new kinds, `useNotifications()` (`WEB_UI.md`).
- **Member transparency variant + "all clear."** Emit the info-only member copy for box-blocking criticals and the resolved follow-up on clear (`NOTIFICATIONS.md`).
- **Per-category mute.** Per-user mute preferences; everything on by default (`NOTIFICATIONS.md` # Configuration).
- **Retention/pruning.** Capped-count / age policy for both tables (`NEXT.md`).
