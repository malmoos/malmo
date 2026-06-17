# mkosi emits no ISO — the live-session-ISO assumption is false

- **Status:** finding + resolution (maintainer's call made 2026-06-17; no lane built in this PR)
- **Date:** 2026-06-17
- **Specs touched:** `BUILD.md` (# 2, # 6, locked decisions — reconciled to disk-image artifacts), `DECISIONS.md` (2026-06-16 ISO entry — resolution appended), `NEXT.md` (transient Tier-1 item resolved + removed)
- **Issue:** #199 (M1 — bootable live-session ISO from mkosi), part of #196 (installable malmo epic)

#199 exists to de-risk the one piece the mkosi decision (`DECISIONS.md` 2026-06-16) flagged as unproven in-repo: that mkosi can emit a **bootable live-session ISO**, the way the medium lane's `Format=disk` proves it can emit a bootable disk image. The investigation found the assumption is not merely unproven — it is **false**: mkosi has no ISO output format at all. This slice surfaces the blocker and the forward paths "before M2/M3 build on it" (#199 "Done when") and does **not** build a `dev/iso/` lane. **The maintainer's call has since been made (2026-06-17): Path 1 — drop the literal `.iso`, ship disk images, cloud VM image first** (see # Paths forward and `DECISIONS.md`).

## What was investigated

mkosi **26** (the pipx-installed version on the build host; same version as #189) was inspected directly — its output formats are the entire ISO question.

- `mkosi --help` lists `--format {confext,cpio,directory,disk,esp,none,portable,sysext,tar,uki,oci,addon}`.
- The source enum is authoritative: `mkosi/config.py:213` —

  ```python
  class OutputFormat(StrEnum):
      confext = enum.auto()
      cpio = enum.auto()
      directory = enum.auto()
      disk = enum.auto()
      esp = enum.auto()
      none = enum.auto()
      portable = enum.auto()
      sysext = enum.auto()
      tar = enum.auto()
      uki = enum.auto()
      oci = enum.auto()
      addon = enum.auto()
  ```

- There is **no `iso` / `cdrom` member**, and no ISO machinery anywhere in the package: a recursive grep for `iso9660`, `xorriso`, `el torito`, `mkisofs` across `mkosi/` returns nothing. mkosi builds **GPT disk images** (`Format=disk`) and expects the artifact to be `dd`'d to a USB stick; ISO9660 / El-Torito optical-media packaging is deliberately out of its scope and has never been an mkosi output.

## The finding

**mkosi cannot produce an `.iso`.** The closest mkosi-native bootable artifact is `Format=disk` — the GPT disk image the medium lane already builds and boots under QEMU+OVMF+swtpm with the control-plane images baked in and `docker load`ed at first boot (`qemu-fullstack-app-install.md`, M2 boot acceptance PASSED). That is functionally a cold-boot live session; it is a `.raw`/`.img` disk, not a `.iso`.

This contradicts three places that assert mkosi yields the ISO:

- `BUILD.md` # 2 — "**mkosi is the single image builder** — for the install ISO, the cloud VM image, and the QEMU test image."
- `BUILD.md` # 6 — the release artifact is named `malmo-vX.Y.Z-amd64.iso`.
- `DECISIONS.md` 2026-06-16 — the locked mkosi-over-live-build call, whose "knowingly accepted" note already hedged that "a live installer ISO that boots a session is live-build's home turf … the one part of mkosi's fit not yet proven in-repo." The sharper truth: it is not provable as written, because mkosi has no ISO path.

The "live fs == installed fs" invariant (`BUILD.md` # 3) is **not** what breaks — a `Format=disk` root is exactly what the later installer slice lays on disk. What breaks is the literal `.iso` packaging.

## Paths forward — and the call (Path 1, made 2026-06-17)

1. **✅ CHOSEN — `Format=disk` images, drop the literal `.iso`.** mkosi-native, zero new tooling, holds "single builder" exactly. The artifacts are a `qcow2`/`raw` **cloud VM image** and a `raw` image flashed to **USB** for bare metal (what mkosi assumes, and what UEFI boots directly). Cost: retire the `.iso` artifact + "ISO" vocabulary in `BUILD.md`; no optical-media path — explicitly out of scope, the product never needed CD/DVD boot. Lowest-friction landing and mkosi's own distribution model.
2. **Thin `xorriso` / `grub-mkrescue` post-step wrapping mkosi's rootfs into a true hybrid `.iso`.** Yields a literal isohybrid `.iso` (boots from CD *and* USB). Cost: introduces a non-mkosi tool, which reopens "mkosi is the **single** builder" (`BUILD.md` # 2 / `DECISIONS.md`) and brushes the Option-D ("custom xorriso scripts") path that decision rejected as the *builder*. **Rejected** — no product need for optical boot.
3. **`live-build` for the ISO only.** Reopens #197 for the ISO target specifically. Cost: the exact two-builder maintenance burden the 2026-06-16 decision chose mkosi to avoid. **Rejected.**

**The call (maintainer, 2026-06-17): Path 1.** The `.iso` requirement was loose terminology inherited from the live-build era, not a hard product need — the two real targets are a cloud VM image and a USB-flashable bare-metal image, both `Format=disk`. The cloud VM image is the **priority** target (`ENVIRONMENT.md`: the cloud image *is* the installed system — no installer/kiosk/LUKS-on-target); bare-metal USB + the kiosk installer follow. Recorded in `DECISIONS.md` 2026-06-17 and reflected in the #196 epic ordering.

## Known gaps / what was deliberately not done

- **No `dev/iso/` lane, no make target, no QEMU boot in this PR.** This PR records the finding + the decision; building the image lanes is the follow-up work in the re-ordered #196 epic.
- **Not boot-verified on the contributor host.** The contributor's box (Ubuntu 24.04, mkosi 26) cannot run `mkosi build` under `sudo` (#189, `PR_CAPBSET_DROP` EPERM); the finding here is a static inspection of mkosi's capabilities, which #189 does not affect. Not a blocker for the maintainer's environment (Ubuntu 20, where the medium lane builds and boots).
- The medium lane (`dev/test-qemu/`) is untouched.
- **A full "ISO → disk image" vocabulary sweep of `BUILD.md` is deferred** to the cloud-image implementation slice. This PR reconciles the load-bearing decision points (# 2 decision, # 6 artifacts, the locked-decisions summary, the artifact diagram); the softer "bundled in the ISO for offline first-boot" prose still uses "ISO" loosely for "install image" and gets cleaned when the lanes land.

## What's next (re-ordered #196 epic — cloud VM first)

1. **Cloud VM image (priority).** Stand up the lean hosted mkosi profile (`ENVIRONMENT.md` # How the profile is realized — no Avahi/Samba/NetworkManager/cryptsetup-TPM/mergerfs) + build-tagged slim cloud `host-agent` + `/etc/malmo/profile` marker; emit `qcow2`/`raw`; boot it directly in QEMU (no swtpm/LUKS) → brain up → first-run reachable. No installer/kiosk (the image *is* the installed system).
2. **Bare-metal USB (deprioritized, kept in plan).** The kiosk-installer arc (current #196 M2/M3): live-boot the `raw` from USB → kiosk installer partitions/LUKS/TPM-enrolls the internal disk → reboot. Boots the same `Format=disk` rootfs the medium lane already proves.
3. Reconcile the remaining `BUILD.md` "ISO" vocabulary when the lanes land (see Known gaps).
