# Dashboard login rate-limit + lockout

- **Status:** done
- **Date:** 2026-06-03
- **Specs touched:** `LOGGING.md` — added `login.lockout` to the v1 action vocabulary table (the action is implicit in `AUTH.md` # Rate limiting's "all failed attempts logged" requirement but wasn't listed). `CLAUDE.md` — added `retry_after` to the standard structured-field list. No `DECISIONS.md`/`NEXT.md` change — the design was specified, not redesigned.

Closes issue #8. The brain login endpoint went username-lookup → `VerifyPassword` (a deliberately-expensive PAM round-trip) → 401 with no throttling, so the PAM call — the asset `AUTH.md` # Rate limiting exists to protect — was reachable on every repeated guess. This adds the per-username backoff/lock and per-IP token bucket exactly as specced, gating *before* PAM.

## What was done

**New `auth.LoginThrottle`** (`internal/auth/throttle.go`) — in-memory, clock-injectable (mirrors `Manager.Clock`), two independent gates:

- **Per-username exponential backoff → lock** (`AUTH.md`: 3 fails → 1s, 5 → 10s, 10 → 60s, 20 → 15-minute lock). `usernameBackoff(fails)` maps the consecutive-failure count to the required cooldown since the last failure; at 20 the cooldown becomes the full 15-minute lock.
- **Per-IP token bucket** (10 attempts/minute, continuous refill). The backstop against username-spray: a fresh username carries no per-username delay, so only the IP gate slows a spray from one source. It **throttles, never locks** — banning a LAN address is wrong (`AUTH.md`: "most boxes only see LAN IPs").

Three methods: `AllowAttempt(username, ip)` (called before PAM — spends one IP token, then checks the username backoff/lock; returns `ok=false` + a best-effort retry hint, same shape for both gates so a rejection never leaks whether the username exists); `RecordFailure(username)` (called after a failed `VerifyPassword`; increments the per-username counter, returns `lockedNow` — true only on the failure that *crosses* into the lock, so the caller audits `login.lockout` once per lock, not per subsequent attempt); `RecordSuccess(username)` (resets the per-username counter — `AUTH.md`: "Successful login resets the per-username counter").

**Login handler gated** (`internal/api/auth.go` `login`): `AllowAttempt` runs **before** the `GetUserByUsername`/`VerifyPassword` round-trip. On rejection it returns `429 Too Many Requests` with the retry hint in the message and an `slog.Warn` (the failures that built the backoff were already audited; a throttle rejection isn't re-audited to avoid a flood writing audit rows). On a failed verification it calls `RecordFailure` (keyed on the *supplied* username, existing or not, so the throttle can't enumerate accounts) and, on the lock crossing, emits the new `login.lockout` audit. A clean login calls `RecordSuccess`.

**New audit action** `ActionLoginLockout = "login.lockout"` (`internal/audit/audit.go`), emitted `success=false` with `{username}` + source IP (via the existing `audit.WithClientIP` ctx), mirroring `login.failure`.

**Wiring** (`internal/api/api.go`): `Server` gains a `throttle *auth.LoginThrottle`, constructed in `NewServer` (mirrors how `streamCap` is owned, not injected).

## How it maps to the specs

- `AUTH.md` # Rate limiting — implements the per-username backoff schedule, the 15-minute lock at 20, the per-IP 10/min throttle (logs, never bans), and success-resets-the-counter, verbatim. The section's "login path only" scoping is honored: this lives in the login handler, distinct from the general API rate-limiting (`BRAIN_UI_PROTOCOL.md` # Rate limiting & abuse), which is a separate concern and untouched here.
- `AUTH.md` # Identity primitive / line 250 ("rate-limits on no") — the gate sits exactly where the spec says, in front of the PAM `verify_password` call.
- `LOGGING.md` — `login.lockout` joins the v1 action vocabulary alongside `login.failure`; emitted on both the success-of-the-event sense (it's a real lock) and as a `success=false` security event.
- `CLAUDE.md` # Go discipline — consumer-side ownership (the throttle lives in `internal/auth`, used by `internal/api`); `slog` structured fields (`username`, `host`, `retry_after`); no new store surface and no persistence (`AUTH.md` requires none); no premature abstraction (concrete type, no interface — one consumer).

## Known gaps & deviations

- **Admin unlock is not built.** `AUTH.md` # Rate limiting also describes "an admin can clear a lock from the user-management UI." That's a UI + endpoint piece outside #8's scope (the issue's "Done when" doesn't include it); the limiter already exposes the reset primitive (`RecordSuccess`) a future unlock endpoint would call. The 15-minute lock still self-expires, so an account is never permanently wedged.
- **No `Retry-After` header.** The retry hint rides the `429` body/message, not the header. AUTH.md scopes the `429`/`Retry-After` *header* contract to the general-API plane (`BRAIN_UI_PROTOCOL.md`), not the login path; adding it here would need a header on huma's error path. Left out deliberately.
- **In-memory, unbounded maps.** State resets on brain restart (acceptable per `AUTH.md`). The `users`/`ips` maps grow one entry per distinct failing username/IP; for a LAN home box this is negligible and the per-IP gate bounds attacker-supplied username growth to 10/min. Eviction waits for a timer/scheduler seam the brain doesn't have yet (same deferral shape as the periodic image sweep).
- **Throttled attempts don't count as failures.** A request rejected by the gate never reaches PAM, so it doesn't advance the per-username counter (only real `VerifyPassword` failures do). This is the intended reading of "gate before PAM" and keeps a hammering attacker from inflating their own backoff into a permanent self-lock loop independent of real guesses.

## Tests

- `internal/auth/throttle_test.go` (clock-injected, fast): backoff tiers (`usernameBackoff` at every boundary), cooldown expiry, the lock crossing reported exactly once (and not re-reported at 21), success-reset, the per-IP bucket (10 allowed, 11th throttled, refill after ~6s), and that throttled probes don't escalate the tier.
- `internal/api/auth_test.go` `TestLoginLockoutAfterRepeatedFailures` (end-to-end over HTTP): 20 wrong-password logins reach the lock; a 21st with the **correct** password is still `429` (proving the gate precedes `VerifyPassword`); exactly one `login.lockout` audit row is recorded. The existing `TestLoginLogoutFlow` (2 failures then success) still passes — under the threshold, never throttled.
- `go test ./internal/auth/ ./internal/api/ ./internal/audit/` green; `go vet` + `gofmt` clean. (`internal/hostagent/pamverifier` needs `libpam0g-dev` headers absent on this dev box — unrelated to this change; it builds in CI.)

## What's next

- **Admin unlock UI + endpoint** — surface "Cindy is locked out, unlock her" in Settings → Users, calling a brain endpoint that clears the lock (`RecordSuccess`-equivalent). `AUTH.md` # Rate limiting describes it; deferred out of #8.
- **Limiter eviction** — prune stale `users`/`ips` entries once the brain grows a periodic-timer seam (shared with the deferred image-sweep timer).
