# malmo Service Provisioning

> Working spec for how malmo provides shared infrastructure to apps and how OS-level integrations are exposed. Companion to `SPEC.md`, `CONTROL_PLANE.md`, `APP_MANIFEST.md`.

## The three tiers

Every "service" on a malmo box falls into one of three tiers, distinguished by **how deeply it integrates with the OS**. The tier determines the design.

### Tier 1 — Managed data services

Pure containers with persistent state: **Postgres, MySQL, MariaDB, Redis** (v1). Later: MongoDB, others as demand justifies.

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

**Implementation is locked: native Debian packages, managed under systemd, with admin UIs surfaced inside the malmo dashboard.** No Tier-2 service runs in a Docker container; no Tier-2 service exposes its upstream admin UI at its own subdomain. The user-facing surface is "Settings → Tailscale" (a malmo-built UI on `malmo.local/settings/tailscale`), not "Install Tailscale from store" or "open the Tailscale admin UI at tailscale.local." See `DECISIONS.md` 2026-05-14 and `AUTH.md` for the reasoning chain.

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
- **MySQL** — versions 8.0 and 8.4 (upstream LTS series; 8.0 is past Oracle EOL but kept because Ghost pins it specifically). Some apps speak only the MySQL dialect — Ghost, Kimai.
- **MariaDB** — versions 10.11 and 11.4 (upstream LTS series). Some apps require it specifically — Nextcloud, WordPress.
- **Redis** — version 7. Caching, sessions, queues.

MySQL and MariaDB share one wire protocol and SQL dialect, so they are two `type` values backed by one provisioning path; the per-engine deltas are the image pin, the client binary names, the root-password env var, and the readiness probe (`DECISIONS.md` 2026-06-09).

We add new types when **3+ store apps actually want them**, not before. Each new type is real ongoing operational complexity (backup integration, version management, schema isolation).

Plausible v2+ additions: MongoDB (common in modern self-hosted apps).

### Provisioning protocol — end to end

> **Implementation status (v1, 2026-06-05 — `docs/progress/managed-services-postgres.md`; MySQL family 2026-06-09 — `docs/progress/managed-services-mysql.md`; Redis 2026-06-13 — `docs/progress/managed-services-redis.md`).** Built: Postgres, MySQL, MariaDB, and Redis provisioning end-to-end (lazy spinup, per-app credential, `MALMO_SERVICE_<NAME>_*` injection, drop-on-uninstall). The brain provisions via **`docker exec` of the service's own client** (`psql` / `mysql` / `mariadb` / `redis-cli`) — not a client connection of its own — so the control plane never joins the service network (`DECISIONS.md` 2026-06-02, 2026-06-05). Each service is a brain-owned compose project with a fixed `container_name` (`malmo-svc-postgres-15`, the exec handle) and an in-network DNS alias (`postgres-15.malmo.internal`, the DSN host); dots in a version fold to dashes in every derived name (`malmo-svc-mysql-8-0`, `mysql-8-0.malmo.internal`) because compose project names reject dots. MySQL-family DSNs are `mysql://…:3306/…` for both engines (one wire protocol). **Redis** has no database: the per-app credential is an **ACL user with full keyspace** (`redis://…:6379`, no DB path; the credential is the isolation boundary, `DECISIONS.md` 2026-06-13), provisioned by `ACL SETUSER` and persisted to an external aclfile on the data volume (`ACL SAVE`) so it survives a restart — Redis ACLs are config, not keyspace. **Deferred:** the grace-shutdown timer (services stay running after the last consumer uninstalls), backup/restore + cross-version migration (gated on the backup design), and at-rest encryption of the stored superuser + per-app passwords (plaintext today — folds into `NEXT.md` # App-secret injection hardening). See `NEXT.md` for each.

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

### Env-var injection — the full family

Managed-service credentials are one of four `MALMO_*` injection mechanisms the brain stamps into an app's environment, all sharing the same contract: **the brain owns a stable variable name; the app's compose maps it to whatever it expects.** The app never hardcodes a malmo-specific value and stays portable.

