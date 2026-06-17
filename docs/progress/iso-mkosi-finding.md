# mkosi emits no ISO — the live-session-ISO assumption is false

- **Status:** finding (no lane built; decision handed to the maintainer)
- **Date:** 2026-06-17
- **Specs touched:** `BUILD.md` (# 2, # 6, locked decisions — factual caveats), `DECISIONS.md` (2026-06-16 ISO entry — pointer), `NEXT.md` (new Tier-1 open item)
- **Issue:** #199 (M1 — bootable live-session ISO from mkosi), part of #196 (installable malmo epic)

#199 exists to de-risk the one piece the mkosi decision (`DECISIONS.md` 2026-06-16) flagged as unproven in-repo: that mkosi can emit a **bootable live-session ISO**, the way the medium lane's `Format=disk` proves it can emit a bootable disk image. The investigation found the assumption is not merely unproven — it is **false**: mkosi has no ISO output format at all. Per the maintainer's call this slice is **finding-only** — it surfaces the blocker and the forward paths "before M2/M3 build on it" (#199 "Done when"), and does not build a `dev/iso/` lane. The path choice is the maintainer's (#199 reassigned to onel).

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

## Paths forward (the maintainer's call)

1. **`Format=disk` raw image as the live/install medium — drop the literal `.iso`.** mkosi-native, zero new tooling, holds "single builder" exactly. The release artifact becomes a `.raw`/`.img` flashed to USB (what mkosi assumes, and what UEFI boots directly). Cost: rename the `.iso` artifact and the "ISO" vocabulary in `BUILD.md`; no optical-media / legacy-BIOS hybrid path. This is the lowest-friction landing and matches mkosi's own distribution model.
2. **Thin `xorriso` / `grub-mkrescue` post-step wrapping mkosi's rootfs into a true hybrid `.iso`.** Yields a literal isohybrid `.iso` (boots from CD *and* USB). Cost: introduces a non-mkosi tool, which reopens "mkosi is the **single** builder" (`BUILD.md` # 2 / `DECISIONS.md`) and brushes the Option-D ("custom xorriso scripts") path that decision rejected as the *builder* — using it as a post-step is narrower, but still needs a `DECISIONS.md` ratification.
3. **`live-build` for the ISO only.** Reopens #197 (the live-build-vs-mkosi decision) for the ISO target specifically. Cost: the exact two-builder maintenance burden the 2026-06-16 decision chose mkosi to avoid; almost certainly the wrong trade, but recorded for completeness.

Recommendation (advisory, not the call): **Path 1.** A USB-flashed `.raw` is what mkosi is designed to emit and what a modern UEFI appliance boots; the `.iso` requirement looks like loose terminology inherited from the live-build era rather than a hard product need. If a true `.iso` is genuinely required, Path 2 is the cheaper of the remaining two and keeps mkosi as the rootfs builder.

## Known gaps / what was deliberately not done

- **No `dev/iso/` lane, no make target, no QEMU boot.** Finding-only per the maintainer's call; building a `Format=disk` live lane (the natural next step under Path 1) is left to whoever owns the path decision.
- **Not boot-verified on this host regardless.** This box cannot run `mkosi build` under `sudo` at all (#189, `PR_CAPBSET_DROP` EPERM under mkosi 26 / Ubuntu 24.04) — the same blocker that has kept every M-slice's VM-boot acceptance pending. The finding here is a static inspection of mkosi's capabilities, which #189 does not affect.
- The medium lane (`dev/test-qemu/`) is untouched.

## What's next

1. **Maintainer (onel) picks the ISO packaging path** — Path 1 / 2 / 3 above. The NEXT.md Tier-1 item tracks it.
2. Whichever path: build the live-boot lane (`Format=disk` under Path 1, or the `xorriso` wrap under Path 2), boot it in QEMU with the baked control-plane images, capture the serial log — the original #199 "Done when," now unblocked of the false assumption.
3. Reconcile `BUILD.md` vocabulary (`.iso` → the chosen artifact) and add the `DECISIONS.md` entry if the path flips the "single builder" wording.
