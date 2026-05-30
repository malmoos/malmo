# 0020 — nspawn boot-chain fast-lane tests

- **Status:** done
- **Date:** 2026-05-27
- **Specs touched:** `TESTING.md`, `BOOT.md`

Closes the "no real boot validation" gap called out in
[`boot-pipeline-units.md`](boot-pipeline-units.md) # Known
gaps. The `dist/systemd/` units shipped in 0019 were hand-validated by
reading only; this slice puts them in front of a live systemd
inside `systemd-nspawn --boot` and asserts the shape `TESTING.md`
# Fast lane enumerates: unit dependency errors, drop-in overrides
applying, synthetic targets activating in the right shape.

The fast lane established by [nspawn-usermgr-lane.md](nspawn-usermgr-lane.md) ran a
Go test binary inside the namespace without systemd as PID 1. This
slice introduces the **boot mode** variant of that pattern — same
ephemeral-overlay hygiene, but with `--boot` so unit files actually
load and `systemctl` can answer questions.

## What was done

### `dev/test-nspawn/bootstrap.sh` — `systemd-sysv` added, canary versioned

The cached `debian:bookworm` rootfs from `docker export` is minimal —
no `systemd`, no `/sbin/init`. `--boot` needs both. Added
`systemd-sysv` to the apt install step. It pulls in `systemd` as a
dependency; ~30 MB on disk, harmless to the no-boot lanes.

Canary changed from a touch-only marker to a versioned content gate
(`v2`). The bootstrap rebuilds when the content doesn't match —
otherwise users with a slice-0018 rootfs would silently skip the new
install step and the boot lane would fail on a stale base. Refusing to
upgrade is loud (`rebuilding` log line), not silent.

### `dev/test-nspawn/boot-assertions.sh` — in-container assertions

Runs inside the booted nspawn under `malmo-boot-test.service`. Six
assertion groups:

1. `systemd-analyze verify` parses every unit. (The bookworm systemd
   inside the container has the `verify` subcommand; the host's older
   systemd 245 does not support `--root`, so the static check lives
   inside.)
2. `systemctl cat <unit>` for `docker`, `smbd`, `avahi-daemon` finds
   each drop-in's `malmo-storage-ready.target` reference. Stub parent
   units (see below) make this work in a rootfs that lacks the real
   services.
3. `systemctl list-dependencies malmo-storage-ready.target` lists
   `malmo-storage-verify.service` in its dependency tree.
4. `malmo-storage-verify.service` carries `Before=malmo-storage-ready.target`.
5. `host-agent.service` carries `After=malmo-storage-ready.target docker.service`,
   `OnFailure=malmo-recovery.target`, and `StartLimitBurst=5`.
6. `systemctl start malmo-storage-verify.service` succeeds, writes a
   parseable `/run/malmo/health/storage.json`, and the payload is
   Level-0-shaped (empty findings against a clean rootfs).

Verdict is written to `/var/lib/malmo-boot-result` (PASS or `FAIL: <line>`);
that file is bind-mounted RW from the host so the driver can read it
after the container powers off.

### `dev/test-nspawn/run-boot-chain-tests.sh` — host driver

Mirrors `run-usermgr-tests.sh`'s shape (caller resolution, Go binary
discovery under sudo, root check, bootstrap-if-absent). Then:

- Builds `malmo-storage-verify` statically (`CGO_ENABLED=0`) under the
  invoking user so the Go module cache stays user-owned. The Go build
  is needed because the rootfs has no Go toolchain — same pattern as
  0018.
