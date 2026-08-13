# Container logs reach journald — Docker's log driver on both real images

- **Status:** done
- **Date:** 2026-08-13
- **Specs touched:** docs/specs/LOGGING.md

## What was done

The dashboard's per-app Logs tab was empty on every real box — hosted and appliance, every app, since the tail shipped. It opened the stream and sat on "Waiting for log output…" forever. This makes it work.

The cause was a missing image file, not a code bug. `LOGGING.md` # Docker daemon uses the `journald` log driver calls the driver switch "the single biggest configuration decision", and host-agent-real is built directly on it: `internal/hostagent/journalsource` runs `journalctl CONTAINER_NAME=<container> -f -o json`, and `CONTAINER_NAME` is a field only Docker's journald driver sets. But no image ever wrote `/etc/docker/daemon.json`, so both real profiles ran on Docker's `json-file` default, the match returned nothing, and `journalctl -f` blocked on a stream that could never produce a line.

Shipped:

- **`dev/cloud/mkosi.extra/etc/docker/daemon.json`** — `{"log-driver": "journald"}` for the hosted image. Committed, so the boot-proof test image picks it up too (`dev/cloud/test/mkosi.conf` does `Include=..`, inheriting `ExtraTrees=mkosi.extra`).
- **`dev/cloud/mkosi.extra/etc/systemd/system/docker.service.d/10-malmo-logging.conf`** — `LogRateLimitIntervalSec=0` / `LogRateLimitBurst=0`, exactly as `LOGGING.md` # Tuning specifies. journald enforces its rate limit against `_SYSTEMD_UNIT`, so once every container's stdout flows through dockerd they all share one bucket attributed to `docker.service`; under the 10000-per-30s default one chatty container silently starves every other container's lines. Disabled per-unit rather than globally, so real system services keep their cap.
- **`dev/test-qemu/bootstrap.sh`** — stages both files into the appliance lane's generated `mkosi.extra/`, byte-identical to the hosted lane. `CANARY_VERSION` bumped v26 → v27 so the medium lane rebuilds rather than booting a cached image without them.
- **`dev/cloud/cloud-assertions.sh`** — new assertion 5b in the boot proof: `docker info` reports `journald`, and `journalctl CONTAINER_NAME=malmo-brain` returns lines. Polled, because journald ingest can lag container start under a loaded TCG boot (the same race `wait_brain_log` already documents).
- **`dev/cloud/mkosi.conf`** — the comment block over `ExtraTrees=mkosi.extra` now explains the logging wiring. JSON takes no comments and `daemon.json` is two lines with no hint of what depends on it, so the "why" needed a home a reader will actually reach.
- **`docs/specs/LOGGING.md`** — the drop-in's filename synced to the `10-` prefix the rest of the tree uses.

## How it maps to the specs

Realizes `LOGGING.md` # Docker daemon uses the `journald` log driver and the Docker half of # Tuning, both of which were written but never built. Makes # Per-app logs true on a real box for the first time: "Source: brain → host-agent → `journalctl CONTAINER_NAME=<container> --follow`" was accurate about the code path and wrong about whether it returned anything.

## Known gaps & deviations

- **The journal is still volatile, so scrollback dies on reboot.** `LOGGING.md` # Journal lives on the OS drive specifies a persistent journal at `/var/log/journal/` with `Storage=persistent`, `SystemMaxUse=1G`, `RuntimeMaxUse=128M`. Neither image sets any of it and neither creates `/var/log/journal`, so Debian's `Storage=auto` lands on volatile — the journal lives in `/run` under journald's default cap (10% of RAM) and is lost on every reboot. Live tail and the 100-line backfill work; `# Per-app logs`' promise of "scrollback up to journald's cap" does not, and today's cap is "until you reboot". Deliberately left out of this change: it is a disk-sizing decision on a root partition that grows at first boot, and it would have held up a fix for a fully broken feature. Named as the next item below.
- **Backpressure is weaker than the spec assumes.** `# Tuning` reasons that with docker's rate limit off, `SystemMaxUse=1G` becomes "the sole backpressure for container output". `SystemMaxUse` is not set yet, so the real ceiling is journald's default runtime cap. Bounded and self-rotating, so this is not a disk-fill risk, but a chatty container ages useful history out faster than the spec intends until persistence lands.
- **Not verified on a booted appliance image.** The medium lane (swtpm + LUKS, `dev/test-qemu`) is local-only and needs root plus `/dev/kvm`. The appliance change is a byte-identical copy of the hosted one and the canary is bumped, but it has not been booted. The hosted lane is covered by the boot proof.
- **Only the per-app tail is fixed.** The System logs view and the diagnostic bundle's journal export are not built yet. Both were designed against the same assumption and would have had the same hole; they will now get container output for free when they land.
- **The Logs tab has no empty-state signal.** `LOGGING.md` # Apps are expected to log to stdout specifies a runtime hint — "No logs received. This app may be logging to a file." — after a container produces zero entries over a window. Not built. Had it existed, this bug would have announced itself on every app card instead of looking like a slow stream.

## What's next

1. **Persistent journal** — create `/var/log/journal` and ship the journald drop-in from `LOGGING.md` # Tuning (`Storage=persistent`, `SystemMaxUse=1G`, `RuntimeMaxUse=128M`) on both images, sized against the grown root. Closes the scrollback gap and restores the backpressure the disabled docker rate limit assumes.
2. **The zero-entries empty state** on the app card, per `# Apps are expected to log to stdout`. The cheap signal that would have caught this.
3. **Sidecar logs are unreachable.** The tail follows `main_service` only (`internal/lifecycle/lifecycle.go:1533`; `APP_LIFECYCLE.md` pins `container_name` on the main service by design), so a one-shot bootstrap container's output has no UI path. This is what sent `uptimepage` users looking for an owner sign-in link that the Logs tab can never show — worth a decision on whether the tab should follow the whole compose project.
