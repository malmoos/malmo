# 0015 — Real set-password in host-agent-real (useradd + chpasswd)

- **Status:** done
- **Date:** 2026-05-24
- **Specs touched:** `BRAIN_HOST_PROTOCOL.md`, `AUTH.md`, `USERS_AND_GROUPS.md`, `FIRST_RUN.md`

Closes Tier-B point 1 of the auth slice (`0011` "What's next"). `POST
/v1/auth/set-password` in `host-agent-real` now writes to `/etc/shadow` via
`useradd` + `chpasswd`, so the brain's bootstrap path (`POST /setup →
SetPassword`) creates a real Linux user — and the PAM verifier from `0011` can
finally succeed against the password that was just set. End-to-end dashboard
login on `host-agent-real` works for the first time.

`setRole` and `deleteUser` remain in-memory fakes — they're independent
follow-ups (next slices).

## What was done

### Shared agent layer — consumer-side seam

`internal/hostagent/agent.go` gains a new consumer-side interface alongside
`PasswordVerifier` and `Publisher`:

```go
type UserManager interface {
    UpsertPassword(user, password string) error
}
```

`Agent` gets a `UserMgr UserManager` field. The `setPassword` handler branches:

- **`UserMgr != nil`** (the real binary) — delegate to `UpsertPassword` and
  return `200 OK {}` on success, `500 set-password-failed` on system error. The
  underlying error is captured in a structured `slog.Error("set-password:
  user-manager error", ...)`; the HTTP body says only `"set-password failed"`,
  same posture as `verify-password`.
- **`UserMgr == nil`** (the fake binary, and existing tests) — keep the
  bcrypt-into-`a.passwords` behavior so `FakeVerifier` and the bootstrap-flow
  tests still pass.

This keeps `cmd/host-agent` (fake) untouched and contains the real `/etc/shadow`
write to a single new package.

### Provider — `internal/hostagent/usermgr`

New package with `LinuxUserManager`:

```go
type LinuxUserManager struct {
    Shell        string // default "/bin/bash"
    PrimaryGroup string // default "malmo"
}

func (m *LinuxUserManager) UpsertPassword(slug, password string) error
```

Behavior:

1. `user.Lookup(slug)`. Only `user.UnknownUserError` is treated as "missing —
   create"; any other lookup failure (nss config broken, etc.) returns loud.
2. If missing: `useradd --create-home --shell <Shell> --gid <PrimaryGroup>
   <slug>`. UID assignment is delegated to `/etc/login.defs` (`UID_MIN ≥ 3000`
   per `FIRST_RUN.md` # Identity & display names — set at box-build time, not
   forced via `-u`).
3. `chpasswd` with stdin `"<slug>:<password>\n"`. `chpasswd` goes through PAM,
   which triggers Samba sync when the host is configured with `unix password
   sync = yes` + `pam password change = yes` (`AUTH.md` # Samba password
   backend).

Stderr from `useradd`/`chpasswd` is wrapped into the returned error for the
structured log; never reaches the wire.

### Wiring — `cmd/host-agent-real/main.go`

```go
a := hostagent.New(&pamverifier.PAMVerifier{Service: "malmo"}, pub)
a.UserMgr = &usermgr.LinuxUserManager{}
```

The startup `slog.Warn` was tightened: previously listed all three of
`set-password / set-role / delete-user` as "NOT wired to the system"; now lists
only `set-role / delete-user`.

### Tests

- `internal/hostagent/agent_test.go` — two new tests:
  - `TestSetPassword_DelegatesToUserMgrWhenSet` — asserts the handler calls
    `UpsertPassword` with the right args and does **not** populate the
    in-memory bcrypt map when `UserMgr` is wired.
  - `TestSetPassword_UserMgrError_Returns500` — asserts a 500 response and
    that the body never leaks system detail (no `"useradd"` / no group name).
- `internal/hostagent/usermgr/linux_test.go` — unit tests for
  `useraddArgs` (default + override) and the empty-slug guard. No system calls.
- `internal/hostagent/usermgr/integration_test.go` — `//go:build usermgrtest`,
  exercises real `useradd` + `chpasswd` against `/etc/passwd` and
  `/etc/shadow`. Creates `malmo-usermgrtest`, asserts the shadow hash is
  non-empty, then rotates the password (update path). Cleans up with `userdel
  -r`. Skips when not root. Intended for the nspawn lane.

### Makefile

New target `test-usermgr` that runs the tagged integration test under `sudo`.
Documented as "nspawn lane only — do NOT run on a developer laptop." `.PHONY`
updated.

## How it maps to the specs

- `BRAIN_HOST_PROTOCOL.md` # Credential mutation endpoints — `set-password` is
  upsert (creates if missing, updates otherwise) in a single round-trip. Wire
  shape unchanged; only the back-end becomes real.
- `AUTH.md` # Identity primitive / # Password change — `/etc/shadow` is the
  source of truth; brain holds no hash; `chpasswd` (PAM path) is the
  Samba-sync-friendly mutator.
- `USERS_AND_GROUPS.md` — accounts are real Linux users with primary group
  `malmo`; promotion into `sudo` is a separate operation (still fake, will land
  with the real `setRole`).
- `FIRST_RUN.md` # Identity & display names — UID range ≥ 3000 honored via
  `/etc/login.defs` (host responsibility), not forced by host-agent.
- CLAUDE.md "consumer-side interfaces" — `UserManager` lives in
  `internal/hostagent`, not in `internal/hostagent/usermgr`.
- CLAUDE.md "no premature abstraction" — single concrete implementation;
  interface justified by the consumer-side rule + the same nil-pointer seam
  used by `Publisher`/`Verifier`.

## Known gaps & deviations (loud)

- **No use-case folder creation.** `useradd --create-home` makes `/home/<slug>/`
  but does NOT populate `Photos/`, `Documents/`, `Movies/`, `Music/`, `Notes/`,
  `Downloads/` (per `STORAGE.md` # What apps and users actually see). That's a
  separate slice — likely a sibling host-agent endpoint or an extension of this
  one once the storage layer lands.
- **`malmo` group must pre-exist.** `useradd --gid malmo` fails if the group is
  absent. The box build is expected to create it; host-agent does not. The
  integration test falls back to `nogroup` / `users` on a stock Debian nspawn
  so the test still runs before the build pipeline provisions the group.
- **Samba sync is unverified.** `chpasswd` goes through PAM, which is the right
  hook for Samba's `pam password change` integration, but Samba isn't installed
  here yet so we don't actually exercise the sync. Dashboard login works
  regardless (PAM-only path).
- **No password complexity / length enforcement at host-agent.** The brain owns
  input validation (`AUTH.md`); host-agent trusts what it receives.
- **`setRole` and `deleteUser` still fake** even in `host-agent-real`. The
  startup warning lists them; tests assert the existing fake behavior.
- **No `chpasswd -e` / hash-mode handling.** We feed cleartext on stdin and let
  PAM hash it according to system policy (yescrypt or argon2 per `AUTH.md`).

## What's next

- Real `setRole` in `host-agent-real` (`gpasswd -a/-d <slug> sudo`, per
  `USERS_AND_GROUPS.md:30`).
- Real `deleteUser` in `host-agent-real` (`userdel -r`).
- Use-case folder creation (`Photos/`, `Documents/`, ...) as a follow-up
  host-agent op; owner TBD between this package and a future `storage` package.
- nspawn lane wiring for `test-usermgr` (provision `malmo` group, run under
  systemd-nspawn instead of host root).
