# 0018 — nspawn fast-lane wiring for `usermgrtest`

- **Status:** done
- **Date:** 2026-05-24
- **Specs touched:** `TESTING.md`

Closes the "nspawn lane" follow-up called out by `0015`, `0016`, and `0017`.
The `usermgrtest`-tagged integration tests for `LinuxUserManager`
(`UpsertPassword`, `SetRole`, `DeleteUser`) now run inside an ephemeral
`systemd-nspawn` namespace via `make test-usermgr-nspawn`, against a
real `/etc/passwd`, `/etc/shadow`, and `/etc/group` provisioned by
`useradd`/`chpasswd`/`gpasswd`/`userdel` — not the developer's host.

Before this slice, `make test-usermgr` shelled out to `sudo -E go test`
on the developer box. Three slices had documented that as a verification
gap and none had closed it. Now closed: the same tests, automated, on a
disposable rootfs.

## What was done

### Lane shape

Per `TESTING.md` # Fast lane (≈1 minute, runs per-PR, blocks merge).
Built around a cached base rootfs at `.dev/nspawn/rootfs/` (gitignored)
plus `--ephemeral` overlays per test run so the cache never mutates.

Three pieces:

- `dev/test-nspawn/bootstrap.sh` — produces the rootfs. Sources from
  `docker export debian:bookworm` (rationale below), then runs
  `apt-get install sudo libpam-modules` and `groupadd -g 3000 molma`
  inside the rootfs via `systemd-nspawn`. Canary file
  (`.molma-nspawn-ready`) makes it idempotent.
- `dev/test-nspawn/run-usermgr-tests.sh` — wrapper. Bootstrap-if-absent
  → `go test -c -tags usermgrtest` (CGO disabled, statically linked
  binary) → `systemd-nspawn --ephemeral --bind-ro=<binary>:/usermgr.test`
  → execute. Drops to the invoking user via `logname` for the build
  step so the Go module/build cache stays user-owned.
- `Makefile` target `test-usermgr-nspawn` — wraps the script under
  `sudo -E` (nspawn needs `CAP_SYS_ADMIN`); the old `test-usermgr`
  target (which runs against the host) stays in place as the
  "I-trust-this-box" escape hatch.

### Rootfs source — docker, not mmdebstrap

`TESTING.md` doesn't pin how to build the rootfs; only the lane's runtime
(systemd-nspawn). Started with `mmdebstrap` since it's the
Debian-canonical bootstrap tool. Bailed because:

- The Ubuntu-shipped `debian-archive-keyring` package is stuck at the
  stretch era; bootstrapping `bookworm` from an Ubuntu host fails with
  `NO_PUBKEY` and needs a current keyring fetched out-of-band.
- mmdebstrap's unshare mode creates files owned by subuid mappings,
  so a failed run leaves rootfs fragments that the developer can't
  clean without `sudo rm -rf`.
- `mmdebstrap --keyring=...` (with a fresh keyring) silently printed
  help instead of running — likely a flag-order or quoting issue, but
  every minute spent on that is a minute not running tests.

`docker export debian:bookworm` sidesteps all three:

1. Image signing is docker's problem, not ours.
2. No subuid mapping — files come out root-owned, which is what nspawn
   wants anyway.
3. Docker is already a project dep (Caddy in `make dev`).

Cost: `docker pull` on first run (one-time, ~80 MB). CI on a real Debian
box can swap back to `mmdebstrap` later by overriding `bootstrap.sh` —
the rootfs structure is the contract, not the tool that produced it.

### Tests exercised

The 11 tests in `internal/hostagent/usermgr/` run green inside the
nspawn rootfs, including the three `usermgrtest`-tagged integration
tests that the host-only lane already runs:

- `TestUpsertPassword_CreateThenUpdate` — real `useradd` + real
  `chpasswd`; asserts `/etc/shadow` carries a usable hash.
- `TestSetRole_PromoteDemoteIdempotent` — real `gpasswd -a/-d sudo`;
  asserts `/etc/group` membership flips and double-add / double-remove
  return nil. The rootfs's `sudo` group (installed by the `sudo`
  package) makes this path real, not skipped.
