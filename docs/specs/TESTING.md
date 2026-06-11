# molma Testing

> Working spec for molma's test infrastructure — what runs in CI, at which speed lane, and what each lane catches. Companion to `BOOT.md` (the system-under-test for the bulk of these tests), `BUILD.md` (the image pipeline that feeds them).

## Stance

molma's correctness story isn't unit tests in isolation. Most of what we get wrong is **boot ordering, mount race conditions, TPM behavior, and failure modes that only surface during real boots.** The test strategy is built around that reality.

Three lanes, fastest to slowest. Each catches a different class of bug. Together they convert "boot ordering is hard to test" into "boot ordering is among the most-tested parts of molma."

## Fast lane — `systemd-nspawn` (per-PR, ~1 minute)

Runs systemd userspace in a Linux namespace. No initramfs, no kernel boot, no TPM.

**What it catches:**
- Unit dependency errors (`Requires=` / `After=` ordering)
- Drop-in overrides applying correctly
- Synthetic targets activating in the right shape
- Service-level integration: brain ↔ host-agent ↔ Caddy contracts
- nftables rule generation (in a netns-isolated container)

**What it misses:**
- Initramfs / LUKS / TPM unsealing
- Real kernel + driver behavior
- Hardware-level race conditions

Implementation: a `nspawn` test harness that mounts a built molma rootfs, boots it with synthetic disks (loopback files), and runs assertion scripts inside. Each test is a separate nspawn instance, run in parallel where possible.

**PAM verify coverage:** real `verifyPassword` test coverage (via `PAMVerifier` in `host-agent-real`) requires `/etc/pam.d/molma` installed in the nspawn rootfs plus a provisioned test user (`useradd molma-pamtest && chpasswd`). The `pam_linux_test.go` skeleton (`-tags pamtest`) is the entry point; it lands with this lane's full build-out, not earlier.

## Medium lane — QEMU + swtpm (per-PR or per-merge, ~10 minutes)

Full VM boot with a software TPM (`swtpm`). Mature stack — `systemd`'s own CI runs this way.

**What it catches:**
- Initramfs behavior (LUKS unlock at boot)
- TPM2 unseal and PCR sealing semantics
- Real boot ordering across the kernel-userspace boundary
- Disk hotplug scenarios via QEMU `device_add` / `device_del`
- Failure injection: kill Docker mid-boot, simulate clock skew, detach the data drive
- End-to-end first-run flow
- NetworkManager-integrated discovery: per-LAN-interface mDNS announcement, avahi-daemon.conf allowlist sync, interface-removal rewrite, IP-change replay (realized — rides the second boot of the LUKS cycle against a multi-NIC VM; see `dev/test-qemu/`)

**Core test matrix** (in priority order — these are the ones we should not ship without):

| Test | Setup | Assertion |
|---|---|---|
| Happy-path boot | Two virtio disks (OS + data), enrolled vTPM | Boot completes; dashboard reachable; canary file matches |
| No data drive | OS drive only, no enrollment marker | Level-0 boot; molma userspace starts on OS-drive paths |
| Data drive missing (enrolled, absent) | OS drive + marker, no data disk attached | Recovery mode reached; `smbd` not serving; `host-agent` not started |
| Data drive detached mid-write | Boot, write file, `device_del` data drive | host-agent stops; no writes to OS-drive `/home` |
| Canary mismatch | Tamper with `/srv/molma/.canary` between boots | `molma-storage-verify.service` fails; recovery mode reached |
| Ordering inversion | Remove `After=srv-molma.mount` from a bind mount | Verify oneshot catches it; box does not expose empty `/home` |
| TPM2 unseal happy path | Boot with sealed TPM, no policy change | Auto-unlock succeeds; no passphrase prompt |
| Clock skew | `-rtc base=1970-01-01`, no NTP | Caddy delays ACME until `time-sync.target` reached |
| host-agent crashloop | Ship a known-broken host-agent in a test variant | `molma-recovery.target` activates; recovery page reachable on port 80 |
| nftables coexistence with Docker | Boot, capture `nft list ruleset`, restart Docker, diff | molma's `inet molma` table is unchanged across Docker restarts |

