# malmo Storage Architecture

> Working spec for how malmo lays out storage, what disks the user sees, and how encryption works. Companion to `SPEC.md`, `CONTROL_PLANE.md`, `APP_MANIFEST.md`, `SERVICE_PROVISIONING.md`, `APP_ISOLATION.md`.

## Stance

NAS power users have TrueNAS, Unraid, Proxmox, HexOS. malmo is not trying to be a NAS. The storage story optimizes for the home user who has one or two disks and wants apps and shared folders that just work.

**No NAS vocabulary in the UI.** No "pool," "vdev," "RAID," "parity." The only storage concepts a user sees are **OS drive** and **data drive**.

## Levels of complexity (and the v1 cut)

There's a ladder of how sophisticated a storage layer can be. We deliberately ship the bottom two rungs:

| Level | What it is | Ships in v1 |
|---|---|---|
| 0 | Single drive holds OS + apps + shared data | ✅ floor |
| 1 | OS drive + one data drive | ✅ ceiling |
| 2 | Mergerfs union pool, no parity | ❌ later, additive |
| 3 | Mergerfs + SnapRAID parity | ❌ later, additive |
| 4 | ZFS / Btrfs pools | ❌ out of scope (TrueNAS land) |
| 5 | SSD cache tier in front of HDD pool | ❌ out of scope |
| 6 | Multi-pool with per-pool policies | ❌ out of scope |

The v1 cut covers the realistic home setup: a small NVMe for OS, a big HDD for media. The "I have one drive" and "I have two drives" cases. Three-plus-disk users either pick one as the data drive (others sit unused or as named extra mounts) or wait for Level 2.

The cut is not arbitrary — it's the rung that keeps Level 2 and Level 3 as **additive, non-destructive** upgrades. See "Upgrade path" below.

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

- **Both drives LUKS-encrypted.** Separate keyslots per drive.
- **Auto-unlock via TPM2.** Headless boot survives reboots without a passphrase prompt — non-negotiable for a home server.
- **Recovery passphrase** generated at install, shown to the user once with "write this down or save it somewhere." Required if the TPM is wiped (BIOS reset, motherboard swap) or the drive is moved to another box. Re-displayable from Settings on demand (re-prompts current login).
- **Adding a data drive later** triggers the same flow: format → LUKS → TPM-enroll → mount, recovery passphrase shown.

### Threat model — explicit

- ✅ Defends against **drive removal / drive theft / drive RMA**. A drive that leaves the building is unreadable. This is the realistic threat.
- ❌ Does **not** defend against **whole-box theft**. The TPM is in the box; an attacker with the box has time. Closing this gap means a boot PIN, which breaks unattended boot. v1 accepts the limit. A future "high-security mode" with PIN-on-boot is a Settings toggle.
- ❌ Does **not** defend against **the box admin reading another user's data**. Once LUKS unlocks at boot, every file on the disk is plaintext to root. Filesystem permissions (`0700` per-user pool dirs) protect against the UI surface and other malmo users; they do not stop someone with shell-as-root. **Per-user, admin-resistant privacy is on the roadmap** as a layered fscrypt addition — see "Future: per-user encryption" below.

## Disk roles

The user-visible model is two roles:

### OS drive

Required. Holds the OS, container images, app working data, and managed-service data.

```
/                          (boot drive, ext4 over LUKS)
├── var/lib/malmo/apps/<id>/      app data, container volumes
├── var/lib/malmo/managed/<svc>/  managed-service data (postgres, redis, ...)
└── var/lib/malmo/shared/         shared pools — IF no data drive
                                  → symlink to /mnt/data/shared/ otherwise
```

### Data drive

Optional. Holds **per-user pools** plus a global **shared pool**.

```
/mnt/data/                 (data drive, ext4 over LUKS)
├── users/
│   ├── cindy/             (mode 0700, owned by cindy)
│   │   ├── photos/
│   │   ├── documents/
│   │   ├── videos/
│   │   ├── music/
│   │   ├── downloads/
│   │   └── managed/       (per-(user, app) managed-service data)
│   └── andrei/            (mode 0700, owned by andrei)
│       └── ...
├── shared/                (group-readable; cross-user drop zone)
│   ├── photos/
│   ├── documents/
│   └── ...
└── archived-users/        (soft-deleted users, admin cleans up later)
```