- `TestDeleteUser_CreateDeleteIdempotent` — real `userdel -r -f`;
  asserts `user.Lookup` returns `UnknownUserError` after delete and
  second delete returns nil.

`pickGroup()` falls back to `nogroup` / `users` when `molma` is absent;
the rootfs has `molma` (GID 3000) pre-provisioned per `FIRST_RUN.md`,
so the test exercises the real path.

## How it maps to the specs

- `TESTING.md` # Fast lane — the usermgr tests are exactly the
  "service-level integration: brain ↔ host-agent ↔ Caddy contracts"
  bucket. Lane infrastructure now exists; future fast-lane tests can
  reuse `bootstrap.sh` (extend the in-rootfs `apt-get` line) and
  `run-...-tests.sh` (copy and rename) without inventing scaffolding.
- CLAUDE.md "verify against real system before committing" — the
  failure modes this catches (a `useradd` flag that silently no-ops, a
  `gpasswd` exit code our wrapper doesn't expect, a PAM module path
  baked into chpasswd that differs between Debian versions) only
  surface against real `/etc/passwd`. The host-only `test-usermgr`
  caught them too; the nspawn lane catches them on any contributor's
  box, not just the molma OS itself.

## Known gaps & deviations (loud)

- **First run is not 1 minute.** The `docker pull debian:bookworm` +
  `apt-get install sudo libpam-modules` cold-start takes ~30s on a
  decent connection. Steady-state (rootfs cached, `--ephemeral`
  overlay only) is well under a second per test invocation. The 1-min
  budget in `TESTING.md` is a steady-state target; first-run is
  one-shot infra.
- **No CI integration yet.** This wires the *local* lane. A CI
  workflow that runs `sudo make test-usermgr-nspawn` on a Linux
  runner with systemd-container available is the obvious follow-up —
  deliberately not in scope for this slice (no CI is configured for
  the repo yet).
- **`sudo rm -rf` to clean up.** The rootfs ends up root-owned (docker
  + nspawn-driven apt). `make clean` doesn't touch
  `.dev/nspawn/rootfs/` for this reason; the developer rebuild path is
  `sudo rm -rf .dev/nspawn`. Documented in the bootstrap script's
  header comment.
- **Ubuntu host workaround leaks into the spec.** `TESTING.md` # Fast
  lane mentions `mmdebstrap` indirectly; this slice's rootfs source is
  `docker export`. That's a tooling deviation, not a lane-shape
  deviation — the lane is still systemd-nspawn with a Debian rootfs.
  Documented here; not promoting to `DECISIONS.md` because the choice
  is reversible per-host via an environment variable
  (`MOLMA_NSPAWN_IMAGE` is honored).
- **`molma-pamtest` user not provisioned.** `TESTING.md` # Fast lane
  notes that the `pam_linux_test.go` skeleton needs `/etc/pam.d/molma`
  installed plus a `molma-pamtest` user in the rootfs. This slice does
  not wire that — pam tests still skip. When that work happens, it's a
  small extension to `bootstrap.sh` (`COPY /etc/pam.d/molma`,
  `useradd molma-pamtest && chpasswd`), not a separate lane.
- **Build artifact (`.dev/nspawn/usermgr.test`) is root-owned** because
  the build is invoked via `sudo -u $CALLER env ...` from a root
  script — `env` runs under the target uid but the file's parent dir
  may already be root-owned from the bootstrap. Harmless in practice
  (the binary is rebuilt every run); flagged so the next contributor
  isn't surprised.

## What's next

- CI workflow: `sudo make test-usermgr-nspawn` as a per-PR gate, once
  any CI is set up.
- Promote the bootstrap into a reusable shape when the second
  nspawn-lane consumer lands (pam, network namespace tests, brain ↔
  host-agent contract suite). Today there's only one consumer; the
  shared bits are 30 lines.
- Lane extension for `pamtest` (see `TESTING.md` # Fast lane: PAM
  verify coverage).
