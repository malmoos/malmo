# malmo Build & Boot Pipeline

> Working spec for how malmo ships — from source to a USB stick to a running box. Companion to `SPEC.md`, `CONTROL_PLANE.md`, `FIRST_RUN.md`, `STORAGE.md`.

> **Environment profiles.** This doc describes building the `appliance` install ISO. The same `mkosi` builder also emits a lean **hosted** cloud VM image profile (no Avahi/Samba/NetworkManager/cryptsetup-TPM/mergerfs) paired with a build-tagged slim cloud `host-agent`. See `ENVIRONMENT.md` # How the profile is realized.

This doc is **draft / option-survey**. Most sections present alternatives with a recommendation; locked decisions are called out explicitly. The intent is to surface forks before committing.

## What this doc covers

- The Debian base — release, kernel, what's preinstalled.
- ISO composition — tooling, layout, online vs. offline.
- The installer — what runs between USB-boot and reboot-to-disk.
- `host-agent` packaging and how it lands on disk.
- `malmo-brain` image build, distribution, first-boot pull.
- Versioning and release artifacts.

What it does **not** cover: update mechanics post-install (separate doc), CI/CD specifics, signing infrastructure (deferred until we have a release to sign).

---

## 1. Debian base

### Release

- **Debian 13 "Trixie" (stable).** Current as of 2026, fresh enough kernel/userland for modern hardware.
- Tracking testing or unstable would buy newer packages at the cost of stability we cannot afford for a non-technical-user appliance.

**Locked: Debian stable.** Re-pin when the next stable cuts.

### Kernel

Two real options:

- **Stock stable kernel.** Whatever ships in Trixie. Conservative, well-tested, but a 2026 stable kernel will already be a year+ behind on hardware support — bad for BYO x86 where the user's NIC / Wi-Fi / GPU may be newer than the kernel knows about.
- **`linux-image-*-bpo` (backports kernel).** Newer kernel, same Debian packaging discipline. Standard answer for "I want broad hardware support on stable." Used by ProxmoxVE, many appliances.

**Recommendation: backports kernel.** BYO hardware is a stated pillar (`SPEC.md`); shipping a kernel that doesn't recognize last year's Wi-Fi chips defeats it. Cost is a slightly larger update surface — acceptable.

### Kernel cmdline

The installed GRUB config must set these kernel parameters (`GRUB_CMDLINE_LINUX`):

