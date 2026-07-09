# hosted-cx-bios-boot — the hosted image booted UEFI-only, so it bricked on Hetzner CX (Intel)

- **Status:** done (config validated locally; full boot proof + real-box acceptance are CI/live, see Known gaps)
- **Date:** 2026-07-09
- **Specs touched:** `ENVIRONMENT.md` (# Boot — new dual-firmware boot bullet); `docs/dev/hosted-boot-proof.md` (the lane now has a legacy-BIOS scenario)

Closes **#277**. A box provisioned from the hosted cloud image did not boot on Hetzner Cloud **CX (Intel)** shared-vCPU types: the console printed SeaBIOS's `Booting from Hard Disk` and then hung before any kernel output — dark to ICMP and every TCP port. The **same image booted normally on CPX (AMD)** types. It was reproducible with **no seed** on both, so this was a boot-level/platform issue, not first-boot provisioning.

## Root cause

The image was **UEFI-only**. `dev/cloud/mkosi.conf` set `Bootloader=systemd-boot` (systemd-boot is a UEFI-only bootloader) with an ESP carrying `systemd-bootx64.efi`, and `mkosi.repart/` defined only an ESP + ext4 root — **no BIOS Boot Partition and no MBR boot code**. That boots fine anywhere the firmware is UEFI, but the Hetzner **CX (Intel)** line boots the VM under **legacy BIOS (SeaBIOS)** — the literal `Booting from Hard Disk` is SeaBIOS's boot-from-disk message. SeaBIOS reads the protective MBR, finds no boot code (there is none on a UEFI-only GPT image), and hangs. CPX (AMD) presents UEFI firmware, finds the ESP, and boots. So the outcome was purely a function of which firmware the provider's server type presents, and the image only satisfied one of the two.

Why it shipped undetected: the QEMU boot-proof (`dev/cloud/run-cloud-tests.sh`) booted **only under OVMF (UEFI)** — every scenario attached `-drive if=pflash,…OVMF_CODE…`. The legacy-BIOS boot path was never exercised, so the lane could not have caught this (the same shape of gap as the growfs-never-ran miss in [hosted-grow-root-fs.md](hosted-grow-root-fs.md)).

## What was done

The fix makes the image **dual-firmware** — bootable under both UEFI and legacy BIOS — the same posture Debian/Ubuntu cloud images ship, so a box boots regardless of which firmware a given (or future) provider server type presents. It is deliberately the **minimal, surgical** form: the proven UEFI/systemd-boot path is left completely untouched and BIOS boot is added alongside it, using mkosi's independent `Bootloader=` (EFI) and `BiosBootloader=` (BIOS) knobs.

- **`dev/cloud/mkosi.conf`** — keep `Bootloader=systemd-boot`; **add `BiosBootloader=grub`**. mkosi treats the two independently: UEFI stays systemd-boot, and for BIOS it builds a GRUB `core.img` (`grub-mkimage --format i386-pc`), embeds it in the BIOS Boot Partition + writes `boot.img` to the protective MBR (`grub-bios-setup`), and appends a `grub_platform=="pc"` `menuentry` to the ESP `grub.cfg` that loads the **same** kernel + initrds + `root=PARTUUID=…` cmdline. No UKIs to chainload on the BIOS side — grub boots the plain kernel directly off the vfat ESP (where `CopyFiles=/boot:/` already puts it), so no ext4 grub module is needed.
- **`dev/cloud/mkosi.conf` `Packages=`** — add `grub-common` (ships `grub-mkimage`) and `grub-pc-bin` (ships the `i386-pc` modules **and** Debian's `/usr/lib/grub/i386-pc/grub-bios-setup`, exactly where mkosi looks). Deliberately **no** `grub-efi-*` — the UEFI path stays systemd-boot, so nothing about it changes. The build-time `grub-mkimage`/`grub-bios-setup` binaries come from mkosi's default tools tree, which already carries them; these two image packages provide only the modules mkosi reads from the image root.
- **`dev/cloud/mkosi.repart/05-bios.conf`** (new) — a 1 MiB BIOS Boot Partition, `Type=21686148-6449-6e6f-744e-656564454649` (the well-known GRUB BIOS-boot GPT type; note `Type=bios` is **not** a systemd-repart alias — the raw GUID is required), no `Format=`/`CopyFiles=` (it holds raw `core.img`, not a filesystem). Sorts between `00-esp` and `10-root`. The ESP stays 512M — mkosi bumps the ESP to 1G only for grub-**EFI** images (which duplicate kernels onto the ESP because they can't use UKIs); here EFI is systemd-boot, the kernel sits on the ESP once, and 512M is unchanged and ample.
- **`dev/cloud/expected-packages.txt`** — the lean guard is an **exact** manifest match, so the two grub packages plus their transitive libs (`dmsetup`, `gettext-base`, `libbrotli1`, `libdevmapper1.02.1`, `libefiboot1t64`, `libefivar1t64`, `libfreetype6`, `libfuse3-4`, `libpng16-16t64`) are added, with a header note explaining why grub is now in the lean set. No systemd-boot entries were removed (UEFI still uses it).
- **`dev/cloud/run-cloud-tests.sh`** — the boot-proof gains a **legacy-BIOS scenario** (`bios`): `run_boot` now attaches the OVMF pflash only for `firmware=uefi`, so a `bios` boot uses QEMU's built-in SeaBIOS — the legacy-BIOS firmware a CX box presents. It re-boots the same image under SeaBIOS on its **own fresh overlay** (so it never perturbs the UEFI provisioning sequence) and reuses the un-seeded assertion (control plane up, SSO gate armed) as the "did it boot and come up" proof. This is the check that would have caught #277; it is added to the default full run and to the CI publish gate.
- **`.github/workflows/ci-cloud-image.yml`** — the seeded-boot gate now runs `MALMO_CLOUD_BOOTS="unseeded seeded bios"`, so a build that can't boot under legacy BIOS fails **before** publish.

The test image (`dev/cloud/test/`) `Include=..`'s the production config and defines no `[Content]`/repart of its own, so it inherits `BiosBootloader=grub`, the grub packages, and the BIOS Boot Partition — the boot-proof therefore tests the fixed image, not an unfixed one.

## Verification

- **Config validated locally** with `mkosi 26` (the exact version CI pins) via `mkosi -C dev/cloud summary`: the main image resolves to `Bootable: enabled`, `Bootloader: systemd-boot`, **`BIOS Bootloader: grub`**, with `grub-common` + `grub-pc-bin` in Packages and `systemd-boot*` retained; no parse warnings. `Bootable=yes` makes any missing BIOS prerequisite (modules, partition, ESP, root, binaries) a **hard build error**, so a misconfiguration fails the build loudly rather than silently shipping a UEFI-only image.
- The grub dependency closure was computed against a real `debian:trixie` apt resolver (recommends off, matching `WithRecommends=no`) to pre-populate the lean lockfile exactly.
- `bash -n` clean on `run-cloud-tests.sh`.

## Known gaps & deviations

- **Not built/booted locally.** Per `CLAUDE.md`, the cloud image is a CI job (mkosi build needs root + `/dev/kvm`; local builds are fragile). The full proof — image builds, lean check passes, the UEFI boots stay green, and the **new SeaBIOS boot passes** — runs in `CI / Cloud image` (`-f publish=false`). If the first build's lean check reports a manifest delta (the trixie apt closure differing from the mkosi image by a package), it prints the exact UNEXPECTED/MISSING names to reconcile into `expected-packages.txt`.
- **The `systemd-boot`+`grub` pairing has no upstream test.** mkosi's docs name `Bootloader=grub`+`BiosBootloader=grub` (grub for both) verbatim; the code paths for `systemd-boot`+`BiosBootloader=grub` (Option B, chosen here to leave the UEFI path untouched) are correct-by-tracing but not covered by an upstream test. If CI shows the pairing misbehaving, the fallback is Option A — `Bootloader=grub` for both firmwares (adds `grub-efi-amd64-bin`), the docs-blessed combination — at the cost of replacing systemd-boot on the UEFI path.
- **Real-CX acceptance is still live-only.** The QEMU SeaBIOS boot proves the image is legacy-BIOS-bootable in general; a real Hetzner **CX (Intel)** provision is the final acceptance that this specific platform now boots (mirroring the CL6 live-run deferral of the other hosted-image entries). Not run here.

## What's next

- Run `CI / Cloud image` on this branch (`gh workflow run "CI / Cloud image" --ref fix/277-hetzner-cx-boot -f publish=false`); reconcile any lean-check delta; confirm all four boots (unseeded/seeded UEFI + the BIOS smoke) are green.
- A real Hetzner **CX (Intel)** provision from a published image, as the live acceptance for the platform this issue named.
- **Possibly out of scope but related:** the appliance install ISO (`dev/test-qemu` and the real installer) is likewise `Bootloader=systemd-boot` only. BYO-x86 "old laptop in the pantry" machines that boot legacy BIOS would hit the same wall. Not touched here (this issue is hosted-only); worth a separate issue if the appliance targets legacy-BIOS hardware.
