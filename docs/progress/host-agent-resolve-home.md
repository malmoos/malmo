# host-agent GET /v1/users/{username}/home (slice 2 of install consent flow)

- **Status:** done
- **Date:** 2026-05-30
- **Specs touched:** docs/specs/BRAIN_HOST_PROTOCOL.md

## What was done

Added `GET /v1/users/{username}/home → { home_path, uid, gid }` to host-agent. The brain runs containerized and has no access to `/etc/passwd`; this endpoint lets it resolve the owner's home directory and POSIX UID/GID so that slice 4 (writeOverride) can emit correct bind-mount sources and `user:` directives for personal-scope app instances.

Code changes:

- `internal/protocol/host.go` — new `ResolveHomeResponse{HomePath string, UID int, GID int}` with JSON tags `home_path/uid/gid`.
- `internal/hostagent/agent.go` — `ErrUnknownUser` sentinel; `ResolveHome(user string) (home string, uid, gid int, err error)` added to the `UserManager` consumer-side interface; `resolveHome` handler registered in `Mount`; fake branch returns `/home/<username>` + stable UID/GID in [3000, 3999] from FNV-32a hash of the username; 404 with `unknown-user` when the real manager returns `ErrUnknownUser`.
- `internal/hostagent/usermgr/linux.go` — `ResolveHome` implemented on `LinuxUserManager` via `os/user.Lookup`; maps `user.UnknownUserError` to `hostagent.ErrUnknownUser`; parses `u.Uid`/`u.Gid` via `strconv.Atoi`.
- `internal/hostclient/hostclient.go` — `ResolveHome(ctx, username)` method calling `GET /v1/users/<url-escaped-username>/home`.
- `internal/lifecycle/docker.go` — `ResolveHome(ctx, user)` added to `HostDriver` interface (consumed by writeOverride in slice 4).
- Tests: `internal/hostagent/agent_test.go` (fake branch stable/deterministic, delegate path, 404 shape, 500 shape); `internal/hostclient/hostclient_test.go` (round-trip over UNIX socket + 404 error path); `internal/lifecycle/fakes_test.go` (`fakeHost.ResolveHome` stub to satisfy updated interface).

## How it maps to the specs

Realizes `BRAIN_HOST_PROTOCOL.md` # User info endpoints (new section). The pattern (Pattern A sync, typed 404 with `code` field) mirrors the credential-mutation siblings. `ErrUnknownUser` follows the "typed errors at boundaries" rule from CLAUDE.md: the brain needs to discriminate unknown-user (install error) from host unreachable (retry).

## Known gaps & deviations

`cmd/host-agent-real/main.go` wires `a.UserMgr = &usermgr.LinuxUserManager{}` — `ResolveHome` is automatically live in the real binary once `LinuxUserManager` implements the interface, which it now does. No main changes needed.

The well-known box constants slice 4 also needs (`molma_app_uid`/`molma_app_gid`/`molma_shared_gid` for household-scope instances) are deferred to slice 4, which can add `GET /v1/identity/well-known` or fold them into an existing summary endpoint. Flagged in slice 4's scope.

## What's next

- **Slice 3** — `GET /api/v1/catalog/{id}/install-plan`: read-only endpoint that returns permission lines, role-derived scope options, per-folder source options, and pick-subfolder prompts. No host call needed; reads from the parsed manifest.
- **Slice 4** — `writeOverride` + `writeEnv` enforce permissions: `user:`, folder bind mounts from elected source, `group_add` for shared, devices, GPU, `MOLMA_FOLDER_*` injection. Calls `ResolveHome` (this slice). Decide whether `GET /v1/identity/well-known` belongs here or can use hard-coded defaults.
- **Slice 5** — consent + config UI in `web-ui/src/views/StoreView.vue`.
