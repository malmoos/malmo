# 0023 — LUKS root + first-boot TPM enrollment + unseal verification

- **Status:** done (all three stages verified `medium-lane test: PASS`, 2026-05-29)
- **Date:** 2026-05-28
- **Specs exercised:** `STORAGE.md` (# Encryption posture), `BOOT.md` (# The chain). **Spec edited:** `TESTING.md` (# Medium lane — the unseal scenario is realized as two QEMU processes, not an in-guest reboot; see below).
- **Builds on:** [qemu-medium-lane-scaffolding.md](qemu-medium-lane-scaffolding.md) (QEMU+swtpm scaffolding). 0021 booted a real kernel with a live TPM but a *plaintext* root; this slice adds the encryption + seal/unseal path that 0021 explicitly deferred.

Closes the "no LUKS, no TPM-sealed unseal" gap from 0021 # Known gaps. This is the first test of `STORAGE.md` # Encryption posture and `BOOT.md` # The chain (line 30, "LUKS unlock (root drive) via TPM2 PCR 7") against a real kernel + real (software) TPM.

## Goal

Boot a **LUKS-encrypted** root under QEMU+swtpm, prove the production-shaped flow end to end:

1. **Build:** root is LUKS+ext4, enrolled with a recovery-passphrase keyslot (mirrors `STORAGE.md`: one recovery passphrase enrolled as a keyslot at install). Initramfs can unlock via that keyslot on first boot (no TPM token exists yet, headless box can't prompt).
2. **First boot:** an enrollment step runs `systemd-cryptenroll --tpm2-device=auto --tpm2-pcrs=7 <root-dev>` — the exact command `STORAGE.md` # First-run flow step 4 specifies. Marks itself done (run-once).
3. **Second boot (power cycle — a second QEMU process sharing the disk + TPM state; see the two-boot-cycle section for why not an in-guest reboot):** initramfs auto-unlocks root via the TPM2 token against PCR 7 — no passphrase, unattended. This is the `BOOT.md` headless-boot requirement.
4. **Assert:** `cryptsetup luksDump` shows a `systemd-tpm2` token bound to PCR 7; root sits on a `crypt` device; a sentinel proves we reached the second boot without manual unlock.

## Decisions (settled 2026-05-28)

- **LUKS injection: mkosi-repart `Encrypt=key-file`.** Add `mkosi.repart/` partition definitions; mkosi produces a LUKS+ext4 root at build time, passphrase from a build-time keyfile. Stays inside the existing mkosi pipeline. Does not exercise the *installer's* `luksFormat` (that's `BUILD.md`/live-build's job) — but does exercise the first-boot enrollment + unseal path, which is this slice's actual target. Alternative (post-build cryptsetup wrapper) rejected as fragile and duplicative of mkosi.
- **Secure Boot off.** Plain OVMF. PCR 7 still carries a stable value (measured as SB-disabled), so seal/unseal across reboot works and proves the mechanism. SB-enabled OVMF (real PCR 7 policy + signed bootloader) is a fidelity gap noted below and likely a separate later slice — our test image isn't signed and would fail SB enforcement.

## Boot mechanism — why not gpt-auto (learned on the first encrypted boot)

The obvious way to unlock a LUKS root is to let `systemd-gpt-auto-generator` discover the root partition by GPT type GUID, notice it's LUKS, and set up `systemd-cryptsetup@`. **It doesn't work in this image** — confirmed empirically: the first encrypted boot reached `initrd-switch-root.service` with an *empty* `/sysroot` ("Failed to determine whether root path '/sysroot' contains an OS tree"), then dropped to emergency. gpt-auto produced no units because it runs at generator time (very early in initrd PID1), *before* udev coldplugs `virtio_blk` — which lives in the concatenated `KernelModulesInitrd`, not the base initrd. With no block devices enumerated, gpt-auto finds no disk and emits nothing, and unlike a `.device`-dependent mount unit it does not retry. This is the encrypted-root version of the same timing problem 0021 hit (and worked around for the *plaintext* root with an explicit `root=/dev/vda2`, which can't apply here — that path points at ciphertext).

The fix: **`rd.luks.uuid=<UUID>` on the kernel cmdline.** `systemd-cryptsetup-generator` turns that into a `systemd-cryptsetup@luks\x2d<uuid>.service` with a dependency on `dev-disk-by\x2duuid-<uuid>.device`, which *waits* for udev to bring up the device — the encrypted analogue of 0021's explicit-`root=` fix. The wrinkle is needing the LUKS UUID at cmdline-bake time:

- The UUID is **derived** by systemd-repart, not equal to the partition UUID: `luks_uuid = v4(first16(HMAC-SHA256(key=partition_uuid, msg="luks-uuid")))` (the `luks-uuid` / `derive_uuid` strings are right there in the repart binary).
- So we **pin the root partition UUID** (`mkosi.repart/10-root.conf` `UUID=`), **compute** the derived LUKS UUID in `bootstrap.sh` (Python HMAC), and bake `rd.luks.uuid=…` into `/etc/kernel/cmdline` (mkosi prepends it to `KernelCommandLine` when building the UKI).
- A **post-build check** (`losetup -P` + `blkid`) verifies the computed UUID against the real image header — a wrong formula fails at build with both values printed, never as a silent 90s-timeout unlock failure at boot.

mkosi's own `process_crypttab` reads the *build host's* `/etc/crypttab`, not the image's, so a crypttab-in-initrd approach was a dead end. `rd.luks.uuid` is the systemd-native, deterministic path.

### Second encrypted boot: device-mapper module never loads (learned on the `rd.luks.uuid` boot)

With `rd.luks.uuid` baked in, `systemd-cryptsetup-generator` *did* emit a unit that waits for the device — progress past gpt-auto. But the unlock still failed, this time fast (emergency at ~1.4s, not a 90s device-wait timeout), which means `systemd-cryptsetup` ran and *errored* rather than waited. The serial log (now captured caller-readable; see harness change below) showed the real cause:

```
systemd-cryptsetup[…]: Cannot initialize device-mapper. Is dm_mod kernel module loaded?
systemd-cryptsetup[…]: Failed to activate with specified passphrase: Operation not supported
```

Two findings, one fatal:

- **The passphrase credential works.** "Failed to activate with *specified passphrase*" means the `cryptsetup.passphrase` SMBIOS credential reached systemd-cryptsetup. (bookworm's systemd 252 logs `Unknown key 'ImportCredential'` for mkosi's initrd `credential.conf` drop-in — `ImportCredential=` is 254+ — but the same drop-in's `LoadCredential=cryptsetup.passphrase` compat line still delivers it. Harmless warning, no action.)
- **`dm_mod`/`dm_crypt` are never *loaded*** — `libdevmapper` can't initialize, so the unlock dies regardless of having the right key. This turned out to have *two* independent causes (one masking the other), found across two boots.

**Fix part 1 — force-load via `modules-load.d` (necessary, not sufficient).** A `dev/test-qemu/mkosi.initrd.conf/` directory (mkosi parses it as extra config for the *default initrd* — `finalize_default_initrd`, `mkosi.1` # `mkosi.initrd.conf`) carries `mkosi.extra/usr/lib/modules-load.d/molma-luks.conf` listing `dm_mod` + `dm_crypt`. `systemd-modules-load.service` (`Before=sysinit.target`, ahead of any cryptsetup activation) modprobes them — what Debian's initramfs-tools cryptsetup hook and dracut's `rd.driver.pre=` do, instead of relying on the fragile `/dev/mapper/control` devname-autoload chain (which never completed before `systemd-cryptsetup@` ran). The reboot after this change moved the error from "cryptsetup never tried the modules" to a sharper, decisive one: `systemd-modules-load: Failed to insert module 'dm_mod': No such file or directory`.

**Fix part 2 — the `.ko` files were never in the initrd at all (the real root cause).** "No such file" was literal: `modprobe` resolved `dm_mod` to `kernel/drivers/md/dm-mod.ko` via `modules.dep` (depmod metadata covers the *whole* tree), then ENOENT'd because the file itself was excluded. The reason: every `KernelModulesInitrdInclude=` value is wrapped as `re:<value>` (`parse_kernel_module_filter_regexp`) and matched with `re.search` against the module's **hyphenated file path** (`…/dm-mod.ko`) — with *no* underscore normalization for the regex form (that only applies to the glob-form `KernelInitrdModules`). So `dm_mod`/`dm_crypt` matched nothing, while `ext4`, `virtio.*`, `aes.*`, `xts` matched (no `_`, or `.` papering over the separator). Extracting the modules-initrd (frame 1 of the UKI `.initrd`) confirmed `dm-mod.ko`/`dm-crypt.ko` absent while `ext4.ko`/`xts.ko`/`aesni-intel.ko` present. Fix: `KernelModulesInitrdInclude=dm[-_]crypt` / `dm[-_]mod` — a char class that matches the on-disk hyphen and stays robust to either form. (Lesson: a grep for `dm-mod.ko` in the raw cpio is *not* proof the file is present — `modules.dep` mentions every module by path. Verify file membership by extraction.)

With both module fixes in, the next boot got all the way *into* the unlock: `dm_crypt` inserted, `device-mapper: ioctl … initialised`, cryptsetup read the header and `Set cipher aes, mode xts-plain64`. It then failed at the final step — **`Not enough available memory to open a keyslot` / `Failed to activate with specified passphrase: Cannot allocate memory`**.

**Fix part 3 — give the guest realistic RAM.** This is the LUKS2 **Argon2id** memory-hard KDF. `systemd-repart` enrolled the recovery keyslot on the big-RAM *build host*, so cryptsetup calibrated the memory cost near libcryptsetup's ~1 GiB default cap. Unlocking re-allocates that buffer, and the 1 GiB guest — minus the initramfs unpacked into RAM and the kernel — couldn't. Bumped the QEMU guest to `-m 2G` in `run-medium-tests.sh` (harness-only; the built image is untouched, so no rebuild). 2 GiB is also more representative of a real molma box than 1 GiB. **Fidelity note:** in production the recovery passphrase is enrolled *on the box* at install (`STORAGE.md` # First-run flow), so Argon2 is calibrated to that box's RAM and the enroll-host == unlock-host — the mismatch is a test-lane artifact of enrolling at build time, not a product issue.

Also this round: `run-medium-tests.sh` now copies the serial log to `.dev/qemu/last-serial.log` (caller-owned) and greps the unlock path on failure — the previous `tail -50` cut off the load-bearing `systemd-cryptsetup` error above the dependency-failure cascade. And `medium-assertions.sh` gained a check that `/` is on a live `dm-crypt`/LUKS mapping, so a silent plaintext fallback can't pass as a green Stage 1.

## The two-boot cycle — new harness capability (built as two QEMU processes, not an in-guest reboot)

0021's driver boots once and powers off (`-no-reboot`, disk `snapshot=on`). 0023 needs **two boots of the same disk within one run** so first-boot enrollment persists into the second boot. The plan above this section assumed an *in-guest* `systemctl reboot` inside one long-lived QEMU process. **That's not what shipped** — and the reason is the whole point of the test: the unattended-unseal proof requires the second boot to get *no* recovery-passphrase credential, but the passphrase is delivered as an SMBIOS type-11 credential fixed at QEMU launch and can't be withheld partway through a single process. So the two boots have to be **two sequential QEMU processes** against shared persistent state:

- **One writable per-run disk overlay** (`qemu-img create -f qcow2 -F raw -b <golden>`) shared by both QEMU processes, so the keyslot the first boot enrolls into the on-disk LUKS header is read back by the second — while the golden raw stays clean. (`snapshot=on` would reset between processes; a plain overlay persists.)
- **One OVMF pflash vars copy** shared across both, so firmware measurements into PCR 7 reproduce.
- **swtpm relaunched per boot against one persistent `--tpmstate` dir.** swtpm exits when its QEMU client disconnects, so it can't span both processes anyway — and relaunching it is the faithful model: a TPM *power cycle*. Volatile PCRs reset and are re-measured to identical values by the identical firmware; the persistent SRK + NVRAM live in the state dir, so the PCR-7-sealed keyslot enrolled in boot 1 unseals in boot 2.
- Driver sequence (`run_boot <phase>`, called twice): launch swtpm → launch QEMU → wait for SSH → run `medium-assertions.sh <phase>` → scp the verdict back → `systemctl poweroff` → reap QEMU + swtpm. Boot 1 passes `-smbios type=11,…cryptsetup.passphrase=…` (enroll path); boot 2 passes nothing (unseal path).

## What was done

- **Stage 1 — encrypted root boots: DONE (verified `medium-lane test: PASS`, 2026-05-29).** mkosi-repart produces a LUKS2+ext4 root (recovery passphrase keyslot 0 from `mkosi.passphrase`). The initrd unlocks it via `rd.luks.uuid=` + the `cryptsetup.passphrase` SMBIOS credential, switch-root lands on `/dev/mapper/luks-…`, and the VM reaches multi-user with sshd up. Getting there took four real-boot iterations, each surfacing a bug only visible on hardware (see the boot-mechanism section above): (1) gpt-auto can't discover the encrypted root pre-coldplug → `rd.luks.uuid=` with a repart-derived LUKS UUID; (2) `dm_mod`/`dm_crypt` never loaded → `modules-load.d` in the initrd via `mkosi.initrd.conf/`; (3) the `.ko` files weren't even included → `dm[-_]mod`/`dm[-_]crypt` include patterns (underscore-vs-hyphen `re.search`); (4) Argon2id keyslot OOM in a 1 GiB guest → `-m 2G`. Assertions confirm `/` is a live LUKS mapping, system is running, storage-verify ran, TPM is readable.
- **Stage 2 — first-boot TPM enrollment: DONE (verified `medium-lane test: PASS`, 2026-05-29).**
  - `dev/test-qemu/first-boot-tpm-enroll.sh` (staged to `/usr/lib/molma/`): resolves the LUKS backing partition from the mounted root via `cryptsetup status`, then runs the exact `STORAGE.md` # First-run flow command — `systemd-cryptenroll --unlock-key-file=/etc/molma/secrets/luks-recovery.key --tpm2-device=auto --tpm2-pcrs=7 <backing>`. Writes the run-once marker `/var/lib/molma/.luks-tpm-enrolled` **only on a successful enroll**, so a failed run re-tries next boot.
  - `dev/test-qemu/molma-tpm-enroll.service`: `Type=oneshot`, `ConditionPathExists=!<marker>` (run-once), `After=local-fs.target`, `Before=molma-storage-ready.target`, `WantedBy=molma-storage-ready.target`. "Not a gate" is true for *success* — it's pulled by `Wants=`, so a failed enroll converges the system to `degraded`, not a boot failure. But `Before=` is still an *ordering* gate: the target's start job blocks on this oneshot finishing, so a hung enroll stalls `molma-storage-ready.target` (and everything after it) for up to `TimeoutStartSec=120s`. Fine for the test lane; the real host-agent first-run caller (known-gaps #3 below) must stay off the boot-critical ordering chain — run async / after the milestone, not `Before=` it.
  - Wiring: `bootstrap.sh` stages both files; `mkosi.postinst.chroot` chmods the script and drops the `.wants` symlink by hand (can't `systemctl enable` in a chroot — same idiom as the storage-ready target).
  - `first-boot` assertion: waits for the marker (fails fast on `is-failed`, since sshd has no ordering vs. the enroll unit), then parses `cryptsetup luksDump --dump-json-metadata` with grep/sed only (no jq/python3 in the guest) to assert a `systemd-tpm2` token bound to PCR 7. Confirmed on the real boot: the enroll ran and the PCR-7 token landed in the header.
- **Stage 3 — reboot + unattended unseal: DONE (verified `medium-lane test: PASS`, 2026-05-29).** `run-medium-tests.sh` refactored into the two-process `run_boot <phase>` cycle (see the section above). Boot 2 deliberately supplies no passphrase credential and the console is serial-only, so the recovery keyslot is unreachable — **reaching multi-user is itself the unattended PCR-7-unseal proof.** The serial log confirms the mechanism: on boot 2 the enroll unit is `skipped because of an unmet condition check (ConditionPathExists=!…)` (marker present from boot 1), yet root unlocked and the system came up. The `second-boot` assertion additionally confirms the token + PCR-7 binding survived the power cycle.
- **Two harness bugs found only on the real run (fixed, no rebuild — both harness-side):**
  - `medium-assertions.sh` sampled `systemctl is-system-running` *once* at the top → spurious `starting` failure: on first boot sshd races ahead of the still-running enroll, which is part of the boot transaction (`WantedBy=molma-storage-ready.target`), and the driver's follow-up poweroff then SIGTERM'd the enroll mid-Argon2 (looked like a failure, was an interruption). Fixed by polling for `running`/`degraded` with a timeout instead of one sample.
  - `run-medium-tests.sh` launched swtpm *once* for both boots → boot 2 died at QEMU launch (`Failed to connect socket …/swtpm/sock`): swtpm exits when boot 1's QEMU disconnects. Fixed by relaunching swtpm per boot against the shared `--tpmstate` dir (the TPM power-cycle model above).

## Staging

Built in verifiable stages against the real mkosi/QEMU rather than one speculative commit (0021 surfaced 8 bugs only visible on a real run):

- **Stage 1 — encrypted image boots at all. ✅ DONE** (`mkosi.repart/` + passphrase; VM reaches multi-user unlocking via the recovery keyslot). mkosi's crypttab/UUID/initrd behavior learned the hard way — captured above.
- **Stage 2 — first-boot TPM enrollment. ✅ DONE** (run-once `molma-tpm-enroll.service` runs `systemd-cryptenroll --tpm2-pcrs=7`; `first-boot` assertion confirms a `systemd-tpm2` PCR-7 token in the LUKS header).
- **Stage 3 — reboot + unseal. ✅ DONE** (two-process boot cycle; boot 2 gets no passphrase and still reaches multi-user, proving unattended PCR-7 unseal; `second-boot` assertion confirms the token survived the power cycle).

## Known gaps & deviations (running list)

- **Secure Boot off** — see decision above. PCR 7 reflects SB-disabled state; prod assumes SB on (`STORAGE.md`). Fidelity gap, tracked for a later slice.
- **Recovery passphrase is a build-time keyfile, not the installer-generated secret.** The test exercises enrollment + unseal, not the installer's secret-generation/storage (`/etc/molma/secrets/luks-recovery.key`) — that's `BUILD.md`/first-run territory.
- **Enrollment trigger is a test-lane unit, not host-agent first-run.** The `systemd-cryptenroll` *command* matches production; its real caller (host-agent first-run) isn't built yet. Replace the test unit with the real caller when that lands — and keep it **off the boot-critical ordering chain**: the test unit's `Before=molma-storage-ready.target` means a hung enroll stalls the milestone for up to 120s (see the unit comment), which is fine for a one-shot test but wrong for production boot timing. Run the real caller async / after the milestone.
- **Enroll is append-only, not idempotent — the real caller should `--wipe-slot=tpm2`.** The test relies solely on the run-once marker to avoid duplicate keyslots, but that has a gap: if the enroll *succeeds* and the marker write then fails (or PCRs drift and a re-enroll is wanted), the next run's bare `systemd-cryptenroll --tpm2-device=auto …` *appends* a second TPM2 keyslot rather than replacing the first. The production idiom is `systemd-cryptenroll --wipe-slot=tpm2 --tpm2-device=auto …` so re-enrollment is replace-not-append. Not added to the test unit (the marker covers the test, and baking it in would force a rebuild for a path the test doesn't exercise) — but the real host-agent caller must use it.

## What's next

_(carried from 0021's queue, minus this slice)_

- Data-drive second-disk scaffold (second virtio disk, enrollment-marker + device-backing canary path).
- Failure-injection harness (`dev/test-qemu/scenarios/`).
- Brain + host-agent in the VM.
- CI workflow running all lanes per PR.
