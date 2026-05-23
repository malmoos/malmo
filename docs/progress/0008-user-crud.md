# 0008 — User CRUD (admin management + self-service password change)

- **Status:** done
- **Date:** 2026-05-23
- **Specs touched:** `USERS_AND_GROUPS.md`, `AUTH.md`, `LOGGING.md`, `BRAIN_HOST_PROTOCOL.md`

The brain had first-admin bootstrap and login but no way to add, list, update,
or delete users from the dashboard. This slice lands the full admin user-CRUD
surface and the self-service password change endpoint.

## What was done

### Store — `UpdateRole` + `CountAdmins` (`internal/store`)

- `UpdateRole(id, role string) error` — validates role, returns `ErrNotFound`
  on missing row, uses the existing SQLite CHECK constraint as a second guard.
- `CountAdmins() (int, error)` — `SELECT COUNT(*) FROM users WHERE role='admin'`.
  Used by the last-admin guard in PATCH and DELETE handlers.
- L2 tests: `TestUpdateRoleAndCountAdmins` covers promote, demote, unknown-id
  (ErrNotFound), and invalid role.

### Protocol — `SetRoleRequest` (`internal/protocol/host.go`)

New request type for `POST /v1/auth/set-role`: `{user, role}`. Added to the
credential mutation neighbourhood per `BRAIN_HOST_PROTOCOL.md`.

### Host-agent — `set-role` endpoint (`cmd/host-agent/main.go`)

Fake host-agent stores the last role in `roles map[string]string`. Validates
`role ∈ {admin,member}`, returns 400 otherwise. Real impl would call
`gpasswd -a/-d` to flip `malmo-admin` group membership.

### Hostclient — `SetRole` (`internal/hostclient/hostclient.go`)

`SetRole(ctx, user, role string) error`. Mirrors `SetPassword`/`DeleteUser`.
L2 test: `TestSetRole` covers happy path, member round-trip, and invalid role
rejection.

### Audit vocabulary (`internal/audit/audit.go`)

Five new exported consts: `ActionUserCreate`, `ActionUserRoleChange`,
`ActionUserDelete`, `ActionUserPasswordReset`, `ActionUserPasswordChange`.
Vocabulary table in `LOGGING.md` updated to match.

### `requireAdmin` helper (`internal/api/auth.go`)

`requireAdmin(ctx) error` — returns `huma.Error403Forbidden` when the acting
identity is missing or not an admin. Called as the first line of every
admin-only handler. No middleware layer — per-handler check is simpler at this
scale.

### Admin user-CRUD handlers (`internal/api/users.go`)

New file. Five handlers, all behind `requireAdmin`:

- `GET /api/v1/users` — list all users.
- `POST /api/v1/users` — create user (`{username, password, role}`). Defaults
  role to member. Brain commits first → `host.SetPassword` → rollback on host
  failure (same pattern as `/setup`). 409 on duplicate username.