Per-user pools live under `users/<slug>/`, owned by the user (UID in malmo's 3000+ range), `0700`. Each Tier-3 app instance is scoped to one user and only sees its own user's pool (`APP_ISOLATION.md`).

The `shared/` tree is the v1 cross-user sharing surface — drop a file there and every user's file browser sees it. App-level access to it is **deferred** (per-user app instances do not bind-mount it at MVP).

When no data drive is configured, the whole `/mnt/data/` tree lives on the OS drive. When a data drive is added later, the brain copies it across and replaces the source with a symlink. Per-user UIDs and `0700` perms are preserved.

### Anything else

A third or fourth drive plugged in shows up in Settings as an "extra drive." The user can choose to mount it (named, e.g., `extra-1`) and is responsible for what they do with it. This is an escape hatch, not a feature — extra drives don't participate in shared pools, don't get backed up, and apps don't see them by default. Door-2 custom compose can bind-mount them for a power user who knows what they're doing.

## First-run flow

1. Installer detects all attached disks.
2. User picks the **OS drive** (defaults to the smallest non-removable disk, typically the NVMe).
3. If a second non-removable disk is present, user is offered "use this for shared storage?" — yes by default, picks the largest remaining disk.
4. Installer formats both as LUKS + ext4, enrolls each into the TPM, generates a recovery passphrase.
5. User is shown the recovery passphrase once. They confirm they've recorded it.
6. Boot proceeds. Subsequent boots auto-unlock via TPM, no prompt.

Adding a data drive after install is the same flow minus the OS-drive step, accessible from Settings → Storage.

## Upgrade path to Level 2 / Level 3

Both levels are pure additions on top of the v1 layout. Independent ext4 filesystems on each disk are exactly what mergerfs and SnapRAID expect. Encryption is per-disk and stays out of the way.

### Level 1 → Level 2 (mergerfs)

Mergerfs is a userspace FUSE union over existing mounts.

- Add a second data drive (LUKS + ext4 + TPM-enrolled, same first-run-style flow).
- Disks now mount at `/mnt/data1`, `/mnt/data2`.
- Replace `/mnt/data/shared/` with a mergerfs mount that unions `data1/shared` and `data2/shared`.
- Apps see the same `/var/lib/malmo/shared/` symlink. No app change.
- **No data migration.** Existing files stay on disk 1; new writes distribute by policy (most-free-space).

Cost: install mergerfs, write a systemd mount unit, expose a UI flow. Low.

### Level 2 → Level 3 (SnapRAID)

SnapRAID is a userspace tool that computes parity over existing files in-place.

- Add a parity drive (LUKS + ext4 + TPM-enrolled). Must be ≥ the largest data drive.
- Configure SnapRAID's `data` and `parity` pointers, schedule nightly `snapraid sync`.
- **No data migration.** Parity is computed from current state.
- Encryption interaction is fine: SnapRAID reads through the filesystem (plaintext); parity is stored on the encrypted parity drive.

Cost: install snapraid, schedule a timer, expose a UI flow, surface sync status and last-sync recency. Medium — UI has to teach "parity is point-in-time, the last day of changes is at risk."

### Why this is cheap

The on-disk layout doesn't change between v1 and Level 2/3. Each disk stays an independent ext4 filesystem. Mergerfs is a mount layer; SnapRAID is a scheduled job. Adding either is a feature flag, not a rebuild.

A different v1 choice (ZFS, Btrfs multi-device, LVM-thin) would have made Level 2/3 a destructive migration.

## Future: per-user encryption (planned upgrade, not v1)

LUKS-only protects the disk against theft but not users from each other once the box is unlocked. The planned upgrade adds an encryption layer **above** the disk, keyed per user, so even root cannot read another user's files without that user's password.

**Tech.** `fscrypt` — kernel-native ext4 filesystem-level encryption. Each user's pool dir is encrypted with a key derived from their password. Filenames and contents are unreadable without the key. This is the same primitive that powers Chromebook user separation.

**Why we can ship it later, not now.** The v1 architecture is already aligned:

- ext4 ✅ — fscrypt is ext4-native.
- Per-user pool layout ✅ — `users/<slug>/` is exactly what gets encrypted.
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

- **No pooling without parity.** Level 2 by itself is a footgun by default — users will lose photos and blame us. We don't ship it default-off either, because two-drive home boxes that toggle it on without understanding "no redundancy" are exactly the wrong outcome. Wait until Level 3 lands and ship them together.
- **No snapshots.** ext4 doesn't have them; we're not adding LVM-snap or Btrfs for v1. "Rollback this app update" is implemented at the brain level via volume copy-on-update, not at the filesystem.
- **No quotas.** Apps and pools share the data drive's space. The brain surfaces usage; it doesn't enforce per-app caps.
- **No SMART monitoring or scheduled scrubbing surfaced in v1.** SMART data is collected; degraded-disk warnings ship, but the rich monitoring UI is later.
- **No automatic data-drive replacement workflow.** "My data drive is failing, walk me through swapping it" is a Level-3-era feature. v1 answer: restore from off-box backup.

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
