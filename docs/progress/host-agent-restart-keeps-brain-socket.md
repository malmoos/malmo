# host-agent restart no longer cuts the brain off from the socket

- **Status:** done
- **Date:** 2026-09-07
- **Specs touched:** docs/specs/CONTROL_PLANE.md, docs/specs/BOOT.md

## What was done

**Closes #447.** Follows [update-target-read.md](update-target-read.md), whose new cloud-lane assertion found this and deliberately worked around it rather than fixing it.

`host-agent.service` declared `RuntimeDirectory=malmo` and nothing else. systemd deletes a `RuntimeDirectory=` when the unit stops and creates a **new inode** on start. The brain reaches the agent socket through a bind mount of that *directory* (`brainlaunch.runSpec` mounts `filepath.Dir(cfg.SocketPath)`), and Docker resolves a bind mount into the container's mount namespace when the container **starts**, not per call. So after a plain `systemctl restart host-agent`, the brain's mount still pointed at the deleted inode: it saw an empty directory, never saw the new `agent.sock`, and every host-backed call answered 502.

Nothing closed that window. The brain runs `restart=unless-stopped` and host-agent does not touch a running brain, so the box stayed in that state until a reboot. **Nobody could log in for the whole time** — the brain holds no password hash and calls host-agent on every login (`AUTH.md` # Identity primitive) — while the box looked healthy from outside: every container `Up`, and every endpoint that needs no host call still answering.

The fix is one line on the unit, in `dist/systemd/host-agent.service`:

```
RuntimeDirectoryPreserve=yes
```

`dist/systemd/` is the only copy. All three lanes `cp` from it — `dev/cloud/stage-control-plane.sh:96`, `dev/test-qemu/bootstrap.sh:287`, `dev/test-nspawn/run-boot-chain-tests.sh:103` — so the two `mkosi.extra` paths #447 named in its Touch list are gitignored build artifacts, not sources.

**Both halves are now measured, so neither is left as inference.** #447 measured the Docker half twice and flagged the systemd half as its "one thing not verified". Measured here on a real systemd (245), with a throwaway unit:

```
RuntimeDirectory alone:      inode 38737 -> 38741, contents wiped, directory GONE on stop
+ RuntimeDirectoryPreserve:  inode 38775 -> 38775, contents kept, directory survives stop
```

`man systemd.exec` matches: `RuntimeDirectory=` dirs "are always removed when the service stops" unless `RuntimeDirectoryPreserve=` says otherwise, and for system services they are removed on reboot regardless — which is why preserving one leaks nothing durable.

**A second live bug closed by the same line, found while checking the fix.** `/run/malmo` also holds `/run/malmo/health/storage.json`, written by `malmo-storage-verify` — a `Type=oneshot` with `RemainAfterExit=yes`, pulled in by `malmo-storage-ready.target`, which therefore runs **once at boot and never again**. So host-agent stopping deleted a file it does not own and nothing regenerates. Worse, `HealthSource` treats a missing report as **empty findings**, not as an error (`internal/hostagent/agent.go` — "missing report = empty findings"), by design, so the box would go on reporting "storage is fine" for the rest of its uptime after any host-agent restart. That is a silent false negative on health, from the same root cause, and it is fixed by the same line. It was not in #447.

**The cloud lane now catches the regression.** The `update` boot already restarts host-agent at step 6a, so no new scenario was needed — #447 called that right. The comment block there that documented the bug and explained why the brain-side read was deferred is replaced by an assertion (6a-bis):

- a host-backed endpoint (`GET /api/v1/system/update-target`) answers 200 **before** the restart, so a box that never served it cannot pass by failing twice;
- it answers 200 again after, polled for up to 60s;
- **and the brain container id is unchanged.** This is the half that makes it an assertion rather than a coincidence: a recreated brain also answers 200 — that is exactly how a box "recovers" from this bug today — so without the id check the whole thing would pass green on the broken build.

The failure message dumps the `/run/malmo` inode, the unit's resolved `RuntimeDirectoryPreserve`, and the brain's mount list, so a red run says which half broke without a second boot.

Step 6c's comment was corrected too: it credited the apply's brain recreate for the read working at all. That is no longer why it works, and leaving it would have taught the next reader the bug is still there.

## How it maps to the specs

- **`CONTROL_PLANE.md` # Locked: host-agent runs under systemd** — new bullet recording the directive and why it is load-bearing rather than tidiness.
- **`CONTROL_PLANE.md`, same section** — the existing "socket activation is deliberately deferred" bullet is extended. Socket activation is the canonical answer to "this socket must outlive the service", and it would additionally let connections queue in the kernel backlog through a restart instead of failing. It was weighed here and still deferred: it buys the ~2s restart window, where `RuntimeDirectoryPreserve=yes` already removes the permanent outage, and it would cost `sd_listen_fds` handling in Go plus a non-systemd path anyway (`make dev` has no systemd). The deferral is now a measured call, not just an assumption.
- **`BOOT.md` # What downstream services do** — the `host-agent.service` bullet now says the directory is preserved and names the `storage.json` reason, since that file is a `BOOT.md` artifact.
- No locked decision flipped, so no `DECISIONS.md` entry.

## Known gaps & deviations

- **Not run on a booted box by me.** The fix is a unit-file directive, so `make check` cannot exercise it; the lane assertion needs `CI / Cloud image` (root + KVM). The systemd behaviour is measured above on real systemd and the Docker behaviour is measured in #447, but the two have not been observed together on a malmo box. That is what the `update` boot proves, and it should be read before this merges.
- **The health finding #447 also asked for is not here.** "A brain that cannot reach host-agent is a health finding, not a quiet degrade" — today `pullSystemHealth` logs `slog.Warn("system health: host-agent unreachable; skipping")` and returns (`cmd/brain/main.go`). Making that an issue means a new issue type, a `HEALTH.md` locus-C catalog row, a debounce policy and UI copy, which is past what #447 was sized and accepted as. Filed separately (see What's next) on the maintainer's call, rather than grown into this PR.
- **The two `mkosi.extra` copies were edited before I noticed they were gitignored.** They are regenerated from `dist/` by the staging scripts on the next build, so the edit is inert either way, but it is not part of the diff and should not be read as one.
- **`dist/systemd/host-agent.service` still diverges from `CONTROL_PLANE.md` in the ways it already did** — the spec locks `Type=notify` + `Restart=always` + `WatchdogSec=`, the unit ships `Type=simple` + `Restart=on-failure` and a comment deferring the rest. Untouched here; out of scope.

## What's next

1. Read the `CI / Cloud image` `update` boot for the new 6a-bis assertion (`gh workflow run "CI / Cloud image" --ref fix/447-runtime-dir-preserve -f publish=false`).
2. Brain-side `host-agent-unreachable` health finding, so this class cannot degrade silently again — the piece of #447 carved out above.
3. Socket activation, if the ~2s restart window ever matters. `NEXT.md` already tracks it; the note in `CONTROL_PLANE.md` now records what it would actually buy.
