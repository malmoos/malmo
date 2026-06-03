# molma Storage Architecture

> Working spec for how molma lays out storage, what disks the user sees, and how encryption works. Companion to `SPEC.md`, `CONTROL_PLANE.md`, `APP_MANIFEST.md`, `SERVICE_PROVISIONING.md`, `APP_ISOLATION.md`.

## Stance

NAS power users have TrueNAS, Unraid, Proxmox, HexOS. molma is not trying to be a NAS. The storage story optimizes for the home user who has one or two disks and wants apps and shared folders that just work.

**No NAS vocabulary in the UI.** No "pool," "vdev," "RAID," "parity." The only storage concepts a user sees are **OS drive** and **data drive**.

## Levels of complexity (and the v1 cut)

There's a ladder of how sophisticated a storage layer can be. We deliberately ship the bottom two rungs:

| Level | What it is | Ships in v1 |
|---|---|---|
| 0 | OS drive only (no data drive) | ✅ |
| 1 | OS drive + one or more data drives, mergerfs union pool | ✅ |
| 2 | Level 1 + SnapRAID parity | ❌ later, additive |
| 3 | ZFS / Btrfs pools | ❌ out of scope (TrueNAS land) |
| 4 | SSD cache tier in front of HDD pool | ❌ out of scope |
| 5 | Multi-pool with per-pool policies | ❌ out of scope |

The v1 cut covers the realistic home setup: a small NVMe for OS, one or more big HDDs for media. Adding a second data drive later is a non-disruptive operation — mergerfs pools from day 1 means it's `mergerfs add`, not a re-architecture. SnapRAID (parity protection) is the next additive rung.

**Why mergerfs from day 1, even with one data drive:** adding drive #2 has zero downtime — `mergerfs add` to a running pool. Single-drive boxes pay a small FUSE overhead (5–10% on some workloads, near-zero on most) in exchange for that expansion property. The audience explicitly includes tinkerers who will add drives; we don't want to limit them.

## Filesystem and encryption

**ext4 + LUKS, TPM-auto-unlock.** Both the OS drive and (if present) the data drive are LUKS-encrypted with separate keyslots. Auto-unlock at boot via `systemd-cryptenroll` against the TPM2.

### Why ext4, not ZFS

1. **Licensing.** ZFS-on-Linux is CDDL; the kernel is GPL. Debian ships it as DKMS modules rebuilt against each kernel. For an OS that auto-updates, kernel-vs-ZFS lag is an ongoing operational tax — a kernel security update can leave the box unbootable until ZFS catches up.
2. **Overkill for one disk.** ZFS's value is pools, RAIDZ, scrubbing across redundant disks. Single-vdev ZFS is "ext4 with extra steps and more RAM."
3. **RAM appetite.** ARC is aggressive. Another tuning knob that bites users on 4–8 GB boxes.
4. **Encryption is fine without it.** LUKS under ext4 is the Debian-blessed path. Better tooling, simpler recovery, TPM auto-unlock via `systemd-cryptenroll` is well-trodden.
5. **ZFS forecloses Level 2/3.** ZFS owns the disk; mergerfs and SnapRAID expect independent per-disk filesystems. Picking ZFS would require destructive migration to ever go union-pool.

ext4 + LUKS keeps every door open. Once we'd reach for ZFS we'd be in Level-4 territory we said we don't want.

### Encryption posture