| Variable | Source | Stability |
|---|---|---|
| `MALMO_SERVICE_*` | Managed-service credentials (this doc) | Per provisioning; rotates only on re-provision |
| `MALMO_FOLDER_<NAME>` | In-container path of a bound use-case folder (`APP_MANIFEST.md` # folders) | Fixed (`/malmo/<folder>`) |
| `MALMO_DATA_DIR`, `MALMO_APP_URL`, `MALMO_INSTANCE_ID` | Per-instance facts the brain knows (data root, routed URL, id) | Fixed for the instance |
| `MALMO_SECRET_<NAME>` | A per-app random secret the brain **generates** from a manifest `secrets:` declaration | **Generated once at install, persisted, re-emitted verbatim** — never re-rolled |
| `MALMO_MAIL_*` | The outgoing-mail provider the instance is bound to (this doc # BYO outgoing mail) | Stamped at install / rebind; absent when unbound |

The secret case is the only one the brain *creates* rather than *reflects*: for each `secrets: [{name, bytes?}]` entry (`APP_MANIFEST.md` # D2) it draws `bytes` (default 32, floor 16) from a CSPRNG, base64url-encodes them, and persists the value alongside the instance. Stability is load-bearing: a token-signing secret (e.g. `BETTER_AUTH_SECRET`) that changed on restart would invalidate every live session, so the value is read back from storage on every `.env` rewrite, not regenerated. Security hardening of this path (env-var delivery surface, at-rest encryption, rotation) is open — `NEXT.md` # App-secret injection hardening.

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

**Redis** has no database to scope a role to, so the per-app unit is an **ACL user with full keyspace** (`ACL SETUSER … ~* &* +@all -@admin -flushall -flushdb -swapdb`): each app gets its own credential — revocable on uninstall, and unable to touch the ACL system, `CONFIG`, `SHUTDOWN`, or replication (`-@admin`) — but the keyspace is shared. The isolation boundary is the credential (no unauthenticated access; an app with no Redis declaration never joins the network), not a key partition. This is a stronger boundary than a logical-DB-number split, which has no auth boundary between apps (`DECISIONS.md` 2026-06-13). `-flushall -flushdb -swapdb` removes the keyspace-destruction commands by name (rather than the blunt `-@dangerous`, which would also strip `INFO`/`KEYS`/`SORT` that ordinary clients call) so a single compromised or buggy app can't wipe the shared keyspace every other app reads from. The shared keyspace means one app can still *read* another's keys; per-app key **confidentiality** is the deferred isolation hardening (`NEXT.md` # Managed-service per-app key isolation), whose clean form is the `isolated: true` dedicated-instance escape hatch below.

If we later need stronger isolation (security-sensitive app, regulatory requirement), we can add a `services.database.isolated: true` manifest field that forces a dedicated instance for that app. **Not in v1.**

### Versioning

- Multiple major versions can coexist (Postgres 15 and 16 running side-by-side).
- Brain spins up versions only when an app actually requests them.
- Brain shuts down versions when the last app using them is uninstalled, after a grace period.

### Storage tier for managed services

- The shared Postgres / Redis data lives on **fast tier** if available, falling back to normal.
- Apps don't get a say — this is OS-policy, not per-app config.

---

## BYO outgoing mail (`MALMO_MAIL_*`)

> Implemented 2026-06-12 (`docs/progress/byo-outgoing-mail.md`, issue #122).

Many apps want to *send* email — password resets, reminders, invites. malmo does not run a mail server, relay, or smarthost: **the admin brings their own SMTP account** (Fastmail, a Gmail app password, the ISP's smarthost), and the brain injects its credentials into the apps the admin chooses. The app dials the provider itself over its declared `internet` permission; no mail traffic flows through malmo infrastructure.

**Providers** are box-level records, managed in Settings → Outgoing email (admin-only; create/update/delete are elevation-class, `USERS_AND_GROUPS.md` # Elevation in the UI): a label, SMTP host + port, optional username/password, a from address, and an encryption mode (`none` | `starttls` | `tls`). A synchronous test-send (`POST /mail-providers/{id}/test`) validates a provider end to end — dial, TLS, auth, one delivered message — before any app depends on it. Passwords are write-only at the API: requests carry them, responses never echo them. (At-rest they are plaintext in the brain's SQLite today, same status as managed-service credentials — folds into `NEXT.md` # App-secret injection hardening.)

**Binding** is per-instance: a mail-capable app (manifest `mail:` block, `APP_MANIFEST.md` # D3) is bound to at most one provider. The install dialog offers the picker (None is the default and always valid; a sole registered provider is preselected), and the binding is changeable later from the app's detail page — by admins for any app, by a member for their own personal instances, same authorization as stop/start. Unbound means **nothing is injected**: the app must run with email features off, which is why v1 admits only `optional: true` manifests.

A bound instance's `.env` carries the discrete fields plus a Symfony-style DSN, since apps differ in what they consume:

```
MALMO_MAIL_HOST=smtp.fastmail.com
MALMO_MAIL_PORT=465
MALMO_MAIL_USER=box@example.com
MALMO_MAIL_PASSWORD=<stored>
MALMO_MAIL_FROM=box@example.com
MALMO_MAIL_ENCRYPTION=tls
MALMO_MAIL_USE_TLS=false
MALMO_MAIL_USE_SSL=true
MALMO_MAIL_DSN=smtps://box%40example.com:...@smtp.fastmail.com:465
```

The DSN scheme is `smtps://` for implicit TLS and `smtp://` otherwise (SMTP-URL consumers negotiate STARTTLS opportunistically; an app needing the exact mode reads `MALMO_MAIL_ENCRYPTION`). `MALMO_MAIL_USE_TLS` / `MALMO_MAIL_USE_SSL` are boolean projections of that mode (STARTTLS vs implicit TLS, at most one true) for apps that take two separate flags — e.g. Django's `EMAIL_USE_TLS` / `EMAIL_USE_SSL`, which Paperless surfaces — since a compose file can't derive a boolean from the encryption string. Credentials are URL-escaped. The app's compose maps the vars to whatever it expects, per the family contract above — with a compose default for the unbound case so absence degrades cleanly (Kimai: `MAILER_URL: "${MALMO_MAIL_DSN:-null://null}"`).

**Propagation:** env is read at container create, so a rebind re-stamps the `.env` and recreates the instance's containers immediately (stopped instances pick it up at next start). Editing or deleting a *provider* does not re-stamp bound apps: they keep the previously injected values until their next rebind or reinstall — v1 accepts this lag, and the Settings UI says so. Deleting a provider unbinds its apps in the brain's state (the next rebind of each app drops the vars).

**Explicitly not in v1** (deferral, not rejection — `NEXT.md` # Outgoing mail): no malmo-run relay/smarthost, no per-app rate limiting or queue, no inbound mail anything. If email grows more surface (a default box-wide provider, brain-sent notification email riding the same providers), this section promotes to its own `OUTGOING_MAIL.md`.

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
- Exposes two share shapes over SMB so Windows / macOS / Linux clients can mount them as network drives: per-user home (`\\malmo\<user>` → `/home/<user>/`) and household-shared (`\\malmo\shared` → `/srv/malmo/shared/`). See `STORAGE.md` # Cross-device access (SMB).
- Malmo's UI at `/settings/shares` lets each user opt in/out of their own SMB share (off-by-account-by-default per `AUTH.md`). Brain edits `/etc/samba/smb.conf` (specifically `valid users` allowlists) and asks host-agent to `systemctl reload smbd`. Credentials are the user's malmo password — no per-share password.
- Critical for the "I plug in malmo and want it as a NAS for my laptop" use case.

### DLNA / UPnP media streaming

- Settings → Sharing → Media streaming.
- Exposes media in the household `Shared/Photos/` and `Shared/Movies/` folders over DLNA so smart TVs and game consoles can browse and play them.
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

## Post-v1 candidates

Ideas explicitly out of scope for v1, kept here so we don't lose them. Nothing in this section is committed — each entry would need a separate design pass before becoming a locked decision.

The bar for promotion is the same we apply to new Tier-1 types: (1) 5+ apps would actually use it, (2) sharing creates real benefit beyond convenience (security patching, ops integration, user-visible UX), (3) does not require app upstreams to redesign themselves around malmo, (4) bounded API surface.

### Scheduled / deferred jobs

A unified facility for apps to declare periodic or constraint-based background work. Cron-style schedules ("re-index every 24h") and constraint-based dispatch ("run when the box is idle, on AC power") in one shape — Android's `WorkManager` is the model. Apps declare jobs in the manifest; malmo arbitrates execution.

Value:
- Single observability surface — Activity view can show *why your box is loud at 3am* (Immich indexing, Paperless OCR'ing). Synology-tier ops visibility.
- Resource arbitration — don't let five apps kick off heavy jobs simultaneously.
- Power-aware — defer expensive jobs when the laptop-in-the-pantry is on battery.

Caveat: apps with framework-embedded schedulers (Sidekiq-cron, APScheduler, etc.) won't fully migrate; malmo's scheduler covers what apps choose to declare, not all background work.

### Additional managed data services (Tier-1 catalog growth)

Extend the catalog as concrete app demand justifies. We **host the substrates apps already use** rather than inventing new APIs — same shape as Postgres/Redis today.

Plausible additions:
- **MongoDB** — common in modern self-hosted apps.
- **Kafka, RabbitMQ** — queue/streaming *substrates*, if app demand emerges.

(MariaDB graduated from this list to the v1 catalog alongside MySQL — `DECISIONS.md` 2026-06-09.)

We host queue substrates, not queue libraries. Sidekiq (Ruby), BullMQ (Node), Celery (Python), RQ — these are libraries that run *inside the app's own container*, pointed at a substrate we provide (already-managed Redis for most; potentially Kafka or RabbitMQ later). Malmo does not build or expose a queue API of its own.

### Cross-box services (federated state)

The biggest and most differentiated post-v1 idea. None of the home-server OS competitors have a story here.

User-facing pitch: "your grocery list syncs with your partner's box; your photos sync with your parents' box" — without either app author writing networking code.

The shape is unresolved — three plausible technical models, each implying a different developer experience:

- **Master-master replication** (CouchDB / PouchDB). Each box holds a full DB copy; bidirectional eventually-consistent sync. Mature but dated DX; apps work with conflict-resolution documents.
- **CRDT-as-library** (Automerge, Yjs). No central "server" — apps work with CRDT documents directly, sync is peer-to-peer. Modern DX, arguably better-aligned with the "files are first-class" instinct elsewhere in the spec, but storage story is less mature.
- **Local-first SQLite with sync** (cr-sqlite, Turso embedded replicas). Apps see a normal SQL DB; sync at the storage layer. Most familiar API, youngest ecosystem.

These are not interchangeable; the choice constrains what apps can be built on top. Not locked.

The bigger unresolved piece is **cross-box identity and consent**, which is the load-bearing part of any federation story and likely needs its own design doc (sketch: `FEDERATION.md`) before this gets serious. Components: how a user is named across boxes (box-ID + username? email-shape?), how box-B verifies that box-A's claim of "I'm Alice" is real (tied to the mesh's identity model, or separate?), the consent surface in the dashboard, granularity (per-app? per-document?), and revocation — which is genuinely hard once data has propagated. Each of these is its own design problem.

Why this is post-v1:
- Scope is large across multiple unsettled axes (sync model, identity, consent, revocation).
- No concrete apps demand it in 2026 — the self-hosted ecosystem is single-box-shaped. Risk of building rails for users who don't exist.
- Ecosystem seeding: this only pays off if apps adopt the pattern, which likely requires malmo shipping reference apps to demonstrate it.

Locked now: **the malmo mesh is the intended transport for future cross-box services.** Whatever we build later rides on the same Headscale/DERP substrate we ship for personal device access, not a separate network plane.

---

## Locked decisions

- **Three-tier model:** managed data services / OS integrations / regular apps.
- **v1 Tier-1 catalog:** Postgres (15, 16), MySQL (8.0, 8.4), MariaDB (10.11, 11.4), and Redis (7). Add types only when 3+ store apps justify it.
- **v1 Tier-2 list:** Tailscale, Samba/SMB, DLNA/UPnP (DLNA possibly deprioritized).
- **Shared instances for Tier 1.** One Postgres-15 instance serves many apps; isolation via Postgres roles/DBs.
- **Lazy spinup.** Tier-1 instances start when first needed, shut down with a grace period after the last app using them is uninstalled. *(v1: lazy spinup built; grace-shutdown deferred — services stay running.)*
- **Provisioning via `docker exec`, not a brain SQL client.** The brain runs the service's own client (`psql` / `mysql` / `mariadb`) inside the shared container to create per-app databases/roles, so it never joins the service's Docker network — same principle as probing through Caddy (`DECISIONS.md` 2026-06-05). The container has a fixed `container_name` (the exec handle) and an in-network DNS alias (the DSN host).
- **Cross-version migration: auto-migrate** with an automatic pre-migration backup as the rollback safety net. No prompts.
- **Network isolation:** apps reach Tier-1 services only via dedicated Docker networks; no manifest declaration → no network membership → no reachability.
- **Env-var injection:** stable `MALMO_SERVICE_*` names; app maps them in its compose to whatever it actually expects (per `APP_MANIFEST.md`). Same contract for the rest of the `MALMO_*` family (`MALMO_FOLDER_*`, `MALMO_APP_URL`, `MALMO_SECRET_*`).
- **Generated secrets (`MALMO_SECRET_*`):** a manifest `secrets:` declaration makes the brain generate a CSPRNG value once at install, persist it, and re-emit it stably across restarts. The only injected variable malmo creates rather than reflects. Security hardening is open (`NEXT.md` # App-secret injection hardening).
- **Outgoing mail is BYO (`MALMO_MAIL_*`), not a malmo relay.** Admins register external SMTP providers; the brain injects the bound provider's credentials per instance and the app dials the provider itself. No smarthost, no queue, no inbound mail in v1; unbound apps get nothing injected and must run with email off (`mail: optional: true` is the only admitted shape).
- **Tier 2 is curated, not open.** No third-party Tier-2 in v1.
- **Tier 2 runs as native Debian packages under systemd**, not as Docker containers. The admin UI lives in the malmo dashboard at `/settings/<service>/*` — no upstream admin UI is exposed at its own subdomain. Tier 2 updates ride apt.

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