- Stages units into `.dev/nspawn/boot-stage/etc/systemd/system/`:
  - Real units + targets from `dist/systemd/`.
  - Drop-ins copied to `<unit>.service.d/malmo.conf` (matching the
    on-target layout in `dist/systemd/README.md` # Layout).
  - **Stub parent units** for `docker.service`, `smbd.service`,
    `avahi-daemon.service` (`Type=oneshot ExecStart=/bin/true`).
    systemd does not surface drop-ins whose parent unit is missing;
    the stubs let `systemctl cat <svc>` succeed without installing the
    real packages.
  - The `malmo-boot-test.service` driver unit (oneshot, after
    `basic.target`, `ExecStopPost=/bin/systemctl --no-block poweroff`),
    plus a `basic.target.wants/` symlink to enable it. (We attach to
    `basic.target` rather than `multi-user.target` because the latter
    blocks on getty/console services in the minimal rootfs;
    `basic.target` is enough for the `systemctl show`/`list-dependencies`
    queries the assertions run.)
- Boots `systemd-nspawn --boot --ephemeral` with the staging tree bound
  onto `/etc/systemd/system`, the verifier binary bound at
  `/usr/lib/malmo/malmo-storage-verify`, `/bin/true` stubbed at
  `/usr/lib/malmo/host-agent-real` (so `host-agent.service` can load —
  it never starts because nothing pulls it in), the assertions script
  at `/usr/local/bin/boot-assertions.sh`, and the host's result file
  bound RW at `/var/lib/malmo-boot-result`.
- Wraps the whole nspawn invocation in `timeout 60s` — assertions
  should complete in single-digit seconds; the ceiling catches a hung
  container so CI doesn't burn an hour.
- Reads the verdict, exits 0 on `PASS`, non-zero on anything else.

`--ephemeral` is non-negotiable (matches 0018). `--register=yes` is
deliberately omitted to avoid host-side `systemd-machined` flake.

### `Makefile` — `test-boot-chain-nspawn` target

`sudo -E ./dev/test-nspawn/run-boot-chain-tests.sh`. Wired into
`.PHONY` and `make help`.

## How it maps to the specs

- `TESTING.md` # Fast lane — "Unit dependency errors", "Drop-in
  overrides applying correctly", "Synthetic targets activating in the
  right shape" are all asserted here. "Service-level integration:
  brain ↔ host-agent ↔ Caddy contracts" is `test-health` / `test-caddy`'s
  job; this slice is unit-shape only.
- `BOOT.md` # The storage-ready target — the dependency chain
  `malmo-storage-ready.target ← malmo-storage-verify.service` is
  asserted at boot.
- `BOOT.md` # Failure → recovery target — `OnFailure=malmo-recovery.target`
  on `host-agent.service` is asserted; the verifier's *absence* of
  `OnFailure=` is implicit (any `OnFailure=` line would surface via
  `systemctl show`, but we don't assert the negative explicitly).

## Known gaps & deviations

- **Stub parent units for docker/smbd/avahi-daemon.** The drop-ins are
  validated against `Type=oneshot ExecStart=/bin/true` stubs, not the
  real packages. Catches typos and missing references, doesn't catch
  ordering bugs that emerge only with the real services. The medium
  lane (QEMU+swtpm, separate slice) will exercise the real chain.
- **`host-agent.service` never starts.** Its `ExecStart` is stubbed to
  `/bin/true`, and no `.wants` symlink is staged so multi-user.target
  doesn't pull it in. Assertions query `systemctl show` (unit metadata
  only), not `is-active`. The fast lane proves *shape*; reach-active is
  medium-lane work.
- **No CI integration.** Same status as `test-usermgr-nspawn` and
  `test-health` — there is no CI on this repo yet. Each lane is
  invocable locally via `make`; wiring all three into a CI workflow is
  a separate slice.
- **Host systemd is 245 (Ubuntu 20.04)** in the current dev
  environment. `systemd-analyze verify --root=` is not supported on
  systemd <247, so the static-verify step lives *inside* the booted
  container (systemd 252+ from bookworm). On hosts with newer systemd,
  we could short-circuit with a host-side check; not worth the
  conditional today.
- **`--overlay-ro` not used.** Originally planned (overlay the staging
  tree on top of the rootfs's `/etc/systemd/system`); shipped with
  `--bind-ro` of the whole staging directory instead. Simpler, fewer
  moving parts; revisit if staging conflicts with rootfs-provided
  units surface.

## Lessons from the first end-to-end run

Things that bit during bring-up — recorded here so the next person
adding a fast-lane service doesn't relearn them:

- **Replacing `/etc/systemd/system` with `--bind-ro` drops the rootfs's
  `default.target` symlink.** systemd then falls back to
  `/lib/systemd/system/default.target` → `graphical.target` and hangs
  waiting for a display manager. Staging needs an explicit
  `default.target → /lib/systemd/system/multi-user.target` symlink.
- **`/run` is remounted as tmpfs by systemd PID 1**, masking any
  bind-mount placed under `/run/`. Result-file binds and any
  host-injected files that need to survive boot must live under
  `/var/lib/` or another non-tmpfs path.
- **Graceful shutdown of the container is unreliable** in a minimal
  rootfs whose `/etc/systemd/system` has been replaced. `systemctl
  poweroff` (with or without `--no-block`, `--force`, `--force --force`)
  and `kill -SIGRTMIN+4 1` all stall on `StopJob` timeouts. The fast
  lane sidesteps this by polling the result file from the host and
  SIGKILL'ing the nspawn supervisor as soon as a verdict is written.
  `rc=137` from nspawn is therefore the expected happy-path exit code,
  not an error.
- **`set -e` + `wait $PID`**: when the host driver explicitly kills the
  container, `wait` returns the SIGKILL exit code (137). Under
  `set -e` that aborts the driver before it reads the verdict. Use
  `NSPAWN_RC=0; wait "$NSPAWN_PID" || NSPAWN_RC=$?` to capture without
  aborting.
- **Python and `jq` are not in the minimal bookworm rootfs.** JSON
  shape checks in the in-container assertion script have to be
  grep/case-based. The reporter emits pretty-printed JSON (with
  spaces), so collapse whitespace before pattern-matching.
- **Drop-ins without parent units are silently ignored by `systemctl
  cat`.** Stub `Type=oneshot ExecStart=/bin/true` parent units for
  docker/smbd/avahi-daemon let drop-in assertions work in a rootfs
  that doesn't have the real services installed.
- **`--quiet` on `systemd-nspawn` does not suppress the container's
  PID-1 boot output** (it only suppresses nspawn's own status
  messages). Leaving boot logs visible is fine — they're useful for
  debugging — but suppressing them would require `--console=passive`
  (systemd 247+) or output redirection.
- **`--private-network`** is cheap and removes the only realistic
  host-side risk surface (container can otherwise bind to host
  interfaces). Included by default.
- **Failure injection verified.** Two mutations (renaming the
  drop-in's target reference, commenting out `Before=` on the
  verifier) both surfaced as loud, specific `FAIL:` verdicts. The
  lane catches what it claims to catch.

## What's next

In recommended order:

- **CI workflow that runs the three nspawn lanes** (`test-usermgr-nspawn`,
  `test-boot-chain-nspawn`, `test-health` — the latter is non-nspawn
  but in the same fast-lane budget). Per-PR, blocks merge per
  `TESTING.md` # Where each lane runs. Requires a CI host with
  `systemd-nspawn` available.
- **Extend the boot lane with the avahi reconciler chain** once
  `0013-avahi-dbus-publisher.md`'s host-side units land — assert their
  drop-in ordering against `malmo-storage-ready.target` and
  `malmo-recovery.target`.
- **Replace stub parent units with real packages** opportunistically
  — installing `docker.io` in the rootfs is a non-starter (image
  bloat), but `avahi-daemon` and `samba` are cheap and would let us
  assert against the real units.
- **Negative-case fixtures.** A `fixtures/` subdir of intentionally
  broken unit files (missing `ExecStart`, dangling `After=`, etc.)
  driven through `systemd-analyze verify` to confirm the verify step
  actually rejects them — guards against the "test always passes
  because nothing is checked" regression.
- **`storage-verify-timeout` synthesis** once the wrapper unit exists
  (carried forward from
  [`boot-pipeline-units.md`](boot-pipeline-units.md) # Known
  gaps); this lane is the right home for asserting it.
