# dist/systemd

Systemd unit files shipped with molma. Owned by `docs/specs/BOOT.md`; this
README is the developer-side install/test reference.

## Layout

| File | Install path on target |
|---|---|
| `molma-storage-ready.target` | `/etc/systemd/system/molma-storage-ready.target` |
| `molma-storage-verify.service` | `/etc/systemd/system/molma-storage-verify.service` |
| `molma-recovery.target` | `/etc/systemd/system/molma-recovery.target` |
| `host-agent.service` | `/etc/systemd/system/host-agent.service` |
| `dropins/<unit>.d/molma.conf` | `/etc/systemd/system/<unit>.d/molma.conf` |

Drop-ins live at `/etc/systemd/system/<unit>.service.d/molma.conf` per
`BOOT.md` # Tier-2 services get drop-ins. They add molma-specific ordering
without replacing the upstream Debian unit, so apt updates do not fight us.

## Binary paths assumed

- `/usr/lib/molma/molma-storage-verify` — the storage reporter (`cmd/molma-storage-verify`).
- `/usr/lib/molma/host-agent-real` — the production host-agent (`cmd/host-agent-real`).

`BUILD.md` will own the `.deb` packaging that installs these; until that
lands, this directory is the contract.

## What is intentionally not here yet

- Mount units for `srv-molma.mount`, `srv-molma-mergerfs.mount`, the per-user
  bind mounts. Those require the real LUKS + mergerfs work (separate slice).
- `cryptsetup@data.service` for the data drive — same reason.
- `molma-recovery.service` (the static port-80 page). The `molma-recovery.target`
  here is a placeholder so `OnFailure=` routing has somewhere to land;
  `RECOVERY.md` (deferred, `NEXT.md`) owns the actual UI.
- `tmpfiles.d` for `/run/molma/health/`. The reporter binary `mkdir -p`s the
  directory today; a tmpfiles.d entry is the cleaner long-term shape.
- `Type=notify` + `WatchdogSec=` on `host-agent.service`. `CONTROL_PLANE.md`
  # host-agent specifies the full hardening; not in scope for this slice.
