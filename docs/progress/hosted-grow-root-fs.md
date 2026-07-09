# hosted-grow-root-fs — the root filesystem grow never actually ran on a real box

- **Status:** done
- **Date:** 2026-07-08
- **Specs touched:** `ENVIRONMENT.md` (# Storage — corrected to describe the filesystem grow as an explicit second step, not an inherent part of the `systemd-repart` call)

Corrects [hosted-grow-root-disk.md](hosted-grow-root-disk.md)'s "verified on a real provider box" claim. A live acceptance run on a freshly built image found the **partition** grew to the full disk (`systemd-repart` extended it 8 GiB → 75.8 GiB and set the GPT `GROWFS` attribute bit, confirmed in the journal) but the **ext4 filesystem inside it stayed at exactly 8 GiB** — `dumpe2fs` on the mounted root showed the original block count untouched. The box still runs on the baked 8 GiB in practice; the disk-full/500 failure mode this was meant to close was not actually closed.

## Root cause

Found by rebooting the (still-running) probe box into Hetzner rescue mode and inspecting the disk directly (same technique as [hosted-wildcard-cert-automate.md](hosted-wildcard-cert-automate.md)): mounting the root partition read-only, reading its persistent journal, and checking the GPT partition attributes.

`GrowFileSystem=yes` in a repart definition does not itself grow a filesystem — for the root partition specifically, it only sets the GPT `GROWFS` attribute bit (confirmed via `sgdisk -i`). The actual online resize is normally performed by `systemd-growfs-root.service`, which `systemd-gpt-auto-generator` wires up as a dependency of the root mount **only when the kernel is booted with `root=gpt-auto`**. The journal for the probe box's boot has zero mentions of `growfs` anywhere — not even a "skipped" line — because that generator path was never engaged: the box's actual kernel command line is `root=PARTUUID=<uuid>`, an explicit PARTUUID pointing at the real root partition. Hetzner (and cloud providers generally) boot this way, not with `root=gpt-auto`, so `systemd-growfs-root.service` never starts and the `GROWFS` flag `systemd-repart` sets is inert — nothing is watching for it.

`malmo-grow-root.service`'s own boot-proof assertion passed throughout this, because it only checks that the unit exits successfully; on the QEMU boot-proof disk (fixed-size, no spare space) growing the filesystem is a no-op regardless of whether the growfs step runs at all, so that lane could never have caught this.

## What was done

- **`malmo-grow-root.service`**'s `ExecStart` now calls `systemd-growfs /` — the exact binary and invocation `systemd-growfs-root.service` would use, called directly instead of relying on the generator path that cloud providers' boot cmdline never triggers. Idempotent: a no-op once the filesystem already matches the partition size, same as the `systemd-repart` step it follows. This is one `/bin/sh -c` `ExecStart`, not a separate `ExecStartPost`: the review caught that `ExecStartPost` runs unconditionally, regardless of which `SuccessExitStatus=`-accepted code `systemd-repart` returned, and has no `$EXIT_STATUS`-style visibility into that code (that's `ExecStopPost`-only) — so a naive `ExecStartPost=systemd-growfs /` would also fire on `systemd-repart`'s 76/77 "nothing to grow" exits, have nothing valid to grow, and fail the whole oneshot, which the fail-closed `Requires=` drop-ins on `docker.service`/`host-agent.service` (`hosted-grow-root-disk.md`) would then turn into a hard boot failure. The merged `ExecStart` inspects `systemd-repart`'s exit code directly and only calls `growfs` on 0, passing 76/77 through as clean success — preserving the original "nothing to grow is not a failure" invariant. Verified locally (a throwaway systemd user-scope unit against stand-in binaries) for repart exit codes 0, 76, 77, and a genuine failure code.
- **`50-malmo-grow-root.conf`** comment corrected: `GrowFileSystem=yes` still sets the GPT flag (kept — harmless, and documents intent) but the comment no longer claims it grows the mounted root filesystem by itself.
- **`cloud-assertions.sh`** gains a presence check for `/usr/lib/systemd/systemd-growfs` alongside the existing `systemd-repart` check, and the stale "verified on a provider box" claim in its pass message is corrected to say what the lane actually proves (both steps ran, not that real growth happened).

## Known gaps & deviations

- **Still not exercised in CI.** As before, the QEMU boot-proof disk has no spare space, so neither the partition grow nor the filesystem grow is a real test there — both steps still only prove "ran without failing." Real growth is proven by the live acceptance run below instead, not this lane.
- **Double `systemd-repart` execution, unchanged from the prior entry.** The stock `systemd-repart.service` still runs the same repart definition statically; still harmless, still deferred.

## Live acceptance

Re-ran the real-provider-box acceptance this entry's opening paragraph called for, with the fixed image: a freshly built image (this branch, `systemd-growfs` gated on `systemd-repart`'s own exit code per the review fix above) was provisioned onto a real box through the hosted on-ramp. The box reached HTTPS on both its base name and its wildcard, and `GET /api/v1/system/storage` reported its root volume at **79.99 GB total / 78.21 GB free** — the provider disk's real size, not the baked 8 GiB (nor the 8.35 GB the pre-fix image reported). Confirms the `ExecStartPost` → merged-`ExecStart` fix grows the filesystem on real hardware, not just under the local exit-code simulation used to verify the unit's logic during review. Box torn down cleanly after the check.

## What's next

- The consolidation-onto-a-stock-unit-drop-in idea from the prior entry is still deferred; if picked up, it needs to carry this conditional `growfs` call forward too, not just the ordering/gating.
