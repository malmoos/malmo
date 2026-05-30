# 0017 — Real delete-user in host-agent-real (userdel -r -f) + close orphan-on-rollback gap

- **Status:** done
- **Date:** 2026-05-24
- **Specs touched:** `BRAIN_HOST_PROTOCOL.md`

Closes the last Tier-B follow-up from `0011`. `POST /v1/auth/delete-user` in
`host-agent-real` now shells out to `userdel -r -f <slug>`, so dashboard
"delete user" actually removes the Linux account, its `/etc/shadow` entry,
its `sudo` membership, and its home dir. Before this slice the brain row
disappeared but PAM kept authenticating the user — the system disagreed with
the dashboard.

Also closes the orphan-on-rollback gap noted in the "Known gaps" of `0015`
and `0016`: `/setup` and `createUser` now call `host.DeleteUser` best-effort
when a downstream SetPassword or SetRole fails, so a failed bootstrap no
longer leaves a real Linux account with no brain row to clean it up from.

## What was done

### Shared agent layer — interface extension

`UserManager` in `internal/hostagent/agent.go` grew a method:

```go
type UserManager interface {
    UpsertPassword(user, password string) error
    SetRole(user, role string) error
    DeleteUser(user string) error
}
```

The `deleteUser` handler branches:

- **`UserMgr != nil`** — delegate; opaque `500 delete-user-failed` on error,
  underlying error captured in `slog.Error` (same posture as `set-password` /
  `set-role`).
- **`UserMgr == nil`** — keep the in-memory map deletion (fake binary,
  existing tests).

### Provider — `LinuxUserManager.DeleteUser`

`internal/hostagent/usermgr/linux.go`:

```go
func (m *LinuxUserManager) DeleteUser(slug string) error {
    if slug == "" { return fmt.Errorf("usermgr: empty slug") }
    if _, err := user.Lookup(slug); err != nil {
        var unknown user.UnknownUserError
        if errors.As(err, &unknown) { return nil }   // idempotent
        return fmt.Errorf("usermgr: lookup %q: %w", slug, err)
    }
    if err := runCmd("userdel", "-r", "-f", slug); err != nil { ... }
    return nil
}
```

`-r` removes the home directory + mail spool. `-f` forces removal even if the
user owns running processes; without it `userdel` exits 8 on a logged-in
user, turning routine deletes into 500s.

The `user.Lookup` pre-check is what makes a second delete a no-op — same
pattern as `UpsertPassword`. The wire-level idempotency contract
(`BRAIN_HOST_PROTOCOL.md`: unknown user returns 200) lives at the
brain-handler boundary; the provider returns nil and the handler maps that
to 200.

### Session-termination matrix (what real-deleting a user actually kills)

`userdel -r -f` removes the account file state. It does **not** terminate
in-flight sessions on any channel. The brain-side `s.store.DeleteUser`
cascades dashboard sessions via the SQLite `ON DELETE CASCADE` already in
`internal/store/store.go:119`.

| Channel | What kills it | When it dies |
|---|---|---|
| Dashboard / web | `store.DeleteUser` cascades the `sessions` row | Next request → 401 (cookie token is gone). |
| SSH | Nothing in this slice. `-f` only suppresses the "user busy" error; the running shell keeps its already-resolved UID. | When the user disconnects / the shell exits. |
| SMB | Nothing in this slice. Samba auth is per-connection via PAM. | New mounts fail; existing mounts persist until idle teardown (minutes). |
| `sudo` (already-elevated shell) | Nothing. | Until the shell exits. |

"Existing sessions ride out until disconnect" is the same posture
`USERS_AND_GROUPS.md` # Known sharp edges already accepts for role demotion
("group membership is cached at session start"). Documented here so the next
contributor doesn't expect deleting a user to also nuke their live SSH
window.

### Brain — orphan-on-rollback gap closed

Both bootstrap paths now call `s.host.DeleteUser` best-effort before the
existing store rollback:

- `internal/api/auth.go` `/setup`: on `SetPassword` failure AND on `SetRole`
  failure. The `SetRole` failure is the load-bearing case — by that point
  `UpsertPassword` has created the Linux account via `useradd` + `chpasswd`,
  so a bare store rollback would leave PAM able to authenticate a user the
  dashboard no longer knows about. The `SetPassword` failure branch covers
  the smaller sliver where `useradd` succeeded but `chpasswd` failed.
- `internal/api/users.go` `createUser`: same two branches, same shape.

The cleanup is best-effort (log on failure, don't shadow the original
error); idempotency in the host handler means the call is safe even if no
Linux account was ever created.

The existing `deleteUser` brain handler at `users.go:258-268` already
follows brain-commit-then-host with rollback (`s.store.CreateUser(target)`
on host failure) and audits failure on every observable path. Verified
unchanged.

### Tests

- `internal/hostagent/agent_test.go` —
  `TestDeleteUser_DelegatesToUserMgrWhenSet` (in-memory map is not touched
  when `UserMgr` is wired) and `TestDeleteUser_UserMgrError_Returns500`
  (response body never leaks `userdel` or PID detail).