- `PATCH /api/v1/users/:id` — change role. Guards: last-admin (409 when
  CountAdmins==1 and demoting), no self-demote (409 when actor==target and
  new role != admin), ErrNotFound → 404. Brain commits first; on host-agent
  failure the store row is restored to the previous role so the two sides
  stay aligned per USERS_AND_GROUPS.md ("if either side fails, both roll
  back").
- `DELETE /api/v1/users/:id` — delete user. Guards: last-admin, no
  self-delete (self-delete check fires before the last-admin guard on
  purpose: even a multi-admin household routes "remove my own admin
  account" through another admin). Brain commits first (FK cascades
  sessions; FK SET NULLs audit rows); on host failure the row is recreated.
  Cascaded sessions don't come back — acceptable for a rare error path.
- `POST /api/v1/users/:id/password` — admin password reset. `host.SetPassword`
  only; no store change.

All five handlers audit on failure as well as success — elevation-class
operations (create / role.change / delete / password.reset / password.change)
emit `success=false` rows at every observable failure point (host 502, store
500, conflict 409, guard rejections like last-admin and self-delete). Mirrors
the `login.failure` pattern so the Activity view can answer "did someone
unauthorized try to mutate accounts?" symmetrically with login attempts.

### Self-service password change (`internal/api/users.go`)

- `POST /api/v1/me/password` — any authenticated user. Verifies `current_password`
  via `host.VerifyPassword`; returns 401 and audits `user.password.change`
  success=false on wrong password; calls `host.SetPassword` and audits
  success=true on valid.

### Registration

`registerUsers` and `registerMeRoutes` called from `Server.Handler()`.
CORS `Allow-Methods` extended to include PATCH.

### Tests (`internal/api/users_test.go`)

L3 tests (real store + real in-process host-agent over unix socket):

- `TestListUsersAdminOnly`, `TestListUsersRequiresAuth`
- `TestCreateUserHappyPath`, `TestCreateUserDefaultsToMember`,
  `TestCreateUserDuplicateUsername409`, `TestCreateUserInvalidRole422`,
  `TestCreateUserMemberForbidden`, `TestCreateUserRollsBackOnHostFailure`
- `TestUpdateRoleHappyPath`, `TestUpdateRoleLastAdminGuard`,
  `TestUpdateRoleNoSelfDemote`, `TestUpdateRoleMemberForbidden`,
  `TestUpdateRoleNotFound`
- `TestDeleteUserHappyPath`, `TestDeleteUserLastAdminGuard`,
  `TestDeleteUserNoSelfDelete`, `TestDeleteUserMemberForbidden`,
  `TestDeleteUserNotFound`
- `TestResetUserPasswordHappyPath`, `TestResetUserPasswordMemberForbidden`
- `TestChangeMyPasswordHappyPath`, `TestChangeMyPasswordWrongCurrent401`,
  `TestChangeMyPasswordRequiresAuth`, `TestChangeMyPasswordAuditsFailure`
- `TestCreateUserAuditEvent`, `TestDeleteUserAuditEvent`

## How it maps to the specs

- `USERS_AND_GROUPS.md` — role table (admin/member), UI-is-the-path stance,
  last-admin guard, no self-demote/self-delete.
- `AUTH.md` — password lifecycle (PAM is source of truth; brain never holds
  hashes), admin-set password reset, self-service change with current-password
  verify.
- `BRAIN_HOST_PROTOCOL.md` — new `POST /v1/auth/set-role` in credential
  mutation section. Pattern A (sync).
- `LOGGING.md` — five new `user.*` action consts; vocabulary table updated.

## Known gaps & deviations

- **Forced password change on first login** — `must_change_password` column
  and login-flow routing are deferred (`NEXT.md`).
- **Second-admin recovery code** — promoting a user to admin does NOT generate
  a recovery code. Deferred (`NEXT.md`).
- **Session revocation on self-service password change** — other-device
  sessions stay valid until they age out. Known acceptable per `AUTH.md`;
  mentioned here for visibility.
- **Host-side gpasswd flip** — `set-role` in the fake host-agent is in-memory
  only; real group membership change (`gpasswd`) lands with the real
  host-agent.
- **Demotion doesn't kill live sudo capability** — per `USERS_AND_GROUPS.md`
  # Known gaps; the kernel's cached credentials outlive the group change by up
  to the PAM session lifetime. Acceptable for v1.

## What's next

- Forced password change flag + login-flow routing.
- Recovery code generation on admin promotion.
- Session revocation on password change (requires `DeleteSessionsForUser`
  call after `SetPassword`).
- Real gpasswd in host-agent (lands with host-agent real impl).

---

**Update 2026-05-24:** Recovery-code redemption endpoint (`POST /api/v1/recover`)
landed in 0009. The `DeleteSessionsForUser` call on password change (noted above)
is still deferred.
