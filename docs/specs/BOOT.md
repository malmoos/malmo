# molma Boot

> Working spec for molma's boot sequence — how the box goes from power-on to "dashboard reachable," and what happens when it can't. Companion to `STORAGE.md` (disks, encryption, mount layout), `CONTROL_PLANE.md` (what runs once boot is complete), `HEALTH.md` (how anomalies surface in the running brain), `FIRST_RUN.md` (one-time setup that happens on first boot), `TESTING.md` (how this is validated).

## What this doc owns

The ordered chain of systemd units that takes a molma box from kernel-up to a user-facing dashboard. The synthetic target that signals "storage assembly attempted" (`molma-storage-ready.target`). The very narrow conditions under which the box routes to `molma-recovery.target` instead of continuing to a degraded but reachable dashboard.

What this doc does *not* own:

- Disk layout, LUKS keyslots, TPM PCR policy → `STORAGE.md`
- `host-agent` systemd unit shape (Type=notify, watchdog, hardening) → `CONTROL_PLANE.md`
- How storage / state / version anomalies surface in the dashboard, what they block, how they're remediated → `HEALTH.md`
- First-run wizard flow → `FIRST_RUN.md`
- The recovery dashboard UI itself → deferred to `RECOVERY.md` (`NEXT.md`)

## Design stance — boot is best-effort, not strict-gate

For molma's audience (non-technical home users), a box that refuses to boot is a worse failure than a box that boots into a degraded state with a banner explaining what's wrong. A user cannot debug a dark box without help; they *can* read a banner and click a button.

The boot chain is therefore designed to **come up if it possibly can**, even when storage anomalies are detected. Anomalies become *findings* the brain reads at startup and converts into health issues (see `HEALTH.md`). The user sees them, the UI blocks the dangerous operations, and remediation happens from the dashboard.

The systemd-level recovery target (`molma-recovery.target`) exists for the small set of failures where the brain genuinely cannot run — there's no UI to surface anything from, so a static rescue page is the only option. Everything else flows through degraded mode.

## The chain

```
kernel
  └─ initramfs
     └─ LUKS unlock (root drive) via TPM2 PCR 7
        └─ root mounted
           └─ systemd userspace starts
              │
              ├─ molma-storage-ready.target              ← best-effort assembly target
              │     Wants:
              │       • cryptsetup@data.service          (if marker present)
              │       • srv-molma.mount                  (if data drive present)
              │       • srv-molma-mergerfs.mount         (mergerfs union)
              │       • home-<user>.mount, …            (bind mounts, per user)
              │       • var-lib-molma.mount              (bind mount)
              │     After:
              │       • molma-storage-verify.service     (reporter — writes findings to /run/molma/health/)
              │
              ├─ nftables.service                        (Before=docker.service)
              ├─ network-online.target                   (scoped to primary iface)
              │
              ├─ docker.service                          (After=molma-storage-ready.target Wants=molma-storage-ready.target)
              │     └─ host-agent.service               (Type=notify, After=docker; reads /run/molma/health/)
              │         └─ molma-brain container       (launched by host-agent; reads health findings)
              │             └─ molma-caddy container   (launched by brain)
              │
              ├─ Tier-2 host services                    (After=molma-storage-ready.target Wants=…)
              │     • avahi-daemon.service
              │     • smbd.service                       (drop-in: refuse writes if storage findings flag it)
              │     • tailscaled.service
              │     • sshd.service                       (off by default; AUTH.md)
              │
              └─ molma-prepare-wizard.service            (oneshot, runs if not bootstrapped)
```

**Key change from a strict-gate model: `Wants=` not `Requires=` on the storage target.** A failure in any storage unit does not prevent host-agent or the brain from starting. The brain comes up, reads the findings, and raises the corresponding health issues. The user sees the dashboard.

## The storage-ready target — best-effort assembly

`molma-storage-ready.target` is the milestone for "storage assembly has been attempted." It is *not* a strict precondition for the brain. Its job is to:

