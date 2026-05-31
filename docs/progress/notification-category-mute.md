# Per-category notification mute

- **Status:** done
- **Date:** 2026-05-29
- **Specs touched:** docs/specs/NOTIFICATIONS.md, docs/specs/NEXT.md

## What was done

Implemented the brain-side of **per-user, per-category mute** (`NOTIFICATIONS.md` # Configuration) — the last v1 notification-config item. Backend only (table + store filter + API + tests); the settings-toggle UI is the follow-up slice, mirroring how the read surface ([notification-read-surface.md](notification-read-surface.md)) landed before its bell UI ([notification-web-ui.md](notification-web-ui.md)).

- **`notification_mutes` table** (`internal/store/store.go`). Keyed `(user_id, category)` — the PK alone fully models the mute. Presence of a row means the category is muted for that user; absence means on — so a new user has no rows and sees everything ("everything on by default"). Unmute is a `DELETE`. `ON DELETE CASCADE` on `user_id` cleans up when a user is deleted. Created in the `migrate()` DDL block — a fresh additive table, no `ALTER`/backfill needed.
- **Mute is a read-time filter, never emit-time suppression** (`internal/store/notify.go`). A box-wide `admins`/`members` notification is one row shared by many recipients, so it can't be withheld per-user at write time. A new `notificationMuteClause()` (`AND n.category NOT IN (SELECT category FROM notification_mutes WHERE user_id = ?)`) is threaded into the three aggregate read queries — `ListNotificationsForRecipient`, `CountUnreadNotifications`, and `MarkAllNotificationsRead`. The per-id `GetNotification` path (used by mark-read/dismiss) is deliberately **mute-agnostic**, so a user can still act on a specific notification in a muted category (e.g. one they read before muting).
- **Mark-all-read honors the mute filter.** A muted category is left untouched by mark-all, so unmuting later reveals its notifications in their true unread state rather than as already-read rows the user never saw — keeping the three aggregate read queries consistent.
- **Store CRUD** (`internal/store/notify.go`): `MuteNotificationCategory` (idempotent — `ON CONFLICT DO NOTHING`, a repeat mute is a no-op), `UnmuteNotificationCategory` (idempotent no-op when absent), `ListMutedCategories` (sorted).
- **Full category taxonomy in `notify`** (`internal/notify/notify.go`). The mute surface validates against the complete `storage | system | updates | security | account | app` set — a user can mute a category before its source exists (the spec's `updates`-chatter example) — so the four previously-undefined `Category` constants plus a `Categories` slice and `ValidCategory` helper now live in `notify`, the routing/derivation layer. Only `storage`/`system` have producers today; the rest light up as their sources land.
- **Wire surface** (`internal/api/notifications.go`): `GET /api/v1/notifications/mutes` (the caller's muted categories), `PUT`/`DELETE /api/v1/notifications/mutes/{category}` (mute/unmute, idempotent, 422 on an unknown category). Mute/unmute publish `notification.updated` on the SSE bus (mirroring the read-state handlers) so the caller's other tabs refetch the badge. Mutating a mute is **not audited** — a personal view preference, not an elevation-class action (`CLAUDE.md`).

Tests: `internal/store` covers hide-from-list/count, unmute-restores, per-user isolation, idempotency + sorted listing, mark-all-skips-muted (with unmute revealing the still-unread row), and audience-independence (a member muting drops their `members`-broadcast rows). `internal/api` round-trips the wire surface (mute hides + GET reflects + DELETE restores), rejects unknown categories (422 on PUT and DELETE), pins per-user isolation, and extends the auth-fence table to the three new routes. `internal/notify` pins `ValidCategory` over the full taxonomy. Full suite green with `-race` (excluding the PAM-cgo `host-agent`/`pamverifier` packages, which don't build in the sandbox).

## How it maps to the specs

- `NOTIFICATIONS.md` # Configuration — realized: per-user, per-category mute; everything on by default; presence-row model; read-time filter on list/count/mark-all. Added a "Delivery (as implemented)" note in the same change.
- `NOTIFICATIONS.md` # Knock-ons (`BRAIN_UI_PROTOCOL.md`) — the `/api/v1/notifications` family's "per-category mute" endpoints are now real (`GET` list, `PUT`/`DELETE` toggle).
- `NOTIFICATIONS.md` # Locked decisions — "Per-user, per-category mute; everything on by default. No quiet hours / severity tuning in v1." exercised exactly; severity stays non-tunable.
- Reuses the locked audience model and `notification_reads` read-state join from [notification-read-surface.md](notification-read-surface.md)/[notification-clears-transparency.md](notification-clears-transparency.md) unchanged.

## Known gaps & deviations

- **Mute hides all severities, including criticals.** Muting `storage` also hides a `data-drive-readonly` critical. Spec-faithful (the spec scopes mute to a category, defaults everything on, and the example is info chatter) and the user's explicit opt-in, but a "criticals always ring through" carve-out is plausible later — surfaced as an open question in `NEXT.md` # Observability, not decided here.
- **No settings UI yet.** This is the backend half; the settings-toggle UI (`WEB_UI.md`) is the next slice. The mute API is usable over the wire today but has no dashboard control.
- **No producer/emit change.** `internal/notify` and `cmd/brain` emit paths are unchanged — mute is purely a read-side filter plus the CRUD surface.

## What's next

- **Settings UI toggle for mute** (`WEB_UI.md`) — a per-category toggle list reading `GET /mutes` and calling `PUT`/`DELETE`, with the bell badge/list reflecting it live over SSE.
- **Retention / pruning** for `notifications` + `notification_reads` (`NEXT.md` # Observability) — the last v1 notification item; capped count/age, resolved-rows-first a candidate policy.
- Out of family but adjacent: SMART/`disk-full` detectors, update-outcome and security-audit notification sources (`NOTIFICATIONS.md` # The notification list) — each new source makes its category mute-able with no further work here.
