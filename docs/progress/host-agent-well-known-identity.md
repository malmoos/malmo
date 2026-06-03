# host-agent GET /v1/identity/well-known (slice 4a of install consent flow)

- **Status:** done
- **Date:** 2026-05-30
- **Specs touched:** docs/specs/BRAIN_HOST_PROTOCOL.md

## What was done

Added `GET /v1/identity/well-known → { molma_app_uid, molma_app_gid, molma_shared_gid }` to host-agent. This is the second host-side input the override generator (slice 4) needs, alongside `ResolveHome` (slice 2, [host-agent-resolve-home.md](host-agent-resolve-home.md)). Where `ResolveHome` answers "what UID does *this owner's* personal instance run as," this endpoint answers the two host-identity facts that are not per-user: the shared service identity a **household** instance runs as (`molma-app`), and the GID a **shared-source** folder mount joins (`molma-shared`). Split out as its own slice because it is self-contained and independently testable — the enforcement work that consumes it lands next.

Code changes:

- `internal/protocol/host.go` — new `WellKnownIdentityResponse{MolmaAppUID, MolmaAppGID, MolmaSharedGID int}` with JSON tags `molma_app_uid/molma_app_gid/molma_shared_gid`.
- `internal/hostagent/agent.go` — `WellKnownIdentity() (appUID, appGID, sharedGID int, err error)` added to the `UserManager` consumer-side interface; `wellKnownIdentity` handler registered in `Mount` for `GET /v1/identity/well-known`; fake branch (`UserMgr == nil`) returns fixed dev constants `2000/2000/2001`; real branch delegates and maps any error to a generic 500 (no typed error — there is no unknown-user case here).
- `internal/hostagent/usermgr/linux.go` — `WellKnownIdentity` on `LinuxUserManager`: `os/user.Lookup("molma-app")` for the service UID/GID, `os/user.LookupGroup("molma-shared")` for the shared GID, each wrapped with context. These accounts are provisioned by the box build; absent on the dev box, where only the fake branch runs.
- `internal/hostclient/hostclient.go` — `WellKnownIdentity(ctx)` via the standard `do` helper (`GET /v1/identity/well-known`). Unlike `ResolveHome` it does not hand-roll the request, because there is no 404 to discriminate into a typed sentinel.
- Tests: `internal/hostagent/agent_test.go` (fake branch returns the fixed constants; delegate path; 500-on-error that asserts the response does not leak the `molma-app` lookup detail); `internal/hostclient/hostclient_test.go` (round-trip over the UNIX socket). `stubUserMgr` gains a `WellKnownIdentity` stub to satisfy the updated interface.

## How it maps to the specs

Realizes the new `GET /v1/identity/well-known` block under `BRAIN_HOST_PROTOCOL.md` # User info endpoints. Same Pattern A shape and fake/real branching as `ResolveHome`. The chosen fake constants (`2000`/`2000`/`2001`) sit below the per-user fake-UID range `[3000, 3999]` so service identities never collide with hashed user UIDs in the dev loop. The field semantics match `APP_ISOLATION.md` # User content: `molma_app_*` is the household `user:`, `molma_shared_gid` is the `group_add` for any shared-source mount.

## Known gaps & deviations

- **No consumer yet.** `HostDriver` (`internal/lifecycle/docker.go`) is deliberately *not* extended in this slice — the override generator that calls `WellKnownIdentity` is slice 4 proper. `hostclient.Client` already exposes the method; lifecycle picks it up next.
- `cmd/host-agent-real/main.go` needs no change — `LinuxUserManager` gaining a method keeps satisfying the interface, so the endpoint is automatically live in the real binary.
- `internal/hostagent/pamverifier` still fails to build here (CGO `C.RTLD_NEXT`); pre-existing, unrelated.

## What's next

- **Slice 4 — enforce in `writeOverride`/`writeEnv`.** Thread the user's per-folder elections (scope + source + subfolder) through `Install`; stamp `user:` (personal → `ResolveHome` UID/GID, household → `molma-app` from this slice), folder bind mounts from the elected source, `group_add` with `molma_shared_gid` for shared sources, `devices`, GPU, and `MOLMA_FOLDER_*`. Add `WellKnownIdentity` to the `HostDriver` interface. Server-side validate the elections in `internal/api` (authoritative; install-plan is advisory) and audit rejections. Extend `catalog/whoami` with a `folders` declaration and verify a real dev-loop install per `feedback_verify_before_commit`.
- **Slice 5 — consent + config UI.**