- **Every drive LUKS-encrypted.** OS drive and each data drive get their own LUKS volume.
- **One recovery passphrase covers all drives, present and future.** Generated once at install. The same key material is enrolled as a LUKS keyslot on the OS drive, the data drive (if present at install), and every drive added later. The user sees one secret, not one per drive.
- **The passphrase is not shown to the user by default.** At install, the box silently generates it and stores it on the (LUKS-encrypted) OS drive at `/etc/molma/secrets/luks-recovery.key`, owned `root:root`, mode `0400`. The wizard does not display it; there is no "write this down" screen at first-run. The dashboard recovery code (`AUTH.md` # The recovery code) is the only "save this" moment in first-run.
- **Surfaced in Settings → Storage → Advanced** for users who need it: *"Show recovery passphrase — only needed if your box ever fails to boot. Most people never need this."* Tinkerers who move drives between boxes or fiddle with BIOS can fetch it; non-technical users never see it.
- **Why hide it.** The passphrase only matters in scenarios where the box can't auto-unlock at boot (TPM wiped via BIOS reset, Secure Boot policy change invalidates the PCR 7 seal, drive moved to another box). All three are tinkerer scenarios. For non-technical users, hardware death is already a *restore from off-box backup* path (see # What we don't do in v1), not a *type the passphrase at the console* path. Shipping a "save this 32-character secret" screen at install for a string most users will never need is friction without payoff. The doubly-lost case (box can't boot, no off-box backup) is honest and accepted.
- **Auto-unlock via TPM2, sealed against PCR 7 only.** Headless boot survives reboots without a passphrase prompt — non-negotiable for a home server. PCR 7 is the Secure Boot policy state, which is stable across kernel updates — sealing against PCR 11 (kernel measurement) would brick auto-unlock every time Debian ships a kernel security update. PCR 7 trades some attacker-against-Secure-Boot resistance for a working unattended-boot story; the molma threat model (home appliance, primary threats are remote attackers and casual theft) accepts that trade.
- **Upgrade path open.** Each LUKS slot is independent. We can add stricter TPM2 policies later (PCR 7+11 with signed policy, or PCR 7+14) via `systemd-cryptenroll` without destructive migration. Tracked in `NEXT.md`.

### Threat model — explicit

- ✅ Defends against **drive removal / drive theft / drive RMA**. A drive that leaves the building is unreadable. This is the realistic threat.
- ❌ Does **not** defend against **whole-box theft**. The TPM is in the box; an attacker with the box has time. Closing this gap means a boot PIN, which breaks unattended boot. v1 accepts the limit. A future "high-security mode" with PIN-on-boot is a Settings toggle.
- ❌ Does **not** defend against **a doubly-lost scenario** — the box can't auto-unlock (TPM wiped, motherboard swap) *and* the user has no off-box backup *and* never fetched the hidden recovery passphrase from Settings → Advanced. Data is gone. Honest position; same shape as "lost both copies of any single-copy secret."
- ❌ Does **not** defend against **the box admin reading another user's data**. Once LUKS unlocks at boot, every file on the disk is plaintext to root. Filesystem permissions (`0750` on `/home/<user>/`) protect against the UI surface and other molma users; they do not stop someone with shell-as-root. **Per-user, admin-resistant privacy is on the roadmap** as a layered fscrypt addition — see "Future: per-user encryption" below.

### Data drive enrollment marker

A box without a data drive (Level 0) and a box whose data drive is *currently missing* are two completely different situations. The first is normal; the second is a recovery scenario. molma distinguishes them with a marker file on the OS drive.

- **Path:** `/etc/molma/data-drive.enrolled`.
- **Lives on the OS drive** (not the data drive), so it survives data-drive failure or removal.
- **Contents:** JSON — at minimum the LUKS UUID of the enrolled drive and the timestamp of enrollment. The UUID lets us also catch "wrong data drive plugged in" (different physical disk, different UUID → recovery mode, not silent acceptance).
- **Written at:** data-drive enrollment time (first-run wizard, or "add data drive" flow in Settings).
- **Removed at:** explicit "remove data drive" flow in Settings — never silently.

Boot logic, in plain terms:

- **No marker present** → Level-0 boot. Everything lives on the OS drive. Normal.
- **Marker present, drive mounts and matches** → Level-1 boot. Normal.
- **Marker present, drive doesn't mount** → box still boots; brain raises `data-drive-missing` health issue and blocks writes / apps / user changes until the drive is reattached. See `HEALTH.md`.
- **Marker present, UUID mismatches** → box still boots; brain raises `data-drive-wrong` health issue and offers "Format this drive and use it" / "Eject, this is the wrong drive" actions. See `HEALTH.md`.

### Storage canary

Bind mounts that race their parent mount can silently land on the wrong filesystem (e.g., `/home/<user>` bound to an empty OS-drive directory because `/srv/molma` wasn't ready yet). Apps then write user data into the wrong tree; the next boot shadows it with the correct bind and the writes are orphaned.

To make this class of bug **detectable instead of silent**, every data drive carries a canary file:

- **Path on the data drive:** `/srv/molma/.canary` — contents are the drive's UUID.
- **Path through the bind-mount view:** `/var/lib/molma/.canary` — should resolve to the same file, same contents.
- **The device backing each bind** (via `findmnt -no SOURCE`) should match the UUID enrolled in the marker. Content match alone is not sufficient — a stale canary on the OS drive plus a failed data-drive mount would otherwise read as healthy. Device-backing check is the structural verifier; content check is the secondary confirmation.

**`molma-storage-verify.service`** (a `Type=oneshot` in the boot chain, owned by `BOOT.md`) performs both checks and writes its findings to `/run/molma/health/storage.json`. **It is a reporter, not a gatekeeper** — findings become health issues the brain raises in degraded mode, not boot failures that brick the box. See `BOOT.md` # The storage-ready target — best-effort assembly and `HEALTH.md` for the full degraded-mode model.

The canary plus device-backing check is the difference between "the mount unit returned success" and "the bind actually landed on the right filesystem." Cheap and load-bearing.

## Disk roles, mount layout, and what apps see

The user-visible model is two roles (OS drive, data drive). The on-disk reality involves mergerfs and bind mounts, but those are plumbing — **apps and users only ever see `/home/<user>/`, `~/Shared/`, and `/var/lib/molma/`**. Standard Linux paths, no invented vocabulary.

### Files are first-class; apps are windows onto them

The load-bearing principle for the whole storage design:

> **The user's files exist, and the user owns them. Apps are windows onto those files. When the user switches apps, the files stay.**

Concretely: photos live at `/home/cindy/Photos/`, not inside Immich's database. A photos app reads/writes that folder by bind mount; uninstalling the app never deletes the photos. This is the differentiator vs. Nextcloud/Photoprism-style apps that own opaque content libraries.

Two kinds of app data, two locations:

| Kind | Lives at | Survives uninstall? | User sees it? |
|---|---|---|---|
| **User content** (photo files, music, notes) | `/home/<user>/Photos/`, etc. | Yes, always | Yes, daily |
| **App state** (indexes, caches, DBs) | `/var/lib/molma/instances/<id>/data/` | No (or archived on "keep data") | No, ever |

### OS drive

Required. Small (32–64 GB is plenty). Holds:

- Debian root, kernel, initramfs.
- `host-agent` binary, systemd units, vendor config (`/etc/molma/defaults/`).
- Bundled brain image for offline first-boot.

**Designed to be replaceable.** No irreplaceable state lives here. If it dies: install fresh molma on a new SSD, point at the existing data drive, the world resumes.

### Data drive(s)

Optional but expected. One or more drives, each ext4 + LUKS + TPM-enrolled independently. Each drive mounts at `/mnt/disk<N>/`.

**Mergerfs unions them at `/srv/molma/`** — a single tree across all data drives, `epmfs` placement policy ("existing path, most-free-space" — keeps related files on the same drive, balances new directory trees). Apps and users never reference `/mnt/disk<N>/` directly.

```
/mnt/disk1                       data drive #1, ext4+LUKS+TPM
/mnt/disk2                       data drive #2, if added
   └── mergerfs union → /srv/molma/
                          ├── home/      → bind-mounted to /home
                          ├── state/     → bind-mounted to /var/lib/molma
                          └── shared/    → exposed as ~/Shared per user
```

**No data drive (Level 0):** `/srv/molma/` is a directory on the OS drive root. Same paths from app/user perspective. Adding a data drive later mounts it, sets up mergerfs, migrates contents, transparently.

### What apps and users actually see

```
/home/<user>/                    user's personal content (capitalized, macOS / XDG convention)
  Photos/
  Music/
  Movies/
  Documents/
  Notes/
  Downloads/
  Shared/                        → symlink to /srv/molma/shared/

/srv/molma/shared/               household-shared content (mirrors the use-case folders)
  Photos/  Music/  Movies/  Documents/

/var/lib/molma/                  molma's bookkeeping (brain SQLite, app instances)
  brain/state.db
  instances/<id>/                 app working dirs — never user-facing
  managed-services/
```

The use-case folders (`Photos`, `Music`, `Movies`, `Documents`, `Notes`, `Downloads`) are auto-created at user creation. `~/.config/user-dirs.dirs` is populated so XDG-aware tools resolve them. Users may rename, delete, or add folders — apps resolve canonical paths via XDG so a rename doesn't break them.

`Movies` (not `Videos`) — matches macOS, signals "stuff you watch" rather than "videos you record."

### Permissions

- **Per-user content:** `/home/<user>/` owned `<user>:<user>`, mode `0750`. Tier-3 (per-user) app instances run as the user's UID — direct owner access, no group plumbing.
- **Household-shared content:** `/srv/molma/shared/` owned `root:molma-shared`, mode `02770` (setgid bit — new files inherit `molma-shared` group automatically). Every household user is added to `molma-shared` at user creation. See `USERS_AND_GROUPS.md` for the full group reference (and the distinction between `molma-shared` and the unrelated `molma` IPC group).
- **Tier-1/2 shared services** (e.g., a household-level Jellyfin): run as a dedicated service UID in the `molma-shared` group. They see the shared tree, not individual users' homes (unless the manifest declares specific user folders).
- **Defense in depth:** manifest declarations (brain-enforced via bind-mount scope + mode) are the user-visible layer; POSIX permissions are the kernel-enforced safety net.

### Cross-device access (SMB)

Files on the box are reachable from any other device on the LAN via Samba — the only protocol with first-class clients on Windows, macOS, iOS, Android, and Linux.

| Share | Maps to | Auth |
|---|---|---|
| `\\molma\<user>` | `/home/<user>/` | that user's molma password (same as dashboard) |
| `\\molma\shared` | `/srv/molma/shared/` | any household user's molma password |

mDNS (`_smb._tcp`) advertises the box via Avahi so it appears automatically in:

- macOS Finder "Network" sidebar
- Windows Explorer "Network" view (requires Bonjour installed — `FIRST_RUN.md`)
- iOS Files.app "Connect to Server" suggestions
- Android file managers' SMB tabs (NSD-aware file managers find SMB advertisements even though Android browsers can't resolve `.local` URLs)

Samba's Avahi integration is shipped upstream; the molma systemd drop-in keeps it ordered behind `molma-storage-ready.target` but unblocking. The publisher itself (Avahi) and the LAN-only interface scoping are owned in `DISCOVERY.md`.

**TimeMachine compatibility** is enabled on a per-user dedicated share (`fruit:time machine = yes`) — Mac users get a backup target without configuration.

Auth uses the **user's molma password** — the same credential they use for the dashboard. SMB access is off by default per account; the user opts in via Settings (which adds them to Samba's allowlist). The password itself is shared across surfaces; what's off-by-default is service access, not the credential. See `AUTH.md` # Device access (SSH + SMB).

