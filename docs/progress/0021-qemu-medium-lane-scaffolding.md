# 0021 — QEMU + swtpm medium-lane scaffolding

- **Status:** in progress — code written, end-to-end run pending on a non-20.04 host
- **Date:** 2026-05-27
- **Specs touched:** `TESTING.md`, `BOOT.md`, `STORAGE.md`
- **Verified on:** none yet. Ubuntu 20.04 dev box (Python 3.8, no swtpm in apt) couldn't satisfy the dep matrix without ~30 min of jammy backporting + deadsnakes. Code paused for a fresh run on 22.04+ / Debian 12+. The lane logic is structurally complete and reviewed against the same shape as slice 0020's nspawn driver.

Closes the "no real kernel / no real TPM" gap from `TESTING.md` # Medium lane. Slice 0020's fast lane proves unit shape inside an nspawn namespace; this slice boots a real Linux kernel under QEMU with a software TPM and runs our reporter in real systemd userspace. SSH-driven assertions read back the verdict.

**Scope: scaffolding only.** The image is *not* LUKS-encrypted in this slice. LUKS root + first-boot TPM enrollment + second-boot unseal verification is slice 0022 layered on top. The reasoning is below in "Scope split with 0022."

## What was done

### `dev/test-qemu/mkosi.conf` — image build config

bookworm + `systemd-boot` + `tpm2-tools` + `openssh-server` + the packages needed for a small bootable disk. `ExtraTrees=mkosi.extra/` bakes our `dist/systemd/` units at `/etc/systemd/system/`, the `malmo-storage-verify` binary at `/usr/lib/malmo/`, and the assertion script at `/usr/local/bin/`. `PostInstallationScripts=mkosi.postinst` runs in mkosi's chroot to enable units and set tmpfiles.

mkosi was chosen for the test-lane image build per the spec call (`live-build` remains the v1 *production* ISO tool — different concern). `NEXT.md` # Tier 4 # Testing has the open entry "live-build vs mkosi revisit weighted by test-story" which this slice partly informs.

### `dev/test-qemu/mkosi.postinst`

Runs inside the chroot. Adds a `.wants` symlink for `malmo-storage-ready.target` under `multi-user.target.wants/`, writes `/etc/tmpfiles.d/malmo.conf` for `/run/malmo/health/`, ensures the verify binary is executable, and disables `systemd-networkd-wait-online` (would otherwise eat boot time in a slirp-networked VM with no DHCP lease guarantees).

### `dev/test-qemu/bootstrap.sh` — preflight + build

Probes for `mkosi`, `swtpm`, `qemu-system-x86_64`, `ssh`/`scp`, and a usable OVMF firmware path. Missing tools emit a clear install pointer rather than silently failing or auto-`apt install`ing system packages (host changes belong to the user). Versions: mkosi must be v22+ (Ubuntu 20.04 ships v9; `pipx install mkosi` is the supported escape).