Implementation: built on `mkosi qemu` (or the equivalent for whichever image-build tool wins in `BUILD.md`). Each test boots a VM, runs an assertion script via SSH or serial console, tears down.

Note on "reboot + unseal" scenarios (the TPM2 unseal happy path, and the slow lane's TPM rot simulation): a reboot that must *withhold* the recovery passphrase on the second boot is realized as **two sequential QEMU processes** sharing one disk overlay + OVMF vars + swtpm state dir (a faithful TPM power cycle — PCRs re-measure identically, SRK/NVRAM persist), not an in-guest `systemctl reboot`. The recovery passphrase is delivered as an SMBIOS type-11 credential fixed at QEMU launch and can't be withheld partway through a single long-lived process, so proving *unattended* unseal (no passphrase, TPM2-only) requires a fresh process with the credential omitted. Realized in slice 0023; see `docs/progress/luks-tpm-enrollment.md`.

## Slow lane — Soak + ISO end-to-end (nightly on `main`)

**What it catches:**
- Race conditions that fail intermittently (Docker readiness races, network-online timing)
- Full ISO → installer → first boot → wizard → bootstrapped state
- TPM rot simulation across simulated kernel updates

**Soak runs:**
- 100 boots of the happy-path test in a loop. Assert zero failures. Race conditions don't fail deterministically — soak catches them; single boots do not.
- Boot with throttled I/O (`iops=200`) and constrained CPU shares to surface Docker-readiness races.

**ISO end-to-end:**
- Build full ISO from the current branch.
- Boot in QEMU with no pre-installed system.
- Run installer (kiosk web UI) headless via Playwright (or equivalent).
- Reboot; run first-run wizard.
- Assert: dashboard reachable; admin account created; bootstrap marker written; recovery passphrase shown exactly once.

**TPM rot simulation:**
- Boot, capture sealed state.
- Replace `vmlinuz` with a different version (simulates a kernel security update).
- Reboot, assert TPM unseal still succeeds — because we sealed against PCR 7 only, not PCR 11 (see `STORAGE.md` # Encryption posture).
- This is the canary for the silent-rot risk over the appliance's lifetime.

## Where each lane runs

| Lane | Trigger | Budget | Blocking? |
|---|---|---|---|
| Fast (`nspawn`) | Every PR push | ~1 min | Yes — blocks merge |
| Medium (QEMU+swtpm) | Every PR push (after fast lane passes) | ~10 min | Yes — blocks merge |
| Slow (soak + ISO) | Nightly on `main` | ~1–2 hours | No — surfaces an alert; does not block PRs |

## Why this shape

- **Fast lane** validates the *spec* — that units declare the right dependencies, drop-ins are wired, synthetic targets exist.
- **Medium lane** validates the *implementation* — that the units, when actually booted, produce the expected sequence and recover from injected failures.
- **Slow lane** validates *time* — that races we couldn't trigger deterministically don't bite under load, and that the appliance ages gracefully (kernel updates, drift, clock skew).

Each lane runs strictly cheaper than the next. PR feedback comes from fast + medium; expensive coverage runs nightly.

## Tooling — locked

- **`swtpm`** (software TPM emulator) for vTPM in QEMU. Mature, widely deployed, supports TPM2 with PCRs.
- **QEMU** for VM boot, with virtio block devices for disks and a virtual UEFI firmware that supports Secure Boot (so PCR 7 sealing tests are honest).
- **Drop-in compatibility with `mkosi`'s test mode** (`mkosi qemu`). This is a meaningful weight on the `live-build` vs. `mkosi` call in `BUILD.md` — mkosi's test story is materially better. Calling that out here, not relitigating it.

## Tooling — open

- **Assertion harness language.** Go (matches the rest of the codebase, single language) or Python (mature QEMU/swtpm tooling, larger ecosystem of test helpers). Either works. Tracked in `NEXT.md`.

## What this doc deliberately doesn't pin

- Unit-test conventions inside the brain Go codebase (table tests, mocking philosophy, etc.) — that's an implementation-time call once the codebase exists.
- Frontend test strategy for the dashboard (`WEB_UI.md`) — separate concern, lighter stakes, follows whatever the chosen stack idiomatically uses (`vitest` + `@testing-library/vue` is the obvious default).
- Performance / load testing. Out of scope for the household appliance threat model in v1.

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
