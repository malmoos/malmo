# 0011 — Real PAM-based password verification in host-agent-real

- **Status:** done
- **Date:** 2026-05-24
- **Specs touched:** `AUTH.md` (# Brain ↔ host-agent in the auth path), `TESTING.md` (# Fast lane)

Closes Tier-B point 5 of the auth slice (0010 "What's next"). Narrow slice: only `verifyPassword` becomes real. The other three auth ops remain fake and are loudly flagged.

## What was done

### Shared package — `internal/hostagent/`

**`agent.go`** — new package. Extracts the `Agent` struct and all HTTP handlers from the former monolithic `cmd/host-agent/main.go`. Defines the consumer-side `PasswordVerifier` interface:

```go
type PasswordVerifier interface {
    Verify(user, password string) (bool, error)
}
```

`Agent` holds `Verifier PasswordVerifier` plus the existing in-memory maps (`passwords`, `roles`, `published`). The `verifyPassword` handler delegates to `a.Verifier.Verify(...)`; on error it logs and returns `{valid: false}` — never 5xx — preserving the BRAIN_HOST_PROTOCOL.md invariant "never reveal why verification failed."

`New(v PasswordVerifier) *Agent` and `Mount(mux *http.ServeMux)` are the exported constructor and wiring point. HTTP helpers (`decode`, `writeJSON`, `writeErr`, `LogRequests`) moved here too.

**`fake.go`** — `FakeVerifier` backed by the Agent's in-memory bcrypt map via a pointer-back (`a *Agent`). Design choice: pointer-back is simpler than duplicating the map on the verifier or widening the interface with `SetPassword`/`DeleteUser` methods — fewer crossing wires, consistent with the "no premature abstraction" rule.

**`agent_test.go`** — HTTP-layer tests using `httptest.NewRecorder`. Covers:
- verify delegates to verifier (happy path, wrong credentials)
- verifier transport error → `{valid: false}` not 5xx
- setPassword + FakeVerifier round-trip
- setPassword missing fields → 400
- setRole happy path and invalid role
- deleteUser removes from fake map and is idempotent
- system/status and discovery publish/unpublish

### `internal/hostagent/pamverifier/` — isolated PAM package

`PAMVerifier` lives in its own sub-package so that `internal/hostagent` has zero PAM dependency and the fake binary (`cmd/host-agent`) stays pure Go with no CGO or libpam requirement.

**`pam_linux.go`** — build tag `linux && cgo`. `PAMVerifier` struct + `Verify` method. Calls `github.com/msteinert/pam/v2` (MIT, well-maintained). Conversation handler returns the password for `PromptEchoOff`, username for `PromptEchoOn`, empty for info messages. Returns `(true, nil)` on success, `(false, nil)` on `pam.ErrAuthentication` (wrong credentials), `(false, err)` on config/transport failures. Satisfies `hostagent.PasswordVerifier` structurally (no import of hostagent needed).

**`pam_other.go`** — build tag `!(linux && cgo)`. Stub `PAMVerifier` that errors with "requires linux + cgo". Keeps the package importable cross-platform.

**`pam_linux_test.go`** — build tag `linux && cgo && pamtest`. Single skeleton test, skipped in normal runs. Placeholder for the nspawn lane.

### `cmd/host-agent/main.go` — refactored

Slimmed from 228 lines to ~45. Socket setup preserved; handler logic replaced with:
```go
a := hostagent.New(nil)
a.Verifier = hostagent.NewFakeVerifier(a)
mux := http.NewServeMux()
a.Mount(mux)
```

### `cmd/host-agent-real/main.go` — new binary

Same socket setup. Imports `internal/hostagent/pamverifier` (the only binary that does so) and plugs it into the Verifier slot:
```go
a := hostagent.New(&pamverifier.PAMVerifier{Service: "molma"})
```
The `passwords` map remains on the Agent and is still used by `setPassword`/`deleteUser` (fake). Logs a startup `slog.Warn` naming the three unimplemented ops and pointing to this doc.

### `dev/pam/molma` — PAM service file

`auth required pam_unix.so` + `account required pam_unix.so`. Installed to `/etc/pam.d/molma` by the host installer. Used by `PAMVerifier{Service: "molma"}`.

## The (b) vs (c) decision: separate binary

Two options were on the table:
- **(b)** A build tag that swaps the verifier inside a single binary.
- **(c)** Two binaries, shared package.

**Separate binary chosen.** With a single binary, a developer running `make dev` could accidentally build with the PAM tag active and silently test against real PAM. Two distinct entry points (`cmd/host-agent` vs `cmd/host-agent-real`) make the dev/prod boundary explicit and loud — you have to name the binary you want. The shared `internal/hostagent` package means no code duplication.

## Package layout and build isolation

`internal/hostagent` — shared handler layer; no CGO or PAM dependency; importable by any binary with `go build`.

`internal/hostagent/pamverifier` — isolated CGO package; requires `libpam0g-dev` at build time; imported only by `cmd/host-agent-real`. The fake binary (`cmd/host-agent`) never imports this package and builds cleanly with `CGO_ENABLED=1` without libpam headers installed.

Verification:
```bash
# Fake binary and shared package — must succeed without libpam0g-dev:
CGO_ENABLED=1 go build ./cmd/host-agent ./internal/hostagent
CGO_ENABLED=1 go test ./internal/hostagent

# Real binary — expected to fail without libpam0g-dev (correct behavior):
CGO_ENABLED=1 go build ./cmd/host-agent-real
# → fatal error: security/pam_appl.h: No such file or directory

# Full suite (CGO=0 skips pam_linux.go, all packages pass):
CGO_ENABLED=0 go test ./...
```

## Build requirements for host-agent-real

```bash
apt install libpam0g-dev   # PAM headers for cgo
sudo cp dev/pam/molma /etc/pam.d/molma
go build ./cmd/host-agent-real
sudo ./cmd/host-agent-real  # must run as root; pam_unix.so requires privilege
```

## How it maps to the specs

- `AUTH.md` # Brain ↔ host-agent in the auth path — "brain → host-agent `verify_password(user, password)` → PAM `authenticate()` → yes/no" is now real in `host-agent-real`.
- `AUTH.md` # Brain ↔ host-agent — PAM service name `molma`, stack at `/etc/pam.d/molma`.
- `BRAIN_HOST_PROTOCOL.md` — wire shape unchanged; only the verification back-end swaps.
- CLAUDE.md "consumer-side interfaces" — `PasswordVerifier` lives in `internal/hostagent`, not in any provider package.
- CLAUDE.md "`internal/` for everything except `cmd/`" — all handler logic in `internal/hostagent`.

## Known gaps (loud)

- **`setPassword` is fake even in `host-agent-real`.** It writes a bcrypt hash to an in-memory map, not to `/etc/shadow`. Brain's bootstrap path (`POST /setup → SetPassword`) does NOT create a real Linux user. Real `useradd` + `passwd` integration is a Tier-B follow-up.
- **`setRole` is fake even in `host-agent-real`.** Linux group membership (`molma-admin`) is not updated. Real `gpasswd` integration is a Tier-B follow-up.
- **`deleteUser` is fake even in `host-agent-real`.** No `userdel` is called. Real account deletion is a Tier-B follow-up.
- Because `setPassword` is fake, **the PAM verifier will reject passwords set via the dashboard on `host-agent-real`** — the PAM stack checks `/etc/shadow`, which was never updated. End-to-end login only works once real `setPassword` lands.

## What's next

- Real `setPassword` / `setRole` / `deleteUser` in `host-agent-real` (Linux: `useradd`, `passwd`, `gpasswd`, `userdel`).
- nspawn test fixture for PAM verify: provision `molma-pamtest` user, run `-tags pamtest` tests as root.
- Per-protocol opt-in (SSH/SMB) as service allowlists per account.