### Anything else

A third drive plugged in beyond the OS drive can be added to the mergerfs pool (becomes another data drive) or mounted as a named "extra drive" outside the pool. Extra drives are an escape hatch — not pooled, not backed up by molma, not visible to apps by default. Door-2 custom compose can bind-mount them for power users.

## First-run flow

1. Installer detects all attached disks.
2. User picks the **OS drive** (defaults to the smallest non-removable disk, typically the NVMe).
3. If a second non-removable disk is present, user is offered "use this to expand your storage" — yes by default, picks the largest remaining disk. Single-drive boxes skip the question and surface a soft "you can add a data drive later from Settings → Storage" hint on the dashboard after first-run.
4. Installer generates one recovery passphrase, formats both drives as LUKS + ext4, enrolls the passphrase as a keyslot on each, enrolls a TPM2 keyslot against PCR 7 on each, writes the passphrase to `/etc/molma/secrets/luks-recovery.key` (mode `0400`, root-owned).
5. **The passphrase is not displayed.** The user is not asked to write anything down. See # Encryption posture for why.
6. Boot proceeds. Subsequent boots auto-unlock via TPM, no prompt.

Adding a data drive after install is the same flow minus the OS-drive step, accessible from Settings → Storage.

## Adding drives and upgrading to parity