Builds `malmo-storage-verify` statically as the invoking user, generates a per-image ed25519 SSH keypair into `.dev/qemu/ssh-key`, stages `mkosi.extra/`, then invokes `mkosi build`. Idempotent via a versioned canary file (same idiom as 0020's bootstrap).

`host-agent.service` is staged but never enabled — its `Requires=docker.service` would fail at boot in this minimal image. `/usr/lib/malmo/host-agent-real` is symlinked to `/bin/true` so the unit *loads* (matching the fast-lane stub posture).

### `dev/test-qemu/medium-assertions.sh` — in-VM checks

Baked into the image at `/usr/local/bin/`. Four assertion groups:

1. `systemctl is-system-running` returns `running` or `degraded` (not `starting` / `maintenance`).
2. `malmo-storage-verify.service` reached `active`.
3. `/run/malmo/health/storage.json` exists, has `checked_at` + `findings` keys, and `findings` is empty (Level-0 VM has no data drive).
4. **TPM plumbing is live**: `/dev/tpmrm0` is a character device and `tpm2_pcrread sha256:7` succeeds and returns a parseable PCR reading. This proves the swtpm → QEMU `-tpmdev tpm-crb` → kernel TPM2 driver → userspace `tpm2-tools` chain works end-to-end. The PCR *value* isn't asserted — slice 0022 will use it for sealing.

Same posture as 0020's `boot-assertions.sh`: `set -uo pipefail` (deliberately no `-e`; every check is `... || fail "..."`), STARTED sentinel pre-write, EXIT trap upgrades sentinel to FAIL on abort, verdict written to `/var/lib/malmo-medium-result`.

### `dev/test-qemu/run-medium-tests.sh` — host driver

Sequence:

1. Bootstrap-if-absent.
2. Resolve OVMF firmware path (varies across Debian/Ubuntu/Arch).
3. Allocate a free TCP port for SSH port-forwarding (Python `socket.bind(0)` trick; `ss -ltn` fallback).
4. Launch `swtpm` in socket mode with a per-run tempdir for TPM state. `--daemon` + pidfile so we can reap it on cleanup.
5. Launch QEMU with `q35,accel=kvm` (TCG fallback), 1 GB RAM, 2 vCPUs, `-nographic`, serial → logfile, virtio disk in `snapshot=on` mode (image stays clean), OVMF firmware, `-tpmdev tpm-crb` wired to the swtpm socket, slirp networking with SSH port forward, `-no-reboot`.
6. Poll SSH availability up to 90s (boot includes UEFI firmware init + systemd cold start — typically 15-30s on KVM).
7. SSH in, run `/usr/local/bin/medium-assertions.sh`. `scp` the verdict file back to the host.
8. `ssh root@… systemctl poweroff`, bounded wait for QEMU exit, SIGKILL fallback.
9. Read verdict, print PASS/FAIL, exit accordingly.

Per-run state in `mktemp -d` and trap-cleaned on success. Kept on failure for debugging (serial log path printed). The pattern of polling for a verdict and SIGKILL-on-stuck-shutdown is borrowed directly from 0020 — graceful guest shutdown timing is unreliable enough that the host driver shouldn't trust it.

### `Makefile` — `test-medium-qemu`

`sudo -E ./dev/test-qemu/run-medium-tests.sh`. Wired into `.PHONY` and `make help`.

## How it maps to the specs

- `TESTING.md` # Medium lane — "Real boot ordering across the kernel-userspace boundary" is what this slice catches today: real initramfs → systemd → multi-user.target → our reporter. The other matrix items (initramfs LUKS unlock, TPM2 unseal, disk hotplug, failure injection) ride on this scaffolding in subsequent slices.
- `BOOT.md` # The storage-ready target — exercised end-to-end in a real userspace for the first time (0019/0020 exercised the *units* in static and namespace contexts; this is the first kernel boot that activates the target).
- `STORAGE.md` # Encryption posture — only the *TPM plumbing* is exercised here (PCR 7 is readable via `tpm2_pcrread`). The actual `--tpm2-pcrs=7` enrollment + LUKS unseal posture is slice 0022.

## Scope split with 0022

The original product call was "TPM2 unseal in-scope." On implementation, the full path (build-time LUKS + first-boot enrollment + initramfs unseal + second-boot verification) materialized into a much larger slice than the medium-lane *scaffolding*. Splitting along the natural seam:

- **0021 (this slice)** — QEMU runner, swtpm wiring, SSH-driven assertion path, image-build pipeline, real-kernel boot of our reporter. TPM device exposed and exercised (`tpm2_pcrread`) but no sealing.
- **0022 (next)** — LUKS root + first-boot enrollment service + initramfs reconfiguration on first boot + second-boot TPM unseal + assertion that the unseal token is bound to PCR 7.

The split is "ship the lane, then ship the TPM-unseal test on top." Same total work, less risk per slice. The dev-speed/prod-fidelity tradeoff is preserved because 0022 still uses the prod first-run flow per `STORAGE.md` — we're just sequencing it.

## Host-dep dance (Ubuntu 20.04)

The lane needs `mkosi >=22`, `swtpm`, `qemu-system-x86_64`, and OVMF. On Ubuntu 22.04+ / Debian 12+ this is plain apt. On Ubuntu 20.04 (the dev box used for this slice) three of the four deps fight back. Documented here so future-you doesn't relearn it:

- **swtpm: not packaged on 20.04 at all.** Landed in jammy (22.04). Pull jammy `.deb`s manually:
  ```bash
  mkdir -p /tmp/swtpm-debs && cd /tmp/swtpm-debs
  wget \
    http://mirrors.kernel.org/ubuntu/pool/main/s/swtpm/swtpm_0.6.3-0ubuntu3_amd64.deb \
    http://mirrors.kernel.org/ubuntu/pool/main/s/swtpm/swtpm-libs_0.6.3-0ubuntu3_amd64.deb \
    http://mirrors.kernel.org/ubuntu/pool/main/s/swtpm/swtpm-tools_0.6.3-0ubuntu3_amd64.deb \
    http://security.ubuntu.com/ubuntu/pool/main/libt/libtpms/libtpms0_0.9.3-0ubuntu1.22.04.2_amd64.deb
  sudo dpkg -i libtpms0_*.deb swtpm-libs_*.deb swtpm_*.deb swtpm-tools_*.deb
  ```

- **mkosi v22+: needs Python >=3.10.** 20.04 ships Python 3.8. apt's `mkosi` is v9 (way too old; uses a different config schema). Use deadsnakes:
  ```bash
  sudo add-apt-repository -y ppa:deadsnakes/ppa
  sudo apt-get update
  sudo apt-get install -y python3.10 python3.10-venv pipx
  pipx ensurepath
  rm -rf ~/.local/pipx/venvs/mkosi  # clear any failed prior attempt
  pipx install --python=python3.10 mkosi
  exec $SHELL
  mkosi --version  # expect v22+
  ```

- **`python3.8-venv`** is also needed for any pipx work *before* deadsnakes is in place — pipx defaults to system python and fails without the venv module. `sudo apt install python3.8-venv` clears that hurdle.

- **qemu + ovmf**: standard. `sudo apt-get install -y qemu-system-x86 ovmf openssh-client`.

`bootstrap.sh` probes for each tool and emits a clear install pointer rather than auto-installing — host package changes belong to the user, not the test harness.

On 22.04+ none of this is needed; `sudo apt-get install -y qemu-system-x86 swtpm ovmf mkosi openssh-client` is the whole story.

## Known gaps & deviations

- **No LUKS, no TPM-sealed unseal.** See above — slice 0022.
- **No data drive.** Single virtio disk, no second disk for the data-drive enrollment path. `malmo-storage-verify` runs in its Level-0 path (no `/etc/malmo/data-drive.enrolled`). The second-drive shape lands when device-backing canary work picks up.
- **`host-agent.service` is staged-but-disabled.** Its `Requires=docker.service` would fail at boot. Stubbing docker in a VM the way 0020 stubs it in nspawn is feasible but out of scope for this slice. The brain stack as a whole isn't exercised in the VM yet.
- **No CI integration.** Same posture as the fast lane (0018/0020). Each lane is invocable locally via `make`; wiring all the lanes into a CI workflow is its own slice.
- **mkosi v22+ requirement.** Ubuntu 20.04's apt has v9; bootstrap detects and bails. `pipx install mkosi` is the documented escape; we don't (yet) ship a pinned version. A reproducibility concern when the lane spreads to more developer machines.
- **slirp networking.** Adequate for SSH port-forward; not adequate for any future test that needs multicast/mDNS reachable from the host. Bridged networking is the upgrade path when that test lands.
- **TCG fallback.** Without KVM (CI without `/dev/kvm`), TCG works but boot is ~10x slower. Documented; not mitigated.
- **No image signing.** mkosi can produce signed images via `[Validation] Checksum=` / `SecureBoot=` — not used here. Production signing belongs to `BUILD.md`'s pipeline, not the test lane.
- **End-to-end not yet verified.** Code is on disk; the 20.04 dev box couldn't supply all deps without significant backport work (see "Host-dep dance" above). The lane will be verified on a 22.04+ box and then committed. Per the project's "verify against real system before committing" rule, this slice is *not* in the committed history yet. Files live untracked in `dev/test-qemu/` + `docs/progress/0021-…`.

## What's next

In recommended order:

- **0022: LUKS root + first-boot TPM enrollment + unseal verification.** The "real" medium-lane test of `STORAGE.md`'s first-run flow. Builds on this scaffolding by adding LUKS to the image, a first-boot enrollment service, and second-boot assertions that read the LUKS dump for the TPM2 token.
- **Data-drive second-disk scaffold.** Attach a second virtio disk to the QEMU invocation; exercise the enrollment marker path; assert the device-backing canary check (`findmnt -no SOURCE`) once that lands in `internal/storageverify`.
- **Failure-injection harness.** A `dev/test-qemu/scenarios/` directory with named scenarios (`docker-killed-midboot`, `clock-skew`, `data-drive-detached`) each a thin wrapper that mutates the QEMU args or guest state and asserts the expected health-issue surfaces. The 10-test matrix in `TESTING.md` is the target.
- **Brain + host-agent in the VM.** Stub docker (or install a real docker + a tiny test catalog) and start `host-agent.service` so the full read-from-host-agent → brain health-registry → API round-trip is exercised. Currently only 0019's unit tests cover that.
- **CI workflow** that runs `test-usermgr-nspawn`, `test-boot-chain-nspawn`, `test-medium-qemu`, and `test-health` per PR. Carries over from 0020's "next."
