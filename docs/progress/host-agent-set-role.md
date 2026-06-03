# 0016 — Real set-role in host-agent-real (gpasswd) + bootstrap wiring

- **Status:** done
- **Date:** 2026-05-24
- **Specs touched:** `BRAIN_HOST_PROTOCOL.md`, `USERS_AND_GROUPS.md`, `AUTH.md`, `FIRST_RUN.md`

Closes Tier-B point 2 of the auth slice (`0011` "What's next"). `POST
/v1/auth/set-role` in `host-agent-real` now flips Linux group membership via
`gpasswd -a/-d <user> sudo`, so dashboard role changes affect the real
shell-rescue capability (`USERS_AND_GROUPS.md` # Why admins get sudo). The
brain's `/setup` and `createUser` paths were also extended to call SetRole
after SetPassword — without that, a freshly-bootstrapped first admin would
have a real Linux account but no `sudo` membership, contradicting
`USERS_AND_GROUPS.md:32`.

Only `deleteUser` remains in-memory after this slice.

## What was done

### Spec hygiene — `sudo` vs `molma-admin`

`BRAIN_HOST_PROTOCOL.md:115` said `set-role` updates membership in
**`molma-admin`**. Every other doc — `USERS_AND_GROUPS.md`, `FIRST_RUN.md`,
`CLAUDE.md` Load-bearing decisions, `DECISIONS.md` 2026-05-15 — locks the
group as **`sudo`**. Protocol doc was wrong; corrected in this change to point
at `USERS_AND_GROUPS.md` # Roles as the owning policy. Per CLAUDE.md
Documentation discipline ("Keep specs and reality in sync").

### Shared agent layer — interface extension

`UserManager` in `internal/hostagent/agent.go` grew a method:

```go
type UserManager interface {
    UpsertPassword(user, password string) error
    SetRole(user, role string) error
}
```

The `setRole` handler branches:

- **`UserMgr != nil`** — delegate; opaque `500 set-role-failed` on error, with
  the underlying error captured in `slog.Error` (same posture as
  `verify-password` / `set-password`).
- **`UserMgr == nil`** — keep the in-memory `roles` map (fake binary, existing
  tests).

### Provider — `LinuxUserManager.SetRole`

`internal/hostagent/usermgr/linux.go`:

```go
func (m *LinuxUserManager) SetRole(slug, role string) error {
    switch role {
    case "admin":
        return gpasswd("-a", slug, m.adminGroup())  // idempotent on re-add
    case "member":
        if !isInGroup(slug, m.adminGroup()) { return nil }
        return gpasswd("-d", slug, m.adminGroup())
    }
    ...
}
```

New `AdminGroup` field (default `"sudo"`); new `adminGroup()` accessor; new
`isInGroup` that reads `/etc/group` directly via a pure `parseGroupMembership`
function. Direct parse rather than `os/user` lookups so the answer is the
authoritative file state, independent of nsswitch / nscd / sssd caches.

The pre-check on the demote path is what makes `SetRole("member")` idempotent:
`gpasswd -d` on a non-member exits non-zero, which would otherwise surface as
spurious 500s.

### Brain — `/setup` and `createUser` now call SetRole

Pre-flight check during the plan surfaced that the bootstrap paths never
called SetRole; they only flipped the role in SQLite. With the fake this was
invisible. With real `gpasswd` it means the first admin would be created with
the `molma` primary group but never added to `sudo` — breaking the
shell-rescue path that's the entire reason admins get sudo in the first place
(`USERS_AND_GROUPS.md` # Why admins get sudo).

`internal/api/auth.go` `/setup`: after `SetPassword`, call
`SetRole(username, admin)`. On failure, roll back the brain row so the
bootstrap stays retryable — same orphan-Linux-user posture as the existing
SetPassword-failure path (a gap from `0015`, not a new one).

`internal/api/users.go` `createUser`: same — after `SetPassword`, call
`SetRole(username, role)` for **both roles**. Calling it for `member` keeps
the brain-host contract uniform ("after every user mutation the host knows
the canonical role"); the provider's member path is a no-op when the user
isn't already in `sudo`, so the extra round-trip is cheap and the invariant
is easier to reason about.

Audit emission in `/setup` was a pre-existing gap surfaced by review of this
slice: `setup` had no failure audit at all (only the success `setup.complete`
on line 245), unlike `createUser` which audits every failure branch. Added a
new `audit.ActionSetupFailure = "setup.failure"` (mirroring the
`login.success` / `login.failure` and `recover.success` / `recover.failure`
pairs) and emit it on each observable failure path: `CreateFirstAdmin` error
(500 or 409), SetPassword 502, SetRole 502, and `auth.Issue` 500. `createUser`
already had complete audit coverage; no changes needed there.

### Tests

- `internal/hostagent/agent_test.go` — `TestSetRole_DelegatesToUserMgrWhenSet`
  (asserts the in-memory map is **not** written when `UserMgr` is wired) and
  `TestSetRole_UserMgrError_Returns500` (asserts the response body never leaks
  `"gpasswd"` or the group name).
- `internal/hostagent/usermgr/linux_test.go` — `TestSetRole_EmptySlug`,
  `TestSetRole_BadRole`, `TestParseGroupMembership` (table-driven happy + edge
  cases), `TestParseGroupMembership_EdgeCases` (whitespace, empty member
  field).
- `internal/hostagent/usermgr/integration_test.go` (`//go:build usermgrtest`)
  — extends with `TestSetRole_PromoteDemoteIdempotent`: creates a real user,
  asserts not in `sudo` by default, promotes and re-promotes (idempotent),
  demotes and re-demotes (idempotent), asserts membership transitions via
  `/etc/group`. Skips when `sudo` group is absent.
- `internal/api/auth_test.go` — `TestSetupRollsBackOnSetRoleFailure`: spins
  up a host-agent stand-in that succeeds set-password and 500s set-role;
  asserts `/setup` returns 502, `HasAnyUser()` is false, and a
  `setup.failure` audit event was recorded. The pre-existing
  `TestSetupRollsBackOnHostFailure` was extended with the same audit
  assertion to lock the SetPassword-failure branch too.
- `internal/api/users_test.go` — `TestCreateUserRollsBackOnSetRoleFailure`:
  uses the existing `newHarnessWithBrokenSetRole`; asserts a `POST /users`
  with a working set-password but broken set-role returns 502 and the brain
  row was rolled back.

### Wiring — `cmd/host-agent-real/main.go`

`LinuxUserManager` was already wired in `0015`; no new line needed (same
struct now also satisfies `SetRole`). The startup warning was tightened from
"set-role/delete-user are NOT wired" to "delete-user is NOT wired" and the
package-doc comment updated.

## How it maps to the specs

- `BRAIN_HOST_PROTOCOL.md` # Credential mutation endpoints — `set-role`
  contract unchanged on the wire; back-end becomes real; `molma-admin` →
  `sudo` correction landed in the same change.
- `USERS_AND_GROUPS.md` # Roles — admin = in `sudo`, member = not. First
  admin added to `sudo` at account creation (now actually enforced).
- `USERS_AND_GROUPS.md` # Known sharp edges — demotion doesn't kill live SSH
  sessions; documented and accepted, no action.
- `FIRST_RUN.md:71` — first admin → `sudo` is now real, not just an SQLite
  flag.
- `AUTH.md` # Roles — group flip via host-agent is now end-to-end.
- CLAUDE.md "Brain commits first, host is reconstructible" — both `/setup`
  and `createUser` follow the brain-then-host pattern with rollback.
- CLAUDE.md "consumer-side interfaces" — `UserManager` extension stays in
  `internal/hostagent`, not in the provider package.

## Known gaps & deviations (loud)

- **`deleteUser` is still fake in `host-agent-real`.** Last remaining Tier-B
  item from `0011`.
- **`sudo` group must pre-exist on the host.** True on standard Debian; not
  defended in code. Integration test skips when absent rather than failing.
- **Demotion doesn't revoke live SSH sessions** for the demoted admin (Linux
  caches group membership at session start). Already accepted in
  `USERS_AND_GROUPS.md:91`.
- **Orphan Linux user on SetRole failure.** If `useradd` + `chpasswd` succeed
  during `/setup` or `createUser` but then `gpasswd` fails, the brain row is
  rolled back but the Linux user stays. Same shape as the
  `0015` orphan-on-chpasswd-failure gap; closes when real `deleteUser` lands
  and the rollback paths can call it.
- **Stale group caches.** No `nscd` on a stock molma box, so reads from
  `/etc/group` after a `gpasswd` reflect immediately. If `nscd` / `sssd` ever
  enters the picture, the read-after-write in the integration test could flake.
- **TOCTOU window in the demote pre-check.** `SetRole("member")` reads
  `/etc/group`, decides whether the user is in `sudo`, then shells out to
  `gpasswd -d`. A concurrent demote between read and shell-out would cause
  `gpasswd -d` to fail on a non-member and surface as a spurious 500. Not
  defended in code: the brain serializes user mutations at the API layer,
  and `host-agent` is the only path to `gpasswd` on the box. Revisit if
  either of those invariants changes.

## What's next

- Real `deleteUser` in `host-agent-real` (`userdel -r`; cleanup-on-failure
  hooks in `/setup` and `createUser` rollback paths).
- nspawn-lane wiring for `test-usermgr` (provision `molma` group + ensure
  `sudo` group present, run under systemd-nspawn instead of host root).
- Use-case folder creation (`Photos/`, `Documents/`, ...) at user-create time
  (`STORAGE.md` territory).