1. Trigger the LUKS unlock of the data drive (only if the box has one enrolled — see `STORAGE.md` # Data drive enrollment marker).
2. Trigger `/srv/molma` mount (or the OS-drive fallback at Level 0).
3. Trigger the mergerfs union mount over the data branches (when a data drive is present).
4. Trigger all bind mounts for `/home/<user>` and `/var/lib/molma`.
5. Run `molma-storage-verify.service` (`Type=oneshot`) — the **reporter**.

The reporter does not gate anything. It writes its findings to `/run/molma/health/storage.json`:

```
{
  "checked_at": "2026-05-16T08:14:50Z",
  "findings": [
    { "id": "data-drive-missing", "severity": "error", "details": "..." }
    // …or empty array if storage looks healthy
  ]
}
```

What it checks (the canary check, the enrollment-marker match, the device backing the bind mounts — see `STORAGE.md` # Storage canary) becomes findings, not failures. host-agent reads this file on startup and forwards it to the brain; the brain converts findings into health issues per `HEALTH.md`.

**Why this is the right shape:** the historical alternative was a strict gate that refused to start molma userspace if storage looked wrong. That defended against the rare silent-wrong-tree corruption class at the cost of bricking the box for the much more common "drive came unplugged" or "user replaced the data drive" cases. The new shape lets the brain refuse the dangerous operations (writes to user data, app installs) while still coming up to tell the user what's wrong and let them fix it.

### What downstream services do

- **`docker.service`** orders `After=molma-storage-ready.target Wants=molma-storage-ready.target`. Docker comes up even if storage assembly partially failed — host-agent and brain handle the consequences. Docker's own data root issue (see below) is mitigated by Docker checking the data root path is on the expected filesystem before initializing.
- **`host-agent.service`** orders `After=molma-storage-ready.target docker.service`, `Wants=` both. Reads `/run/molma/health/storage.json` after startup and forwards findings to the brain via the existing protocol.
- **`smbd.service`** drop-in orders `After=molma-storage-ready.target Wants=…`. Comes up regardless. When the brain has a `blocks_writes` health issue active, the brain calls host-agent to reload Samba with a read-only share config. Read access stays available even when the data drive is degraded.
- **`tailscaled.service`** doesn't need storage at all; the ordering is just for predictable boot sequencing.
- **`avahi-daemon.service`** is off the critical path for dashboard reachability. It depends on `network.target` (not `network-online.target`) and starts as soon as a link is up, so `molma.local` resolves before NTP, storage, Docker, or the brain. App-slug records are written into `/etc/avahi/services/` by the install reconciler at app-install time (`DISCOVERY.md`, `APP_LIFECYCLE.md`); Avahi watches the directory and re-announces on every boot for free. If Avahi crashes, the brain raises `mdns-down` (`HEALTH.md`) but nothing else degrades.

**Rule: services use `Wants=` for the storage target so a partial-assembly failure surfaces as a runtime degraded state, not a boot block. Services that touch user data must consult the brain's health state before writing — the brain is the single source of truth for "is it safe to write right now."**

## Failure → recovery target — the narrow cases

`molma-recovery.target` exists for the small set of failures where the brain genuinely cannot run. There's no dashboard to surface health issues in, so a separate static rescue page is the only option.

**Two triggers, and only two:**

| Trigger | Cause | Recovery action |
|---|---|---|
| TPM2 unseal failure on the root drive | firmware update, Secure Boot policy change, dbx update, TPM cleared | Box can't boot at all unattended; enter LUKS recovery passphrase at the console (per `STORAGE.md`) |
| host-agent crashloop (5 starts in 60s) past `StartLimitBurst` | host-agent binary itself broken (failed update, corrupt image) | `molma-recovery.target` activates; static page on port 80 offers "Roll back to previous host-agent version" |

That's the entire recovery-target surface. Everything else — drive missing, drive wrong, canary mismatch, brain DB corrupt, version mismatch — flows through the brain's health-issue mechanism (see `HEALTH.md`). The brain is running, the dashboard is reachable, the user sees a banner with an action.

**`OnFailure=molma-recovery.target` is set only on `host-agent.service`.** Not on `molma-storage-verify.service` (it's a reporter, doesn't fail). Not on individual mount units (they can fail without bricking the box).

### What recovery target serves

`molma-recovery.service` is a tiny static-page server bound to port 80, independent of `/var/lib/molma` and the data drive (binary lives on the OS drive, serves from `/usr/lib/molma/recovery/`). It publishes its own mDNS name (`molma-recovery.local`) so the user can find it from another device on the LAN — avahi is not gated behind the storage target for this reason.

The full recovery UI is deferred to `RECOVERY.md` (`NEXT.md`). The boot-level decision is just: **on host-agent crashloop, route to recovery target with a one-button rollback.**

## Ordering rules — the non-obvious calls

### `nftables.service` runs `Before=docker.service`

Docker writes its own iptables/nftables rules at startup for container NAT and forwarding. molma's rules live in a dedicated `inet molma` table (separate family/table from Docker's). Running before Docker ensures our table is in place when Docker brings up container networking; the two never touch each other's rules.

Cross-ref: `MOLMA_NETWORK.md` and `USERS_AND_GROUPS.md` for what those rules enforce (SSH/SMB scoping to LAN + mesh).

### NetworkManager owns the network stack

molma uses **NetworkManager** for all interfaces — ethernet, WiFi, and any future bridge or VPN. Not systemd-networkd. The decisive points are that WiFi is a first-class supported case (`FIRST_RUN.md` # Step 1 — Network) and that host-agent needs a DBus-accessible API for scan / connect / state-change events to drive the dashboard's network panel; systemd-networkd has neither (WiFi requires a separately-managed `wpa_supplicant`, and there is no DBus surface for SSID scan). Running both is split-brain over the routing table, DNS, and `network-online.target`. See `DECISIONS.md` 2026-05-18.

NM-specific tuning molma applies:
- NetworkManager's internal `dnsmasq` plugin is **off**; DNS is plain `/etc/resolv.conf` written by NM from DHCP. No internal resolver that would fight Docker's embedded DNS or the host's recursive resolver.
- WiFi MAC randomization is **off** by default — DHCP reservations on the user's router need a stable MAC. Toggleable from the network panel.
- NetworkManager does not manage Docker's bridges. NM's `unmanaged-devices` config excludes `docker0`, `br-*`, `veth*`, and the macvlan parent interface used by `lan: true` apps. NM and Docker do not touch each other's interfaces.

### `network-online.target` is scoped to the primary connection

`NetworkManager-wait-online.service` declares "online" when *any* configured connection has finished activating. On boxes with two NICs or WiFi+Ethernet, that races or hangs unpredictably. molma pins this to a single primary connection:

- Each NM connection profile carries `connection.required-for-network-online`. Exactly one connection has it set to `true` at a time — the "primary" connection.
- The primary is chosen at first-run (ethernet preferred when present and carrier-up; WiFi otherwise) and re-pinned by host-agent when the user explicitly switches in the dashboard. Multiple ethernet NICs use `connection.autoconnect-priority` to break ties; secondary interfaces are best-effort.
- A WiFi-only box has its WiFi connection as primary. This is the supported case for laptops in the pantry without an ethernet jack.

This matters because Caddy ACME, mDNS publishing, and dashboard reachability all depend on the box actually being on the LAN before they start. If the primary connection fails to come up within the timeout, the brain still starts and raises a `lan-unreachable` health issue (warning, blocks nothing).

### `docker.service` `Wants=` the storage target, not `Requires=`

Docker's data root (`/var/lib/docker`) lives under the bind-mounted state tree on a healthy box. If the bind isn't in place, Docker's data root falls back to its raw OS-drive path — historically, that creates a shadow data root that gets clobbered when the bind eventually lands. Mitigation: Docker is configured with `data-root: /var/lib/docker` (the bind target). If the bind isn't there, Docker initializes against the OS-drive path under the same name — and the brain detects this via a separate health check (`docker-data-root-misplaced`) and refuses app operations until storage is sane.

This is the price of softening the gate: the brain has to know about and detect more edge cases. The benefit is the dashboard always comes up.

### host-agent waits for Docker to be *actually* ready, not just systemd-ready

Docker's systemd notification fires when the daemon socket is up — **not** when it has finished reconciling existing containers with restart policies. host-agent's startup includes a readiness probe (`docker ps` succeeds and the result is stable for ~2s) before any Docker operation. This is implementation discipline, not a unit-level dependency, but it's load-bearing and easy to forget.

### Time sync before Caddy ACME

The brain (which launches Caddy) orders `After=time-sync.target`. ACME refuses to issue or use certs whose validity window doesn't include "now"; a box booting with a stale BIOS clock and not-yet-synced NTP fails to renew. The dependency is cheap (`systemd-timesyncd` usually settles in seconds) and the failure mode is annoying (TLS broken for hours until the next retry).

### Tier-2 services get drop-ins, not replacement units

Upstream Debian ships `smbd.service`, `avahi-daemon.service`, etc. molma extends them via `/etc/systemd/system/<unit>.service.d/molma.conf` drop-ins that add `After=molma-storage-ready.target Wants=molma-storage-ready.target` (and any other molma-specific ordering). Apt updates to the base unit don't fight us. **Drop-ins use `Wants=`, not `Requires=`, for the same reason as the rest of the chain.**

### The bootstrap marker is written by the brain, not by systemd

`molma-prepare-wizard.service` runs as a oneshot when `/var/lib/molma-state/.bootstrapped` does not exist. Its job is "ensure the brain is reachable and the wizard can run" — *not* "the box is bootstrapped." The `.bootstrapped` marker is written **by the brain** at the end of the wizard, not by systemd at oneshot exit. This avoids the failure mode where the user closes the wizard halfway and the next boot believes setup is complete.

### Hang protection on the storage chain

`molma-storage-verify.service` has `TimeoutStartSec=60s`. A hang in the verifier (e.g., a wedged disk driver) trips the timeout, the reporter writes a `storage-verify-timeout` finding, and the brain raises the corresponding health issue. The boot does not stall.

Individual mount units carry a `TimeoutSec=30s`. A mount that hangs forever is treated as a mount that failed — the chain moves on, the finding is recorded, the brain handles it.

## What this doc deliberately doesn't pin

- **TPM2 PCR policy specifics** → `STORAGE.md` # Encryption posture.
- **Data drive enrollment marker schema** → `STORAGE.md` # Data drive enrollment.
- **Health issue catalog and remediation tiers** → `HEALTH.md`.
- **Recovery dashboard UX (the page served by `molma-recovery.target`)** → `RECOVERY.md`, deferred (`NEXT.md`).
- **Boot test infrastructure** → `TESTING.md`.
- **host-agent unit specifics** (Type=notify, watchdog, hardening) → `CONTROL_PLANE.md` # Locked: host-agent runs under systemd.

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
