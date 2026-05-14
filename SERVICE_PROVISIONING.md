# malmo Service Provisioning

> Working spec for how malmo provides shared infrastructure to apps and how OS-level integrations are exposed. Companion to `SPEC.md`, `CONTROL_PLANE.md`, `APP_MANIFEST.md`.

## The three tiers

Every "service" on a malmo box falls into one of three tiers, distinguished by **how deeply it integrates with the OS**. The tier determines the design.

### Tier 1 — Managed data services

Pure containers with persistent state: **Postgres, Redis** (v1). Later: MariaDB, MongoDB, others as demand justifies.

- Run as containers, fully isolated.
- Brain owns lifecycle, schema, credentials.
- Apps consume via `services:` declarations in the manifest; brain injects credentials as env vars.
- Backups are first-class — brain dumps state at backup time, restores symmetrically.
- Invisible to the user.

### Tier 2 — OS integrations

Things that need **deep system integration** and affect the whole box's behavior: **Tailscale, Samba/SMB, DLNA/UPnP** (v1 candidates), plus future entries like Pi-hole-style ad-blocking, exit-node VPNs, network printer sharing.

These cannot be regular apps because:
- They need privileged capabilities (`NET_ADMIN`, `/dev/net/tun`, raw sockets, host networking).
- They affect *other* apps' network/storage/visibility, not just themselves.
- Misconfiguration has system-wide blast radius.
- They have authentication flows external to malmo (e.g., Tailscale's browser auth).
- Their updates belong with the OS, not the app store.

**Home: malmo Settings UI**, not the App Store. **Curated by us** — no third-party Tier-2 modules in v1.

**Implementation is locked: native Debian packages, managed under systemd, with admin UIs surfaced inside the malmo dashboard.** No Tier-2 service runs in a Docker container; no Tier-2 service exposes its upstream admin UI at its own subdomain. The user-facing surface is "Settings → Tailscale" (a malmo-built UI on `malmo.local/settings/tailscale`), not "Install Tailscale from store" or "open the Tailscale admin UI at tailscale.malmo.local." See `DECISIONS.md` 2026-05-14 and `AUTH.md` for the reasoning chain.

### Tier 3 — Regular apps

Everything else. Run with whatever permissions they declared in their manifest. The 99% case.

## Mental model: how do I install software on malmo?

1. **Standard case:** install from app store (Tier 3).
2. **Need Postgres / Redis:** the manifest just declares it. Brain provides it (Tier 1, invisible).
3. **Need Tailscale / SMB / VPN exit:** toggle in Settings (Tier 2, curated).
4. **Power user with a custom container:** paste a compose file (Tier 3 custom door).
5. **Genuinely advanced:** SSH in, do whatever — but it's not the supported path.

No `apt`, no terminal-by-default. The OS surface area is the UI.

---

## Tier 1 — Managed data services

### Catalog (v1)

- **Postgres** — versions 15 and 16. Most modern self-hosted apps target Postgres.
- **Redis** — version 7. Caching, sessions, queues.

We add new types when **3+ store apps actually want them**, not before. Each new type is real ongoing operational complexity (backup integration, version management, schema isolation).

Plausible v2+ additions: MariaDB (some apps require it specifically — Nextcloud, WordPress), MongoDB (common in modern self-hosted apps).

### Provisioning protocol — end to end

#### At app install

1. Brain reads the manifest, sees `services.database: { type: postgres, version: "15" }`.
2. Brain checks if a Postgres-15 instance is already running.
   - **Lazy spinup:** if not, start one now. Idle versions don't run.
3. Brain connects to that Postgres-15 instance as superuser and creates:
   - a database named `<app-id>_<random_suffix>` (e.g., `photoprism_a4f7`)
   - a role with a randomly-generated password
   - grants the role full privileges on that database, *only* that database
4. Brain stores the credentials in its SQLite, encrypted at rest.
5. At app start, brain injects env vars into the app's container:
   ```
   MALMO_SERVICE_DATABASE_HOST=postgres-15.malmo.internal
   MALMO_SERVICE_DATABASE_PORT=5432
   MALMO_SERVICE_DATABASE_NAME=photoprism_a4f7
   MALMO_SERVICE_DATABASE_USER=photoprism_a4f7
   MALMO_SERVICE_DATABASE_PASSWORD=<generated>
   MALMO_SERVICE_DATABASE_DSN=postgres://photoprism_a4f7:...@postgres-15.malmo.internal:5432/photoprism_a4f7
   ```
6. The app's compose maps these to whatever variables the app actually expects (per `APP_MANIFEST.md` — naming convention is **app-defined**).

#### At app uninstall

1. Run app's `pre_uninstall` hook (e.g., final dump if it wants).
2. Drop the database, drop the role. Clean slate — no leftover schemas.
3. If this was the last app using the Postgres-15 instance, mark it for shutdown after a grace period (e.g., 12 hours). Avoids spin-up churn if the user is reinstalling.

#### At backup

1. Run app's `pre_backup` hook (let it quiesce / dump app-internal state).
2. Brain runs `pg_dump --format=custom` on that app's database.
3. Dump is included in the app's backup archive alongside its data volumes.
4. Run app's `post_backup` hook.

Per-app dumps mean per-app restore — restoring one app doesn't disturb others sharing the same Postgres instance.

#### At restore

1. Brain ensures the right Postgres major version is running.
2. Brain recreates the app's database and role with the credentials from the backup.
3. Brain pipes `pg_restore` from the dump.
4. App starts, reads injected env vars (which now point to the restored DB), resumes.

#### At app update — same major version

Nothing special. Same DB, same credentials.

#### At app update — different major version (cross-version migration)

When the new app version's manifest declares a different major (e.g., now needs Postgres 16 instead of 15):

1. Brain auto-takes a backup of the app's current DB before doing anything.
2. Brain spins up the new major version instance if not already running.
3. Brain `pg_dump` from old → `pg_restore` into new.
4. App starts pointed at the new instance.
5. Old DB and role on the previous version are dropped.

**Auto-migrate is the policy** — happens transparently as part of the app update. The pre-migration backup is the safety net; if the migration fails, malmo rolls back to the old version + restored DB and surfaces the failure to the user. We accept the responsibility of getting this right; the alternative (force every cross-version app update through a manual user prompt) creates worse UX for the non-technical audience.

### Network architecture

- Each managed service instance runs on a dedicated internal Docker network: `malmo-svc-postgres-15`, `malmo-svc-redis-7`, etc.
- Apps that declared a service in their manifest are attached to the matching network at start time.
- Internal DNS: `postgres-15.malmo.internal` resolves **only on networks where that service is reachable**.
- Apps **cannot reach managed services they didn't declare**. Network membership is the enforcement mechanism, not a software allowlist.

### Per-app isolation in shared instances

One Postgres-15 instance serves many apps. Each app sees only:
- Its own database.
- Its own role with privileges on that database only.

Enforcement is via standard Postgres role/grant mechanics — not separate instances. Cleaner resource use, simpler operations.

If we later need stronger isolation (security-sensitive app, regulatory requirement), we can add a `services.database.isolated: true` manifest field that forces a dedicated instance for that app. **Not in v1.**

### Versioning

- Multiple major versions can coexist (Postgres 15 and 16 running side-by-side).
- Brain spins up versions only when an app actually requests them.
- Brain shuts down versions when the last app using them is uninstalled, after a grace period.

### Storage tier for managed services

- The shared Postgres / Redis data lives on **fast tier** if available, falling back to normal.
- Apps don't get a say — this is OS-policy, not per-app config.

---

## Tier 2 — OS integrations (v1)

Three integrations targeted for v1. All three have clear demand, established implementations, and bounded scope.

### Tailscale

- Settings → Network → Tailscale.
- Installed as the upstream `tailscale` Debian package. `tailscaled` runs under systemd on the host.
- Malmo's UI at `/settings/tailscale` is a thin wrapper over host-agent operations (`tailscale up`, `tailscale status`, etc.).
- User clicks "Sign in"; brain triggers `tailscale up` via host-agent, which prints a one-time auth URL. The dashboard surfaces the URL as a button that opens Tailscale's standard browser auth flow.
- Once joined, the box is on the user's tailnet.
- Apps that declare `permissions.tailscale: true` (manifest perm) are reachable via Tailscale's MagicDNS from any device on the user's tailnet.
- **Coexists with malmo's built-in mesh.** Two separate networks. A user might use the malmo mesh for "people I share photos with" and personal Tailscale for "all my own machines."
- Tailscale account is between the user and Tailscale Inc. — malmo doesn't broker auth.

### Samba / SMB

- Settings → Sharing → Network shares.
- Installed as the upstream `samba` Debian package. `smbd` and `nmbd` run under systemd on the host.
- Exposes the user's storage pools (e.g., `photos`, `documents`) over SMB so Windows / macOS / Linux clients can mount them as network drives.
- Malmo's UI at `/settings/shares` lets the user pick which pools to expose, with read-only or read-write toggles and optional per-malmo-user credentials. Brain generates `/etc/samba/smb.conf` and asks host-agent to `systemctl reload smbd`.
- Critical for the "I plug in malmo and want it as a NAS for my laptop" use case.

### DLNA / UPnP media streaming

- Settings → Sharing → Media streaming.
- Exposes media in the `photos` / `videos` pools over DLNA so smart TVs and game consoles can browse and play them.
- Often folded into specific apps (Plex, Jellyfin) but a lightweight built-in option covers the "I just want to play videos on my TV" non-app case.
- May be deprioritized if Jellyfin coverage in the app store is solid at launch; revisit closer to v1.

### What makes something a Tier-2 candidate

- Privileged capabilities required.
- System-wide effect.
- External authentication flow.
- Curated by us — committed to maintenance.
- **Available as a Debian package (or packageable by us as a `.deb`).** If a Tier-2-shaped integration only ships as a Docker container, we either (a) don't support it in v1, or (b) accept a one-off Docker-with-extra-caps path for that specific case. Most viable Tier-2 candidates have first-class Debian packaging from upstream.

We don't accept third-party Tier-2 contributions in v1. Adding a new Tier-2 integration is an OS feature, not an app submission.

---

## Locked decisions

- **Three-tier model:** managed data services / OS integrations / regular apps.
- **v1 Tier-1 catalog:** Postgres (15, 16) and Redis (7). Add types only when 3+ store apps justify it.
- **v1 Tier-2 list:** Tailscale, Samba/SMB, DLNA/UPnP (DLNA possibly deprioritized).
- **Shared instances for Tier 1.** One Postgres-15 instance serves many apps; isolation via Postgres roles/DBs.
- **Lazy spinup.** Tier-1 instances start when first needed, shut down with a grace period after the last app using them is uninstalled.
- **Cross-version migration: auto-migrate** with an automatic pre-migration backup as the rollback safety net. No prompts.
- **Network isolation:** apps reach Tier-1 services only via dedicated Docker networks; no manifest declaration → no network membership → no reachability.
- **Env-var injection:** stable `MALMO_SERVICE_*` names; app maps them in its compose to whatever it actually expects (per `APP_MANIFEST.md`).
- **Tier 2 is curated, not open.** No third-party Tier-2 in v1.
- **Tier 2 runs as native Debian packages under systemd**, not as Docker containers. The admin UI lives in the malmo dashboard at `/settings/<service>/*` — no upstream admin UI is exposed at its own subdomain. Tier 2 updates ride apt.

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