- **`psi=1`** — enables the Pressure Stall Information accounting (`/proc/pressure/*`) that the `ram-pressure` health detector (`HEALTH.md` # Detector catalog) reads. Debian builds the kernel with `CONFIG_PSI=y` but `CONFIG_PSI_DEFAULT_DISABLED=y`, so PSI returns no useful data at runtime unless `psi=1` is on the cmdline. Without it the detector silently reads zeros and never fires — a false all-clear. Cost is negligible (a few per-cgroup counters).

### Firmware

- Include `firmware-linux`, `firmware-iwlwifi`, `firmware-realtek`, `firmware-amd-graphics`, `firmware-misc-nonfree` and similar. Non-free firmware is now in Debian's official installer by default (since Bookworm); we follow suit. Without this, half of laptops won't have working Wi-Fi at first boot.

### Preinstalled packages

Minimum to be a malmo box:

- `systemd`, `systemd-cryptenroll`, `cryptsetup` — boot, encryption, TPM auto-unlock (`STORAGE.md`).
- `docker-ce` (or `docker.io` from Debian; see below) — runtime for everything.
- `avahi-daemon` — mDNS publishing for `*.local` app hostnames and SMB service discovery (`_smb._tcp`).
- `caddy` — only if we ship it on host; if it runs as a container under the brain (per `CONTROL_PLANE.md`), skip on host.
- `malmo-host-agent` — our own `.deb`.
- `openssh-server` — SSH daemon, scoped to LAN + mesh via nftables (see "SSH" below).
- `samba` — SMB file shares for cross-device access (`STORAGE.md` # Cross-device access).
- `mergerfs` — userspace union for data drives (`STORAGE.md` # Data drives). Activates whenever a data drive is present.
- `nftables` — firewall, scoping SSH and SMB to LAN + mesh.
- Standard base utilities (`curl`, `ca-certificates`, `tpm2-tools`, `lvm2`, `e2fsprogs`, `cryptsetup-initramfs`).

**Open: `docker-ce` (upstream Docker repo) vs. `docker.io` (Debian-packaged).** Upstream is fresher and what the Docker docs assume; Debian's package lags but integrates more cleanly with apt security updates. Lean toward `docker-ce` from Docker's own apt repo — most of our app authors test against upstream Docker.

### SSH

`openssh-server` is **installed and enabled at boot** — sshd listens on :22 from first boot. However, **no account can authenticate by default**: `sshd_config.d/malmo-allowed.conf` carries an empty `AllowUsers` directive, so sshd rejects every account regardless of whether the password is valid. Per-account opt-in (Settings → My account → Enable SSH) adds the user to `AllowUsers` and reloads sshd. The user's malmo password — the same one they use for the dashboard — is what authenticates them; SSH does not have its own password (`AUTH.md` # Device access).

Why daemon-on-but-no-account instead of daemon-off-until-toggle:

- The "turn on SSH" UX is a single toggle in Settings, with no host-level service restart visible to the user. The brain calls host-agent to edit the allowlist; the daemon was already running.
- An attacker on the LAN sees an open :22, but no account is in the allowlist — sshd rejects connections at auth-name resolution, before evaluating credentials. Blast radius is bounded by `AUTH.md`'s opt-in mechanics.
- `PermitRootLogin no`, `PasswordAuthentication yes` (sshd accepts the user's malmo password; a public key can also be added to `~/.ssh/authorized_keys` for key-based login).

**Network scope: LAN + mesh only, structurally.** An nftables rule on :22 default-denies and allows only:

- RFC1918 source ranges: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (the LAN).
- The mesh interface (`tailscale0` / `headscale0`) when present — devices the user has paired via `MALMO_NETWORK.md`.

SSH from the public internet is **structurally blocked**, not relying on per-account opt-in alone. A port scan from outside sees a closed port, not a refused-auth banner. The path to "SSH to my box from outside" is "pair the device on the mesh" — same trust model the user already learns for the dashboard. Interface-agnostic by design (nftables on source IP, not `ListenAddress` on a NIC name), so changing NICs / adding Wi-Fi doesn't break it.

Implemented as a drop-in at `/etc/nftables.d/malmo-ssh.conf`, owned by the `.deb`. Mesh interface name is templated at host-agent startup based on which mesh client is installed.

### What we deliberately do not preinstall

- Desktop environment, X/Wayland session manager (except as needed for the installer — see #3).
- Anything from `tasksel`'s "standard" set beyond what we explicitly list.

---

## 2. ISO build tooling

**Decided (2026-06-16): Option C — `mkosi`.** It is malmo's single image builder, for the install ISO, the cloud VM image, and the QEMU test image alike. See "Decision" below; `DECISIONS.md` 2026-06-16 carries the delta. The four options below are kept as the record that produced the call.

The fork, as it stood — four real options:

### Option A — `live-build`

Debian's official meta-tool for building live + installer ISOs. Used by Kali, Tails, official Debian Live.

- **Pros:** Designed exactly for this. Handles squashfs, bootloader (GRUB/syslinux for BIOS+UEFI), hybrid ISO/USB, package selection, hooks for customization. Mature, well-documented, Debian-blessed.
- **Cons:** Configuration is a sprawl of directories and shell hooks. Debugging is "read the source." The tool is in maintenance mode — works fine, but few new features.

### Option B — `debian-installer` + preseed

The installer Debian ships on its own ISOs. Drive it via a preseed file (`preseed.cfg`).

- **Pros:** The most boring, well-trodden path. Massive deployments use it.
- **Cons:** Preseed is a key-value config file with awkward escape rules. Customizing the installer's *appearance* (malmo branding, custom screens) means patching `cdebconf` themes and is genuinely painful. Conditional logic (e.g., "if no second disk, skip step 2") is a pre-script hack.
- **Verdict:** Right for a sysadmin tool, wrong for a consumer appliance.

### Option C — `mkosi`

systemd-team's modern image builder. Declarative TOML config, can produce disk images, ISOs, container images. Used by Fedora CoreOS-adjacent work and increasingly in the systemd ecosystem.

- **Pros:** Clean config. First-class support for the kind of immutable / A/B image we expect to migrate to (`SPEC.md` "OS update model"). Aligned with systemd, which we depend on heavily.
- **Cons:** Newer; less battle-tested for Debian specifically (better support for Fedora/Arch). Smaller community for "I'm building a Debian appliance" recipes.
- **Strategic angle:** if we're going to A/B immutable later anyway, picking mkosi now means one tool for both v1 and the future. live-build has no story for A/B images.

### Option D — Custom (`debootstrap` + `xorriso` + scripts)

Roll our own. What Ubuntu's modern installers do under the hood.

- **Pros:** Full control. No tool quirks to work around.
- **Cons:** We become the maintainers of an ISO builder. Many person-weeks to match what live-build gives for free.
- **Verdict:** Reject. Premature DIY for a problem with mature solutions.

### Decision (2026-06-16): Option C — `mkosi`

**Locked: mkosi is the single image builder** — for the install ISO, the cloud VM image, and the QEMU test image. This overturns the earlier "live-build-for-v1, migrate-to-mkosi-later" recommendation (`DECISIONS.md` 2026-06-16). One builder, one config, one artifact definition for every target.

Why mkosi-now rather than live-build-then-migrate:

- **The test lane is already mkosi, all the way up the stack.** `dev/test-qemu/` builds the full control plane (`host-agent → brain → Caddy + UI`), boots it under `mkosi qemu`, and `mkosi-repart` already produces a LUKS2+ext4 root that TPM-unseals and switch-roots (`TESTING.md` # Full-stack control-plane integration; `docs/progress/luks-tpm-enrollment.md`). Shipping live-build for the ISO would mean maintaining a *second* builder that must stay byte-identical with the test image to hold the "live fs == installed fs" invariant (# 3). mkosi makes that invariant trivially true — there is one artifact.
- **Systemd-native is the right substrate for malmo.** We lean hard on systemd — `systemd-cryptenroll` + TPM unseal, UKI, `systemd-boot`, `cryptsetup-initramfs`. mkosi is the systemd team's own image tool, so partitioning / LUKS / TPM / UKI-signing are first-class rather than bolted on. (Umbrel, on the same Debian base, assembles a Docker-built rootfs + Rugix + Mender to get the equivalent; mkosi collapses that into one pipeline.)
- **One config emits every target.** The same mkosi definition produces the flashable install ISO **and** a cloud VM image (qcow2 / raw) for the hosted-in-cloud product. live-build has no cloud-image story; that would be a third path.
- **A/B-immutable is the stated future** (`SPEC.md` OS update model). live-build has no A/B story; mkosi's disk-image output A/B-swaps natively. We are **not** shipping A/B in v1 — v1 is mutable Debian + a flash-an-ISO install — but picking mkosi now means that future lands with no re-tooling.

What this decision **does not** settle (kept open — see `NEXT.md`):

- **The OTA update orchestrator.** mkosi's presumptive partner is `systemd-sysupdate`, but that is *not* chosen here. Umbrel uses Mender, Home Assistant OS uses RAUC — both with a deeper production track record than `systemd-sysupdate` for Debian appliances. Naming the orchestrator waits for the A/B work.
- **The interactive installer is unchanged.** mkosi vs live-build is only *how the bootable artifact is assembled* — malmo still ships the guided first-run installer of # 3 / `FIRST_RUN.md` Phase 1 (disk selection, recovery passphrase, confirm-wipe). The USB stick boots that installer, which writes the OS to the machine's internal disk. We are **not** adopting the competitors' direct-flash-the-image-onto-the-target model.

Knowingly accepted costs:

- mkosi's Debian support is thinner than live-build's (it is better-trodden for Fedora / Arch). The LUKS/TPM bring-up already paid down the riskiest part of that on a real Debian boot, but expect occasional sharp edges a live-build user would not hit.
- **A live installer ISO that boots a session is live-build's home turf** (the Tails / Kali pattern), and is the one part of mkosi's fit not yet proven in-repo — the test lane boots a *disk image*, not a live-session ISO carrying the kiosk installer (# 3). Validating mkosi's live-ISO output is a follow-up, not a reason to keep a second builder.
  - **⚠ Resolved 2026-06-17 (#199): mkosi emits no ISO — and malmo no longer wants one.** Investigating this exact follow-up found mkosi 26's output formats are `{confext,cpio,directory,disk,esp,none,portable,sysext,tar,uki,oci,addon}` — there is **no `iso`/ISO9660 format** (and no `xorriso`/El-Torito code in the package). mkosi builds GPT *disk* images. The call (maintainer, 2026-06-17): **drop the literal `.iso` entirely; the bootable artifacts are disk images** — a `qcow2`/`raw` for the cloud VM and a `raw` `dd`'d to a USB stick for bare metal. Optical-media / CD-DVD boot is explicitly out of scope. The "live fs == installed fs" invariant (# 3) is unaffected — a `Format=disk` root is what gets booted/laid down — and "mkosi is the single builder" holds exactly (this is mkosi's native distribution model). The cloud VM image is the **priority** target; bare-metal USB follows (`#196` epic ordering). See `DECISIONS.md` 2026-06-17 and `progress/iso-mkosi-finding.md`.

---

## 3. The installer

`FIRST_RUN.md` Phase 1 specifies the installer's user-visible flow: hardware check → disk selection → recovery passphrase → confirm wipe → install → reboot. This section is *how* that flow runs.

### Three execution models

- **Model 1 — Custom TUI** (text-mode, ncurses-style). Lightweight, ugly, fine for tinkerers, wrong for the long-term audience.
- **Model 2 — Custom GUI in a minimal Xorg/Wayland session.** Boot a minimal desktop, run a malmo-branded GTK/Qt app. Pretty, heavy on ISO size and dev work.
- **Model 3 — Web installer in a kiosk browser.** Boot a minimal compositor (`cage` or `weston --kiosk`), launch Chromium pointed at a local installer service (Go binary serving HTTP on `localhost`). The installer service does the actual work (partitioning, LUKS, TPM enrollment, file copy).

### Recommendation: Model 3 (kiosk web installer)

- Reuses our web stack — same TypeScript framework, same components, same designers as the post-install dashboard. Visual consistency from USB-boot to dashboard.
- The installer service is a sibling to `malmo-brain` in shape: Go binary, HTTP API, but its job ends at first reboot. We can borrow patterns and even some packages.
- ISO cost: ~150–250 MB for compositor + Chromium. Acceptable on a multi-GB ISO.
- The same UI language carries forward — no jarring "install looks like a 90s setup, then suddenly it's a polished web app."

ZimaOS and a couple of other appliance OSes use this exact pattern. It's well-trodden.

### What the installer service does

1. Probe hardware (CPU, RAM, disks, UEFI, TPM2). Refuse with a clear message if any hard requirement (`FIRST_RUN.md`) fails.
2. Present disk picker + recovery-passphrase screen.
3. On confirm:
   a. Partition target disk(s) (GPT, ESP + LUKS-encrypted root).
   b. `cryptsetup luksFormat`, generate recovery passphrase, enroll TPM2 with `systemd-cryptenroll`.
   c. Lay down the OS image (squashfs → ext4 copy, or rsync from the live filesystem). The installer's *own* live environment is essentially the same image we lay on disk.
   d. Install GRUB to the ESP, configure for UEFI.
   e. Run `update-initramfs` so initramfs has TPM-unlock support.
4. Show recovery passphrase, require user confirmation.
5. Reboot.

### Decision: live filesystem == installed filesystem

The same root filesystem the live ISO boots from is what gets copied to disk. No separate "live image" vs. "installable image." Means everything we test in the live environment is what runs post-install. With one mkosi-built artifact (# 2) this invariant is structural rather than a discipline to maintain across two builders.

---

## 4. `host-agent` packaging

Three options:

- **A — Ship as `.deb` in our own apt repo.** ISO build pulls it during package selection. Updates ride apt. Standard Debian.
- **B — Bake the binary directly into the live filesystem at ISO build time** (no `.deb`, just a file + a systemd unit). Simpler, but no apt-managed update path.
- **C — Distribute as a container alongside the brain.** Inverts the architecture — host-agent is the *one* thing that should be on the host, not in a container (`CONTROL_PLANE.md`). Reject.

**Recommendation: A.** Ship `malmo-host-agent.deb` from our apt repo.

- Native package, native systemd unit, native logs.
- apt is how host-agent updates until we move to A/B images. When we do, the `.deb` gets baked into the immutable image and the apt path retires. Cheap migration.
- Our apt repo (`apt.malmo.network` or similar) hosts this one package for v1. Adding more later is mechanical.

The repo is signed; the ISO build trusts our key. Key management is a release-infra concern, deferred to the release-infra doc.

---

## 5. `malmo-brain` image

Per `CONTROL_PLANE.md`: brain runs as a container, supervised by host-agent.

### Build

- Multi-stage Dockerfile. Build stage compiles the Go binary (static, CGO disabled where possible). Runtime stage: **`debian:trixie-slim` with the `docker` CLI + Compose plugin bundled** (`docker-ce-cli` + `docker-compose-plugin` from Docker's official apt repo — the same trusted source as the host engine, per the Docker-package-source decision below). **Not distroless:** the brain orchestrates apps by shelling out to the `docker` / `docker compose` CLI (`internal/lifecycle/docker.go`), which a distroless runtime — no shell, no binaries — cannot host. Multi-stage already keeps the Go toolchain out of the final image; the bundled CLI is a runtime dependency it can't trim, putting the image at **~256 MB** (measured in M0, #163) — immaterial against the multi-GB app images the box pulls, and slim stays debuggable (it has a shell). See `DECISIONS.md` 2026-06-13 for the flip off distroless.
- Output is a single OCI image, tagged `vX.Y.Z` and `latest` (latest only on stable channel).

### Distribution — three options

- **A — Public registry (`ghcr.io/malmo/brain` or Docker Hub).** Pull at first boot. Simple, no infra to run beyond a registry account. Requires internet at first boot.
- **B — Self-hosted registry (`registry.malmo.network`).** Same as A but we own the namespace and don't depend on GitHub/Docker policies. Modest VPS cost.
- **C — Bundle the image in the ISO.** Image is loaded into Docker at install time via `docker load`. Works offline at first boot. ISO grows by the image size (~256 MB for the slim-with-CLI brain image, measured in M0 #163 — see the Build section above — still small against the multi-GB app images the box pulls).

### Recommendation: B + C combined

- **Bundle a pinned brain image in the ISO** so the box boots and is functional with zero internet.
- **Self-hosted registry for ongoing updates.** host-agent (or the brain itself) pulls newer tags from `registry.malmo.network` when online.
- Self-hosted over public-registry-only because: (1) a `malmo` namespace on Docker Hub is not guaranteed; (2) we already need `malmo.network` infra for the mesh, adding a registry is incremental; (3) avoids dependency on a third party's pull-rate-limit policy.
- We can mirror to a public registry as a redundancy story, but it's not the source of truth.

### First-boot brain bootstrap

1. host-agent starts (systemd, after Docker).
2. host-agent checks `/var/lib/malmo/brain-image.tar` (bundled in ISO) — if Docker doesn't already have the image, `docker load` it.
3. host-agent pulls the latest tag from `registry.malmo.network` if online and a newer version exists. (Behavior on offline: keep the bundled version. Behavior on update failure: keep current. Never break boot.)
4. host-agent starts the brain container with the configured pin.
5. Brain takes over from there — Caddy, `malmo-ui`, sidecars, etc. (`CONTROL_PLANE.md`).

---

## 5b. `malmo-ui` image

The dashboard ships as a **second OCI image**, built and distributed the same way as the brain. `WEB_UI.md` owns the stack and deploy model; this section covers only how the image is built and lands on a box.

### Build

- Base `caddy:alpine`, with the built UI bundle (`web-ui/dist`) baked in at `/srv/ui` and the trivial SPA Caddyfile (serve `/srv/ui`, fallback to `index.html`, gzip/brotli/ETag on by default). No build-stage Go compile — the bundle is produced by the UI's own `vite build` upstream of the image build (`WEB_UI.md`).
- Output is a single OCI image, tagged `vX.Y.Z` and `latest` (latest only on stable channel) — the same `vX.Y.Z` as the brain, one repo version (# Versioning, above; `WEB_UI.md` # deploy + update flow).

### Distribution

Same as the brain (# 5 Distribution): **bundled in the ISO for offline first-boot** (`docker load` from a pinned tarball) **and** pulled from `registry.malmo.network` for ongoing updates. Both images appear together in the release manifest (`RELEASE_MANIFEST.md`); the updater recreates only what changed (`WEB_UI.md` # deploy + update flow).

### Launch

`malmo-ui` is **not** started by host-agent. The brain launches it as part of the control-plane stack, alongside Caddy (`CONTROL_PLANE.md` # Locked: the dashboard UI is a brain-launched container). host-agent's brain bootstrap (# First-boot brain bootstrap) ends at the brain; the brain brings up everything downstream.

---

## 6. Artifacts and channels

### Per-release artifacts

All artifacts of a release share the **one** `vX.Y.Z` from the repo `VERSION` file (# Versioning, above) — there is no independent per-component tag to keep in sync.

- `malmo-vX.Y.Z-amd64.qcow2` — the **cloud VM image** (priority target; the hosted product provisions tenants from it — `ENVIRONMENT.md` # Provisioning). Emitted by mkosi `Format=disk`.
- `malmo-vX.Y.Z-amd64.raw` — the **bare-metal install medium**, `dd`'d / flashed to a USB stick (the "old laptop in the pantry" path). Same mkosi `Format=disk` rootfs; not optical media (no `.iso` — see # 2's 2026-06-17 resolution and `DECISIONS.md`).
- `malmo-host-agent_X.Y.Z_amd64.deb` — published to `apt.malmo.network`.
- `registry.malmo.network/malmo/brain:vX.Y.Z` — the brain image. `latest` tag advances on stable channel.
- `registry.malmo.network/malmo/ui:vX.Y.Z` — the dashboard image. Same `vX.Y.Z` as the brain (one repo version); both bundled in the ISO for offline first-boot.

### Channels

- **Stable** — what `malmo.com/download` points at. Default for all installs.
- **Beta** — opt-in via Settings. Same artifacts, different repo / tag suffix.
- *(No nightly in v1. Internal CI builds exist but aren't a user-facing channel.)*

A box's channel determines which apt repo it follows for `host-agent` and which brain tag it tracks.

### Versioning

**One repo version for the whole monorepo** (`vMAJOR.MINOR.PATCH`), not independent per-component SemVer (DECISIONS.md 2026-07-16, flipping the two bullets this section used to carry). `host-agent`, `malmo-brain`, and `malmo-ui` all ship from one commit in one repo — an independent counter per component was bookkeeping with no consumer once that was true.

- **`VERSION`** — a plain-text file at the repo root, the single source of truth. It holds the **last released** version and changes only in the dev->main release PR (`docs/dev/contributing.md` # Release model): bump `VERSION` -> merge dev->main -> tag `vX.Y.Z` matching `VERSION` -> the tag push builds and publishes. No `-dev` suffix, no "next target" bookkeeping between releases.
- **Every build stamps two fields, not one:** the repo version (from `VERSION`) and the git commit it was built from (`git rev-parse --short HEAD`), via `-ldflags -X` into `internal/version` — e.g. `malmo-brain --version` prints `malmo 0.4.0 (g1a2b3c)`. On a tagged release the commit is the tag's commit; on a dev build between releases it isn't, and that's visible without needing a suffix on the version string itself. `VERSION` (not `git describe`) is the source CI asserts a pushed tag against, because the brain's container build and the mkosi cloud-image build both run from contexts without full `.git` history (the Dockerfile's build context excludes `.git` entirely — `.dockerignore`).
- **The image inherits the repo SemVer, not CalVer.** The ISO/cloud-image build used to be planned as `YYYY.MM` on the reasoning that it's a snapshot of host-agent + brain + Debian + apps, not a single component — that reasoning assumed independent per-component versions needed reconciling into something else for the image. With one repo version, brain/UI/host-agent/image are all just "the same commit," so the image takes the same `vX.Y.Z` the commit already has. One commit, one identity, not two.
- The image still carries a manifest listing the exact versions of every component it bundles (Debian base version, kernel, etc. — components genuinely external to this repo).

---

## 7. Build pipeline shape (informational)

Not locking specifics, but the rough shape:

```
   Source (host-agent, brain, UI)
            │
            ▼
       CI (build, test)
            │
            ├──► host-agent .deb ──► apt.malmo.network
            ├──► brain image ─────► registry.malmo.network
            └──► ui image ────────► registry.malmo.network  (caddy:alpine + bundle, see WEB_UI.md)
                                     │
                                     ▼
                          mkosi image assembly (Format=disk)
                                     │
                                     ▼
                  malmo-vX.Y.Z-amd64.qcow2 (cloud VM, priority)
                  malmo-vX.Y.Z-amd64.raw   (bare-metal USB)
                                     │
                                     ▼
                              releases.malmo.network
                                     │
                                     ▼
                        stable.json (+ minisig) — see RELEASE_MANIFEST.md
```

GitHub Actions or self-hosted CI — TBD, not architecturally interesting at this stage.

---

## Locked decisions

- **Base: Debian stable (currently Trixie / 13).**
- **Kernel: Debian backports kernel** for hardware support on BYO x86.
- **Non-free firmware bundled** for Wi-Fi and GPU support out of the box.
- **Image tooling: `mkosi`** (decided 2026-06-16, `DECISIONS.md`). One builder for the cloud VM image, the bare-metal USB install image, and the QEMU test lane; systemd-native, and A/B-ready for the immutable future. Overturns the earlier live-build-for-v1 recommendation. **(⚠ #199, 2026-06-17 resolved: mkosi has no ISO9660 output — it builds GPT *disk* images, and malmo no longer ships a literal `.iso`. Artifacts are a `qcow2`/`raw` cloud image (priority) and a `raw` USB image; CD/DVD/optical boot is out of scope. See # 2's resolution + `DECISIONS.md` 2026-06-17.)**
- **Installer execution model: kiosk web installer.** Minimal compositor (`cage` / `weston --kiosk`) + Chromium pointed at a local installer service. Closest production reference: Fedora's Anaconda Web UI.
- **Docker package source: `docker-ce` from Docker's official apt repo.** Revisit if Docker Inc. policy changes; swap to `docker.io` is a one-line apt source change.
- **`host-agent` ships as a Debian package** from our own apt repo, not as a container.
- **`malmo-brain` ships as an OCI image**, `debian:trixie-slim` runtime with the `docker` CLI + Compose plugin bundled (the brain shells out to them; distroless can't host them — `DECISIONS.md` 2026-06-13), from our own registry, also bundled in the ISO for offline first-boot.
- **`malmo-ui` ships as a second OCI image** (`caddy:alpine` + baked UI bundle), from our own registry, also bundled in the ISO. Launched by the brain, not host-agent (`CONTROL_PLANE.md`).
- **Same root filesystem serves both the live (installer) environment and the installed system.**
- **SSH daemon enabled at boot; no account can authenticate until per-user opt-in** (`AUTH.md` # SSH access). Root login disabled.
- **Channels: stable only in v1, no beta, no nightly.** Beta is additive when triggered (see `RELEASE_MANIFEST.md`).
- **Versioning: one repo SemVer for the whole monorepo, the image inherits it.** `VERSION` at the repo root is the source of truth; every build additionally stamps the git commit as a separate field. No independent per-component counters, no CalVer for the image (DECISIONS.md 2026-07-16, flipping both prior positions).

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