- `internal/hostagent/usermgr/linux_test.go` — `TestDeleteUser_EmptySlug`.
- `internal/hostagent/usermgr/integration_test.go` (`//go:build usermgrtest`)
  — `TestDeleteUser_CreateDeleteIdempotent`: real `useradd` → real `userdel
  -r -f` → assert `user.Lookup` returns `UnknownUserError` → second delete
  returns nil.
- `internal/api/auth_test.go` — `TestSetupRollsBackOnHostFailure` and
  `TestSetupRollsBackOnSetRoleFailure` extended to record every
  `delete-user` call on the host-mock and assert exactly one call for the
  setup username. Catches a "rollback runs but skips host" regression.
- `internal/api/users_test.go` — `TestCreateUserRollsBackOnHostFailure` and
  `TestCreateUserRollsBackOnSetRoleFailure` extended with the same
  assertion via a new `deleteCalls *[]string` field on the test harness;
  both broken-host harnesses now record calls.

### Wiring — `cmd/host-agent-real/main.go`

`LinuxUserManager` was already wired in `0015`; the same struct now
satisfies the extended interface. The startup warning ("delete-user is NOT
wired") was removed and the package-doc comment updated to reflect that all
auth ops now hit the real system.

## How it maps to the specs

- `BRAIN_HOST_PROTOCOL.md` # Auth endpoints — `delete-user` contract
  unchanged on the wire; back-end becomes real. Inline note added next to
  the existing `set-password` / `set-role` real-impl notes.
- CLAUDE.md "Brain commits first, host is reconstructible" — `/setup` and
  `createUser` rollback paths now call host cleanup before restoring the
  brain row, keeping the two sides aligned through every observable failure.
- CLAUDE.md elevation-class audit rule — `deleteUser` brain handler
  re-verified to audit failure on all 5 branches (self-delete, count-admins
  failure, last-admin guard, store-fail, host-fail) and success. No new
  failure-emission paths added by this slice; the existing audit envelope
  covers them.

## Known gaps & deviations (loud)

- **`userdel -r` deletes `/home/<slug>/`** — which (per `STORAGE.md`) holds
  the user's first-class content (`Photos/`, `Documents/`, `Music/`, …).
  Today the dashboard delete-user button is the user's only "remove account"
  affordance and we take this as deliberate: a deleted account loses its
  data with it (v1 product call). The "transfer to another user / preserve
  files" flow is a separate slice — when added, that path branches BEFORE
  calling `host.DeleteUser`.
- **Live SSH / SMB sessions are not terminated.** See the matrix above. New
  auth is blocked immediately; existing sessions ride out their natural
  lifetime. Documented and accepted, matching the demotion posture in
  `USERS_AND_GROUPS.md` # Known sharp edges.
- **Best-effort host cleanup can silently fail.** If `s.host.DeleteUser`
  errors during a `/setup` or `createUser` rollback, we log and continue —
  the user still gets a 502 for the original failure, but a Linux account
  may persist. This is a strict improvement over the previous behavior
  (always-orphan) but not a guarantee. The brain returns the original
  error so the admin retries; a retried `createUser` for the same username
  hits the upsert path on the host and converges. No mechanism for an
  out-of-band sweep — relies on retry-converges.
- **TOCTOU window in the `DeleteUser` lookup pre-check.** `user.Lookup` →
  `userdel -r -f` is not atomic. If something external to host-agent removed
  the account between the two calls, `userdel` exits 6 ("user does not
  exist") and the handler surfaces it as a 500 instead of the
  idempotent-nil it promises. Safe today: host-agent is the only writer to
  `/etc/passwd` on a malmo box (CLAUDE.md "UI is the path"); revisit if
  that invariant ever changes. Same shape as the demote pre-check TOCTOU
  noted in `0016`.
- **Silent store-rollback failures fixed in `/setup`** (the `_ = s.store.DeleteUser(u.ID)`
  pattern in `auth.go` predated this slice — same form `createUser` already
  did correctly). Tightened in-scope because the host cleanup added here
  makes the silent-failure consequence worse: a successful host delete
  followed by a silent store-delete failure would permanently wedge the box
  at the `CreateFirstAdmin` 409. Now logged via `slog.Error` on both
  branches; matches `createUser`.
- **No test for an in-flight session at delete time.** The integration test
  asserts file-state convergence but doesn't simulate a logged-in user with
  `-f` in play. The matrix above is the contract; nspawn-lane coverage of
  the live-session branch is a follow-up.

## What's next

- "Transfer files to another user" flow before delete (the option-4 path
  from the planning discussion). UI affordance + brain endpoint that
  reassigns ownership of `/home/<slug>/` to a chosen admin before calling
  `host.DeleteUser`.
- nspawn-lane wiring for `test-usermgr` (also called out by `0016`): run
  under systemd-nspawn with the `malmo` group pre-provisioned, instead of
  host root.
- Out-of-band reconciliation sweep (a brain → host "do any of these
  usernames have orphan Linux accounts?" probe) — only worth building if
  retry-converges turns out to leave residue in practice. Watch and decide.
