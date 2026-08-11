# The box declares its control-plane pair in two files, not one

- **Status:** done
- **Date:** 2026-08-11
- **Specs touched:** docs/specs/UPDATES.md (# 8.3, # Locked decisions)

Fourth implementation slice of the update work designed in [#369](https://github.com/malmoos/malmo/pull/369), after [brain-healthz-probe.md](brain-healthz-probe.md) gave the updater something to probe. Closes #379.

## What was done

This is the declaration half of the control-plane updater — no Docker, no network, no transaction. New package `internal/hostagent/controlplane`:

- **`RewriteUIImage(dir, ref)`** points the `malmo-ui` service in the staged compose at a new image and returns the ref it replaced. This is the `UPDATES.md` # 8.3 handoff: host-agent writes it *before* recreating anything, so the brain reconciles to the version already running instead of reverting it.
- **The ledger** (`images.json`, beside `compose.yml`): the pair the box should be running, the pair it ran before, and when each was applied. `ReadLedger` / `WriteLedger` / `ResolveBrainImage` / `PreviousExpired`.
- **`cmd/host-agent-real`** now resolves the brain image through the ledger, falling back to `MALMO_BRAIN_IMAGE` and logging which source won.

**The spec said one file; the box needs two, and that gap is the real finding here.** `UPDATES.md` # 8.3 read as though host-agent writes "the new image references" into the staged compose and the brain reconciles to them. That works for `malmo-ui`, a compose service. It cannot work for the brain: a process cannot bring itself up, so `brainlaunch` starts the brain with `docker run` from `MALMO_BRAIN_IMAGE` — the brain is not in that compose at all. Without a second declaration, an applied update is one `docker rm` away from undoing itself: `brainlaunch` leaves an existing brain container alone, but a box whose brain container is gone relaunches the ref baked into the disk image at build time and silently goes backwards. # 8.3 and the matching locked-decision bullet now describe both files.

**The rewrite edits one line instead of round-tripping the YAML.** `dev/control-plane/compose.yml` is hand-authored and heavily commented — why Caddy's image is interpolated, why the socket-proxy is deliberately absent, why the network is external — and a marshal-based rewrite would delete every one of those comments the first time a box updated. A one-line edit also means a bug here can damage exactly one line. The scan is service-scoped by indentation, because `image:` under `caddy` comes first in the real file and a naive scan would repoint the reverse proxy at the dashboard bundle.

**Both files are written atomically** (same-directory temp, fsync, rename, fsync the directory). Both are read at boot to decide what the box runs, so a torn write from a power cut mid-update would be read by the next boot as the box's identity.

**Every read failure falls back rather than refusing.** A missing ledger is the normal state of every box shipping today, so it returns "not found" with no error. A corrupt one is an error where it is read directly, but `ResolveBrainImage` still falls back to the baked ref — the brain is how anyone finds out what is wrong with the box, so it has to come up.

## How it maps to the specs

- `UPDATES.md` # 8.3 — the handoff point, now with the two-file reality and why the second file exists.
- `UPDATES.md` # 3 — `RetentionWindow` (7 days) and the single previous generation, matching "no n-deep history in v1".
- `CONTROL_PLANE.md` — unchanged and respected: the brain still owns the compose stack, host-agent still launches the brain.
- `CLAUDE.md` # Layer boundaries — the compose read-back parse is duplicated here rather than importing `internal/lifecycle` (brain-only). Ten lines of struct is the cheaper side of that boundary, and both sides are held to the same file by tests that parse the committed `dev/control-plane/compose.yml`.

## Known gaps & deviations

- **Nothing writes the ledger yet.** The transaction that applies an update (#380) is the writer; this slice ships the reader, the writer, and the resolution rule. Until then every box takes the fallback path, which is exactly today's behaviour.
- **`PreviousExpired` answers the retention question but deletes nothing.** Removing the old images and the SQLite snapshot belongs to the code that created them (#380).
- **An inline comment on the UI's `image:` line is dropped by a rewrite.** Deliberate — a comment about the old ref is wrong the moment the ref changes — but it is a silent loss, and the committed file has no such comment today.
- **The `${VAR}` case is refused, not resolved**, mirroring `lifecycle.ControlPlaneUIImage` on the read side. If the UI service ever becomes interpolated like the caddy one, updates stop with a clear error rather than writing something `docker compose` would resolve differently.
- **`cmd/host-agent-real` cannot be compiled or vetted on this machine** (the cgo PAM dep's `C.RTLD_NEXT`, a pre-existing condition on `dev`), so the one-line wiring there was type-checked with `CGO_ENABLED=0` and is otherwise proven by CI. The resolution rule itself lives in the package and is unit-tested, which is why it was put there rather than inline in `main.go`.
- **No box has run this.** The rewrite is tested against the real committed compose, but a booted box writing a ledger and relaunching from it is #382.

## What's next

1. **#380 — the update transaction.** Pull by digest, snapshot the brain's SQLite, write both declarations, recreate only what changed, probe, revert both on failure, GC after 7 days.
2. **#381 — the trigger.** A minimal `system-update` job plus the admin endpoint that starts it.
3. **#382 — the QEMU proof.** A real update and a real revert on a booted box.
