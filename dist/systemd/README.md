# dist/systemd

Systemd unit files shipped with malmo. Owned by `docs/specs/BOOT.md`; this
README is the developer-side install/test reference.

## Layout

| File | Install path on target |
|---|---|
| `malmo-storage-ready.target` | `/etc/systemd/system/malmo-storage-ready.target` |
| `malmo-storage-verify.service` | `/etc/systemd/system/malmo-storage-verify.service` |
| `malmo-recovery.target` | `/etc/systemd/system/malmo-recovery.target` |
| `host-agent.service` | `/etc/systemd/system/host-agent.service` |
| `dropins/<unit>.d/malmo.conf` | `/etc/systemd/system/<unit>.d/malmo.conf` |

Drop-ins live at `/etc/systemd/system/<unit>.service.d/malmo.conf` per
`BOOT.md` # Tier-2 services get drop-ins. They add malmo-specific ordering
without replacing the upstream Debian unit, so apt updates do not fight us.

## Binary paths assumed

- `/usr/lib/malmo/malmo-storage-verify` — the storage reporter (`cmd/malmo-storage-verify`).
- `/usr/lib/malmo/host-agent-real` — the production host-agent (`cmd/host-agent-real`).

`BUILD.md` will own the `.deb` packaging that installs these; until that
lands, this directory is the contract.

## What is intentionally not here yet

- Mount units for `srv-malmo.mount`, `srv-malmo-mergerfs.mount`, the per-user
  bind mounts. Those require the real LUKS + mergerfs work (separate slice).
- `cryptsetup@data.service` for the data drive — same reason.
- `malmo-recovery.service` (the static port-80 page). The `malmo-recovery.target`
  here is a placeholder so `OnFailure=` routing has somewhere to land;
  `RECOVERY.md` (deferred, `NEXT.md`) owns the actual UI.
- `tmpfiles.d` for `/run/malmo/health/`. The reporter binary `mkdir -p`s the
  directory today; a tmpfiles.d entry is the cleaner long-term shape.
- `Type=notify` + `WatchdogSec=` on `host-agent.service`. `CONTROL_PLANE.md`
  # host-agent specifies the full hardening; not in scope for this slice.
