# 0009 — Recovery-code redemption (`POST /api/v1/recover`)

- **Status:** done
- **Date:** 2026-05-24
- **Specs touched:** `AUTH.md` (# Using the recovery code)

## What was done

### Store — `UpdateRecoveryHash` (`internal/store/store.go`)

`UpdateRecoveryHash(userID, newHash string) error` replaces the stored bcrypt
hash for an admin user in-place. Returns `ErrNotFound` on unknown user ID.
Used by the recover handler to rotate the code atomically with the host-side
password change.

### Audit vocabulary (`internal/audit/audit.go`)

Two new constants: `ActionRecoverSuccess = "recover.success"` and
`ActionRecoverFailure = "recover.failure"`. Both follow the elevation-class
audit rule (CLAUDE.md) — failure rows emitted at every observable failure path.

### Public path allowlist (`internal/api/auth.go`)

`"/api/v1/recover"` added to `publicPaths`. Recovery code IS the credential;
no session is required.

### `recover` handler + route (`internal/api/auth.go`)

`POST /api/v1/recover` accepts `{username, recovery_code, new_password}`.

**Ordering (matches AUTH.md # Using the recovery code + CLAUDE.md "brain commits first"):**

1. Look up user by username; always run `bcrypt.CompareHashAndPassword` even on
   unknown username so timing doesn't leak which usernames have recovery codes
   (mirrors the login handler).
2. Return 401 on unknown user or wrong code; emit `recover.failure` audit row.
3. Generate a new code + hash.
4. Store the new hash in brain SQLite (brain commits first — it is the durable
   side; host-agent is reconstructible).
5. Call `host.SetPassword`. On failure: restore the old hash (rollback so the
   recovery code stays usable for a retry), emit `recover.failure`, return 502.
6. Call `DeleteSessionsForUser` to revoke stale sessions (best-effort; failure
   not propagated — stale sessions age out naturally).
7. Return `{new_recovery_code: "..."}` (shown once, never persisted).

No session is issued — user logs in normally after recovery, so login emits its
own `login.success` row.

### Tests (`internal/api/auth_test.go`)

Seven new tests:

- `TestRecoverHappyPath` — success path: new code returned, old code rejected,
  old password rejected, new password accepted, rotation verified.
- `TestRecoverWrongCode` — 401 on wrong code.
- `TestRecoverUnknownUser` — 401 on unknown username (same as wrong code; no leakage).
- `TestRecoverMissingFields` — 422 on every combination of missing required fields.
- `TestRecoverHostFailureRestoresOldHash` — broken host-agent returns 502; old
  recovery hash is restored so the code is still valid for retry.
- `TestRecoverSessionsAreRevoked` — existing sessions are invalidated after
  successful recovery.
- `TestRecoverAuditsFailureOnWrongCode` — `recover.failure` row appears in
  admin audit log.

## How it maps to the specs

- `AUTH.md` # Using the recovery code — full happy-path + error semantics
  realized: bcrypt verify, constant-time-ish handling, forced password change,
  session revocation, fresh code shown once, old code consumed.
- `AUTH.md` # Order-of-operations rule — "check host-agent reachability before
  consuming the recovery code": implemented as rollback on host failure (the
  code in brain is restored to the old hash).
- `LOGGING.md` / CLAUDE.md "Elevation-class mutations audit success and failure"
  — `recover.failure` emitted on wrong code, unknown user, and host failure;
  `recover.success` on redemption.
- CLAUDE.md "brain commits first, host is reconstructible" — new hash written to
  SQLite before `SetPassword`; old hash restored on host failure.

## Known gaps & deviations

- **Recovery code for promoted admins** — `PATCH /api/v1/users/:id` (role change
  to admin) does not generate a recovery code. Noted in 0008; still deferred.
- **Session revocation on self-service password change** — `POST /api/v1/me/password`
  does not yet call `DeleteSessionsForUser`. Noted in 0008; still deferred.
- **argon2id vs. bcrypt** — `AUTH.md` says "argon2id" for the recovery hash;
  the implementation uses bcrypt (same family as what `newRecoveryCode` already
  used in `/setup`). Cost is fine at home-server scale; migrating to argon2id
  is a later hardening pass if desired.

## What's next

- Recovery code generation on admin promotion (`PATCH /api/v1/users/:id`).
- Session revocation on self-service password change — landed in 0010.
- Forced password change flag + login-flow routing.
