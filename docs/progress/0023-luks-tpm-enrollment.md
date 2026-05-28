# 0023 — LUKS root + first-boot TPM enrollment + unseal verification

- **Status:** in progress
- **Date:** 2026-05-28
- **Specs touched:** `STORAGE.md`, `BOOT.md`, `TESTING.md`
- **Builds on:** [0021](0021-qemu-medium-lane-scaffolding.md) (QEMU+swtpm scaffolding). 0021 booted a real kernel with a live TPM but a *plaintext* root; this slice adds the encryption + seal/unseal path that 0021 explicitly deferred.

Closes the "no LUKS, no TPM-sealed unseal" gap from 0021 # Known gaps. This is the first test of `STORAGE.md` # Encryption posture and `BOOT.md` # The chain (line 30, "LUKS unlock (root drive) via TPM2 PCR 7") against a real kernel + real (software) TPM.

## Goal

Boot a **LUKS-encrypted** root under QEMU+swtpm, prove the production-shaped flow end to end:

1. **Build:** root is LUKS+ext4, enrolled with a recovery-passphrase keyslot (mirrors `STORAGE.md`: one recovery passphrase enrolled as a keyslot at install). Initramfs can unlock via that keyslot on first boot (no TPM token exists yet, headless box can't prompt).
2. **First boot:** an enrollment step runs `systemd-cryptenroll --tpm2-device=auto --tpm2-pcrs=7 <root-dev>` — the exact command `STORAGE.md` # First-run flow step 4 specifies. Marks itself done (run-once).
3. **Second boot (in-test reboot):** initramfs auto-unlocks root via the TPM2 token against PCR 7 — no passphrase, unattended. This is the `BOOT.md` headless-boot requirement.
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

## The in-test reboot — new harness capability

0021's driver boots once and powers off (`-no-reboot`, disk `snapshot=on`). 0023 needs **two boots of the same disk within one run** so first-boot enrollment persists into the second boot:

- Drop `-no-reboot`; switch the disk off `snapshot=on` to a **writable per-run overlay** (`qemu-img create -b … -F raw` or a per-run `cp` of the raw), so enrollment writes persist but the golden image stays clean.
- swtpm + OVMF pflash vars stay alive for the whole QEMU process, so PCRs and LUKS-header state persist naturally across an in-guest `systemctl reboot`.
- Driver sequence: boot → SSH → first-boot assertions → `systemctl reboot` → wait for SSH to drop and return → second-boot assertions → poweroff.

## What was done

_(filled in as stages land — see "Staging" below)_

## Staging

Built in verifiable stages against the real mkosi/QEMU rather than one speculative commit (0021 surfaced 8 bugs only visible on a real run):

- **Stage 1 — encrypted image boots at all.** `mkosi.repart/` + passphrase; confirm mkosi wires up encrypted-root boot and the VM reaches multi-user unlocking via the recovery keyslot. Learn mkosi's actual crypttab/UUID/initrd behavior here.
- **Stage 2 — first-boot TPM enrollment.** Add the enrollment unit; assert a TPM2 token lands in the LUKS header on first boot.
- **Stage 3 — reboot + unseal.** Harness reboot cycle; assert second boot unlocks via TPM unattended and the token is bound to PCR 7.

## Known gaps & deviations (running list)

- **Secure Boot off** — see decision above. PCR 7 reflects SB-disabled state; prod assumes SB on (`STORAGE.md`). Fidelity gap, tracked for a later slice.
- **Recovery passphrase is a build-time keyfile, not the installer-generated secret.** The test exercises enrollment + unseal, not the installer's secret-generation/storage (`/etc/malmo/secrets/luks-recovery.key`) — that's `BUILD.md`/first-run territory.
- **Enrollment trigger is a test-lane unit, not host-agent first-run.** The `systemd-cryptenroll` *command* matches production; its real caller (host-agent first-run) isn't built yet. Replace the test unit with the real caller when that lands.

## What's next

_(carried from 0021's queue, minus this slice)_

- Data-drive second-disk scaffold (second virtio disk, enrollment-marker + device-backing canary path).
- Failure-injection harness (`dev/test-qemu/scenarios/`).
- Brain + host-agent in the VM.
- CI workflow running all lanes per PR.