### Adding a data drive (already supported in v1)

Mergerfs runs from day 1, so adding drive #N is a brain-orchestrated operation, not a re-architecture:

1. New drive shows up in Settings → Storage.
2. Admin clicks "Use this drive." Dashboard re-prompts for the admin password — this bypasses the 5-minute elevation window (see `AUTH.md`); add-drive is treated as an enrollment-class action, fresh password required every time.
3. Brain calls host-agent `POST /v1/storage/enroll-drive` (see `BRAIN_HOST_PROTOCOL.md`). host-agent verifies the password via PAM, then: reads `/etc/molma/secrets/luks-recovery.key`, formats the drive, runs `cryptsetup luksFormat` adding the recovery passphrase as a keyslot, enrolls a second keyslot against PCR 7 via `systemd-cryptenroll`, mounts at `/mnt/disk<N>/`, writes the canary.
4. Brain calls `mergerfs add` on the running pool. Zero downtime; `/srv/molma/` gains capacity immediately.
5. Existing files stay where they are; new writes distribute by `epmfs` policy.

The user never sees or types the LUKS passphrase. The promise *one passphrase covers all drives* is upheld silently by host-agent reading it back from disk after admin authentication.

A user who lost their admin password but has the dashboard recovery code redeems the code first (which forces a fresh admin password — see `AUTH.md` # Using the recovery code), then proceeds normally. Add-drive itself never accepts the recovery code directly.

### Ejecting a data drive

Inverse of add-drive, same gate. Used when an admin wants to physically remove the data drive (upgrading to a bigger one; intentional de-enrollment).

1. Admin clicks "Eject this drive" in Settings → Storage. Re-prompt for admin password (same fresh-password rule as add-drive).
2. Brain stops every app that uses the drive (Tier-1, Tier-2, and per-user Tier-3 instances), then unmounts the mergerfs pool, then unmounts the LUKS volume.
3. host-agent removes the enrollment marker (`/etc/molma/data-drive.enrolled`).
4. Dashboard tells the user "safe to remove."

Multi-data-drive eject (mergerfs has to migrate files off the ejected drive first) is out of scope for v1 — v1 supports one data drive at a time. The single-drive eject is the inverse of single-drive add; the multi-drive case lands when we ship multi-data-drive UX.

### Upgrade to Level 2 (SnapRAID parity)

SnapRAID is a userspace tool that computes parity over existing files in-place. Pairs naturally with mergerfs: mergerfs handles pooling, SnapRAID handles redundancy.

- Add a parity drive (LUKS + ext4 + TPM-enrolled). Must be ≥ the largest data drive.
- Configure SnapRAID's `data` and `parity` pointers, schedule nightly `snapraid sync`.
- **No data migration.** Parity is computed from current state.
- Encryption interaction is fine: SnapRAID reads through the filesystem (plaintext); parity is stored on the encrypted parity drive.

Cost: install snapraid, schedule a timer, expose a UI flow, surface sync status and last-sync recency. UI has to teach "parity is point-in-time — the last day of changes is at risk." Deferred from v1 because parity only matters with 2+ data drives, requires the user to dedicate one drive as parity, and the recovery UX is a real product surface in its own right.

### Why this is cheap

Each disk is an independent ext4 filesystem. Mergerfs is a mount layer; SnapRAID is a scheduled job. Adding either is a feature flag, not a rebuild. A different v1 choice (ZFS, Btrfs multi-device, LVM-thin) would have made expansion a destructive migration.

## Future: per-user encryption (planned upgrade, not v1)

LUKS-only protects the disk against theft but not users from each other once the box is unlocked. The planned upgrade adds an encryption layer **above** the disk, keyed per user, so even root cannot read another user's files without that user's password.

**Tech.** `fscrypt` — kernel-native ext4 filesystem-level encryption. Each user's home directory is encrypted with a key derived from their password. Filenames and contents are unreadable without the key. This is the same primitive that powers Chromebook user separation.

**Why we can ship it later, not now.** The v1 architecture is already aligned:

- ext4 ✅ — fscrypt is ext4-native.
- Per-user home layout ✅ — `/home/<user>/` is exactly what gets encrypted.
- Per-user accounts with passwords ✅ — key material is right there.
- Per-user app instances ✅ — lifecycle hook to load/unload keys exists.

The v1 → encrypted delta is a migration tool + key-lifecycle module, not a re-architecture. The migration is per-user (copy-then-swap into a new encrypted directory, hours per user with TB-scale data, never destructive in place).

**Key lifecycle — the real product call, made when fscrypt ships.**

- *Model A:* key loaded only while the user has an active UI session. Apps stop when the user logs out. Strongest privacy, but background work (photo backup, sync) breaks for inactive users.
- *Model B:* key loaded while any of the user's apps is running. Once anything is installed, key is effectively always loaded. Background work works; admin-with-root can read while apps run.

There is no model that gives both background work and full admin-resistance without hardware-level secure enclaves we don't have. The product call belongs to the upgrade conversation, not v1.

**Discipline between now and then.** Any feature shipped before fscrypt that touches user data is designed *as if* fscrypt is already on:

- **Backup is per-user-keyed from day one.** Admin cannot restore another user's backup without their password, even though the underlying storage is currently plaintext.
- **No admin-keyed cross-user search or indexing.** If a feature needs to read every user's files, it doesn't ship until each user's instance does it for themselves.
- **No "admin reset of user data" flows.** Admin can reset a member's password to let them log in; admin cannot recover the data under the old password. Forgotten password = lost data, communicated upfront.

This keeps the future migration data-only, not feature-redesign.

## What we don't do in v1

- **No parity protection.** Mergerfs pools drives but doesn't replicate. Each drive's files are at independent risk — a dead drive means its files are gone (files on other drives survive). The UI surfaces this honestly when the user adds drive #2 ("Your data isn't replicated across drives. Add a parity drive later, or rely on off-site backup."). Parity ships at Level 2 (SnapRAID).
- **No snapshots.** ext4 doesn't have them; we're not adding LVM-snap or Btrfs for v1. "Rollback this app update" is implemented at the brain level via volume copy-on-update, not at the filesystem.
- **No quotas.** Apps and users share the data drive's space. The brain surfaces usage; it doesn't enforce per-app caps.
- **No SMART monitoring or scheduled scrubbing surfaced in v1.** SMART data is collected; degraded-disk warnings ship, but the rich monitoring UI is later.
- **No automatic data-drive replacement workflow.** "My data drive is failing, walk me through swapping it" is a parity-era feature. v1 answer: restore from off-box backup.

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
