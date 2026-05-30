# 0010 — Session expiry + 5-minute elevation window

- **Status:** done
- **Date:** 2026-05-24
- **Specs touched:** `AUTH.md` (# Sessions, # Lifetime, # Invalidation, # Roles), `USERS_AND_GROUPS.md` (# Elevation in the UI)

Closes point 4 of the auth slice (0006 "What's next"). All four Tier-A auth items have now landed.

## What was done

### Part A — Session expiry

#### Store (`internal/store/store.go`)

- `Session` struct gains `ExpiresAt time.Time` and `ElevatedUntil time.Time`.
- `sessions` table DDL updated to include `expires_at INTEGER NOT NULL DEFAULT 0` and `elevated_until INTEGER NOT NULL DEFAULT 0`.
- Idempotent ALTER TABLE migration: probes `PRAGMA table_info(sessions)` and only adds the column if absent, so the migration is safe on both fresh and existing databases.
- Backfill: existing rows with `expires_at = 0` are updated to `created_at + 90 days` immediately after migration.
- `CreateSession` and `GetSession` updated to persist/scan both new columns.
- `SetElevatedUntil(token string, until time.Time) error` — marks a session elevated; called by the elevate handler.
- `ListSessionsForUser(userID string) ([]Session, error)` — returns all sessions for a user ordered by `created_at`; used in tests.

#### Auth (`internal/auth/auth.go`)

Three new constants:
- `SessionIdleWindow = 30 * 24 * time.Hour` — session is invalid if not seen within this window.
- `SessionHardCap = 90 * 24 * time.Hour` — absolute expiry set at issue time.
- `ElevationWindow = 5 * time.Minute` — how long a session stays elevated after `POST /auth/elevate`.

`Issue()` now sets `ExpiresAt = now + SessionHardCap`.

`Validate()` enforces both limits before looking up the user:
- Idle check: `now > last_seen_at + SessionIdleWindow` → delete row, return `ErrInvalidSession`.
- Hard cap: `now > expires_at` (and `expires_at != zero`) → delete row, return `ErrInvalidSession`.

`Identity` gains a private `elevated bool` field, computed by `Validate` at the Manager's clock so tests that fake the clock get correct results. `IsElevated()` reads this field.

`Elevate(token string) error` — new method; sets `ElevatedUntil = clock() + ElevationWindow` via the store.

`SessionStore` interface gains `SetElevatedUntil`.

### Part B — Elevation window

#### Audit (`internal/audit/audit.go`)

Two new constants:
- `ActionElevateSuccess = "auth.elevate.success"`
- `ActionElevateFailure = "auth.elevate.failure"`

Both follow the elevation-class rule: failure rows emitted at every observable failure path.

#### API — elevate handler (`internal/api/auth.go`)

`POST /api/v1/auth/elevate` (auth-required, not public):
- Body: `{password}`.
- Calls `host.VerifyPassword`; on failure: audit `auth.elevate.failure`, return 401.
- On success: `auth.Elevate(token)`, audit `auth.elevate.success`, return `{elevated_until: <unix>}`.

`requireElevated(ctx) error` — new helper (next to `requireAdmin`). Returns `huma.NewError(403, "elevation_required")` when the session is not elevated. Callers wire it **after** `requireAdmin` so members get `admin_required`, not `elevation_required`.

#### API — user-CRUD wiring (`internal/api/users.go`)

`requireElevated` added after `requireAdmin` in four handlers:
- `createUser` (`POST /api/v1/users`)
- `updateUserRole` (`PATCH /api/v1/users/:id`)
- `deleteUser` (`DELETE /api/v1/users/:id`)
- `resetUserPassword` (`POST /api/v1/users/:id/password`)

`listUsers` (`GET /api/v1/users`) is **not** elevation-gated — it's read-only.
`changeMyPassword` (`POST /api/v1/me/password`) is **not** elevation-gated — per spec, self-service is exempt (user just proved their current password).

#### Self-service password change session revocation (`internal/api/users.go`)

`changeMyPassword` now calls `DeleteSessionsForUser` after a successful `SetPassword`, per `AUTH.md` # Invalidation. This was flagged as a gap in 0008 and 0009.

### Tests

#### `internal/auth/auth_test.go` — 7 new tests

- `TestValidateRejectsIdleExpiredSession` — session invalid after `SessionIdleWindow`; row deleted.
- `TestValidateRejectsHardCapExpiredSession` — session invalid after `SessionHardCap`; row deleted.
- `TestValidateStillValidBeforeExpiry` — session valid just under the idle window.
- `TestIssueSetExpiresAt` — `ExpiresAt = now + SessionHardCap` persisted on issue.
- `TestElevateAndIsElevated` — `Elevate` sets `ElevatedUntil`; `IsElevated()` true immediately after.
- `TestIsElevatedFalseAfterWindowExpires` — `IsElevated()` false once window elapses.
- `TestIsElevatedFalseWithoutElevate` — `IsElevated()` false on fresh session.

#### `internal/api/auth_test.go` — 4 new tests

- `TestElevateHappyPath` — 200 + `elevated_until` + `auth.elevate.success` audit row.
- `TestElevateWrongPasswordFails` — 401 + `auth.elevate.failure` audit row.
- `TestElevateRequiresAuth` — 401 without session.
- `TestSessionIdleExpiry` — manually rewinds `last_seen_at` via store, then `/me` returns 401.
- `TestUserCRUDRequiresElevation` — all four elevated endpoints return 403 before elevation; `POST /users` succeeds after.

#### `internal/api/users_test.go` — updated existing tests

All 18 existing user-CRUD tests that exercised the admin happy-path now call `h.elevate(password)` immediately after `h.setupAdmin`. The broken-host tests additionally seed a real bcrypt hash in the fake host-agent's password map so `verify-password` (used by elevate) succeeds.

## How it maps to the specs

- `AUTH.md` # Lifetime — 30-day rolling idle + 90-day hard cap realized.
- `AUTH.md` # Invalidation — session deleted on expiry (not just rejected); self-service password change now revokes all sessions.
- `USERS_AND_GROUPS.md` # Elevation in the UI — 5-minute window, `POST /auth/elevate` endpoint, `requireElevated` guard on user-management mutations.
- CLAUDE.md "elevation-class mutations audit success and failure" — `auth.elevate.failure` emitted on wrong password; `auth.elevate.success` on success.

## Known gaps & deviations

- **Recovery code for promoted admins** — `PATCH /api/v1/users/:id` (role change to admin) does not generate a recovery code. Still deferred.
- **argon2id vs. bcrypt** — recovery hash uses bcrypt, same as before. Hardening pass deferred.
- **Rate-limiting on `/login` and `/elevate`** — still deferred.
- **"Sign out everywhere"** — `DELETE /api/v1/sessions` or similar not yet built; only implicit revocation on password change/recovery.

## What's next

- Recovery code generation on admin promotion.
- Rate-limiting on `/login` and `/elevate`.
- Real PAM in host-agent (swap bcrypt map for `pam_authenticate`).
- Per-protocol opt-in (SSH/SMB) as service allowlists per account.
