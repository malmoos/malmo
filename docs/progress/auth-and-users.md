# 0006 — Auth + initial user model

- **Status:** done
- **Date:** 2026-05-23
- **Specs touched:** `AUTH.md`, `USERS_AND_GROUPS.md`, `BRAIN_HOST_PROTOCOL.md`, `BRAIN_UI_PROTOCOL.md`, `FIRST_RUN.md`, `WEB_UI.md`

The walking-skeleton brain had no authentication — anything reaching `:8080`
could install or uninstall apps. This slice closes that gap end-to-end:
first-admin bootstrap, password login, opaque cookie sessions, server-side
revoke, and a UI router that picks the right view based on box state.

## What was done

### Wire-level seam — fake host-agent (`cmd/host-agent`)

Three new endpoints, **real `BRAIN_HOST_PROTOCOL.md` wire format**, in-memory
implementation:

- `POST /v1/auth/set-password` — bcrypt the password, upsert into the agent's
  map. Upsert so `/v1/setup` can be retried after a brain-side rollback.
- `POST /v1/auth/verify-password` — returns `{valid: bool}`; never reveals
  *why* a verification failed (matches PAM's posture, per the spec).
- `POST /v1/auth/delete-user` — idempotent.

The brain stays spec-faithful: it never holds a password hash. When the real
host-agent lands (PAM via `authenticate()`), only this package's
implementation changes; the brain's call sites don't.

### Hostclient wrappers (`internal/hostclient`)

Typed `VerifyPassword` / `SetPassword` / `DeleteUser` methods + table tests
that round-trip against an `httptest` server speaking the protocol.

### Store — users + sessions (`internal/store`)

Two new tables (migrated idempotently alongside `instances`):

- `users(id PK, username UNIQUE, role, recovery_hash, created_at)`. No
  password column. `RoleAdmin` / `RoleMember` constants.
- `sessions(token PK, user_id FK, created_at, last_seen_at)`. FK cascades on
  user delete — a deleted user can't have orphan sessions.

New CRUD: `CreateFirstAdmin` (atomic empty-table guard via
`INSERT … WHERE NOT EXISTS`, returns `ErrConflict` on collision),
`GetUserByUsername`, `GetUser`, `DeleteUser`, `HasAnyUser`,
`CreateSession`, `GetSession`, `TouchSession`, `DeleteSession`. Layer-2 tests
on real SQLite cover migration idempotency, UNIQUE collisions, the
empty-table race, FK cascade, and `TouchSession` semantics.

### Session manager (`internal/auth`)

New package, owns the session lifecycle and cookie shape (not the HTTP
middleware — that lives in `internal/api`, per the consumer-side-interface
rule). Surface:

- `Manager.Issue(userID) → Session` — 256-bit token, base64url-encoded
  (43 chars). Server-side validated; the value is only entropy.
- `Manager.Validate(token) → Identity, error` — resolves the session, fetches
  the user (defensive: missing user → `ErrInvalidSession` even though the FK
  cascade should have removed the row), bumps `last_seen_at`.
- `Manager.Revoke(token)` — idempotent.
- `Manager.Cookie(token)` / `ClearCookie()` — `HttpOnly`, `SameSite=Lax`,
  `Path=/`, no `Domain` (so the same cookie works for `.local` HTTP today and
  `<box-id>.molma.network` HTTPS later, per `MOLMA_NETWORK.md`). `Secure` is
  opt-in via `SecureCookies` — off in dev where the brain serves plain HTTP
  (the browser would drop a Secure cookie over `http://`).
- `WithIdentity` / `FromContext` — single context key, no token leak to
  handlers.
- A small `SessionStore` interface lives next to the consumer (`auth`), not
  in `store`. Tests use a fake satisfying it.

### API surface + middleware (`internal/api`)

Five new endpoints (huma-registered, so they show up in `openapi.json`):

| Method | Path | Notes |
|---|---|---|
| `GET`  | `/api/v1/auth/state` | Public probe: `{has_users: bool}`. The UI uses this to decide setup vs. login on cold load. |
| `POST` | `/api/v1/setup` | Public; allowed only when zero users exist. Creates first admin, returns the recovery code **once**. |
| `POST` | `/api/v1/login` | Public. Looks up user *and* runs `host.VerifyPassword` regardless of lookup result (constant-time-ish — keeps username-existence out of timing). |
| `POST` | `/api/v1/logout` | Revokes the session bound to the cookie. Idempotent. |
| `GET`  | `/api/v1/me` | Returns the current `UserDTO`. |

`authMiddleware` wraps the mux behind CORS:
- Public allowlist: `/api/v1/setup`, `/api/v1/login`, `/api/v1/auth/state`,
  `/openapi.json|.yaml`, `/docs` (+ subpaths).
- Everything else (catalog, apps, install/uninstall, SSE, jobs, `/me`,
  `/logout`) requires a valid session cookie. Missing/invalid → 401.

`/v1/setup` is a transaction across two systems. The brain commits first
(atomic empty-table guard fences concurrent callers); host-agent
`set-password` runs second. If the host call fails, the brain rolls the user
row back so `/v1/setup` stays callable instead of being wedged
half-bootstrapped. The recovery code is generated *before* the user row is
written so the row carries its bcrypt hash from row creation.

Layer-3 API tests: bootstrap progression (`/auth/state` flips to
`has_users: true`), `TestProtectedRoutesRequireSession` (401 without cookie,
200 with valid cookie, 401 after logout), full login/logout flow including
cookie attachment and `/me`, and the host-failure rollback path.

### Dashboard — auth-aware router (`web-ui/`)

The Vue app now has three views, picked from auth state:

- `Setup.vue` — first-run create-admin form; on success, shows the recovery
  code in a one-shot panel with an **"I've saved it — continue"** button
  before flipping the router to the dashboard. Per `AUTH.md` # Recovery: the
  brain has only the hash, so this is the user's single chance.
- `Login.vue` — username/password.
- `Dashboard.vue` — the existing app catalog + installed apps + custom-app
  form, now with a `Sign out` button in the header that shows the current
  username.

`auth.ts` owns the singleton `currentUser` ref and the bootstrap flow:
`GET /auth/state` → if `has_users` then `GET /me`; route accordingly. The
`api.ts` wrapper exposes `setUnauthenticatedHandler` so any 401 from a later
call drops `currentUser` to `null`, which makes `App.vue` fall back to the
login view without each call site having to handle 401.

`setup()` deliberately does NOT set `currentUser` synchronously — the recovery
code must be shown first. `setupComplete()` (called by the ack button) flips
`currentUser` and `hasUsers`, which routes the user into the dashboard.

## How it maps to the specs

- **`AUTH.md`** — password-only v1; server-side opaque cookies
  (`molma_session`, `HttpOnly`, `SameSite=Lax`, 256-bit token); admin
  recovery code generated once, hash-stored, shown once; the brain holds no
  password hash (PAM via host-agent is the source of truth).
- **`USERS_AND_GROUPS.md`** — `users.role` is the dashboard role; `RoleAdmin`
  / `RoleMember` are the only values. Linux-account mirroring lands when the
  real host-agent does.
- **`BRAIN_HOST_PROTOCOL.md`** — three auth endpoints added to the protocol
  doc with the dash-cased paths used by the real wire (`verify-password`,
  `set-password`, `delete-user`).
- **`BRAIN_UI_PROTOCOL.md`** — five new endpoints follow the existing sync
  (Pattern A) shape; cookies via `Set-Cookie` header on responses.
- **`FIRST_RUN.md`** — `/v1/setup` is the bootstrap-admin step; the full
  first-run wizard remains deferred.
- **`WEB_UI.md`** — auth state is a singleton ref (not a Query) because the
  rest of the UI conditions render on it; `api.ts` continues to be the
  swap-for-`openapi-fetch` seam.

## Known gaps & deviations

- **No role-based authz beyond "must be logged in".** `IsAdmin()` exists on
  `Identity` but no endpoint yet uses it — there's only one role-gated
  decision (setup-vs-not), and it's structurally enforced by the empty-table
  guard. The seam is in place for the first real privileged endpoint.
- **No session expiry / re-auth / 5-min elevation window.** `last_seen_at`
  is bumped on every authenticated request, but nothing yet enforces an
  idle timeout or re-prompts for destructive ops (`AUTH.md` § Sessions).
- **Recovery-code redemption flow is unbuilt.** Codes are generated, shown
  once, and hash-stored — but there's no UI / endpoint to redeem one.
- **No multi-user CRUD UI.** "Add a member" is a follow-up.
- **Real PAM integration deferred.** Host-agent stays fake: bcrypt in a map.
  The protocol shape is real; swapping the impl won't touch the brain.
- **No per-user Tier-3 app instances.** `APP_ISOLATION.md` work is
  downstream of this slice.
- **No audit-log entries yet.** `AUTH.md` calls for `audit_events` rows on
  login/logout/setup; the seam is there (every call site has a `slog` line)
  but the table itself is deferred to the LOGGING slice.
- **Rate-limiting on `/login` deferred.** `host.VerifyPassword` answers
  truthfully; throttling lives in the brain per the spec, but no limiter is
  wired yet.

## What's next

Ordered, roughly by leverage:

1. **Audit events table + writes** (`LOGGING.md`) — landed in 0007.
2. **Multi-user CRUD UI + endpoints** — landed in 0008.
3. **Recovery-code redemption flow** — landed in 0009 (`POST /api/v1/recover`).
4. **Session expiry + 5-min elevation re-prompt** — landed in 0010 (`AUTH.md` # Sessions, `USERS_AND_GROUPS.md` # Elevation). All four Tier-A auth items now done.
5. **Real PAM in host-agent** — swap the bcrypt map for `pam_start`/`pam_authenticate`. Brain doesn't change.
6. **Per-protocol opt-in** (SSH / SMB) as a service allowlist per account, per `USERS_AND_GROUPS.md`.
