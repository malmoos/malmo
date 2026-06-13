# malmo App Manifest

> Working spec for the `manifest.yml` schema — the contract between an app and the malmo OS. Companion to `SPEC.md`, `CONTROL_PLANE.md`, and `APP_LIFECYCLE.md`.

## Core design principle: one model, two doors

The brain only ever knows about manifests. *Everything* installed on a malmo box has one. The user-facing UX has two entry points:

- **Door 1 — App store.** App author wrote a complete `manifest.yml` + `docker-compose.yml`. One-click install. Full integration (managed services, backup hooks, declared permissions).
- **Door 2 — Custom container.** User pastes/uploads a raw `docker-compose.yml`. The brain **generates a synthetic manifest** with sensible defaults. The app is a first-class citizen — it gets a subdomain, shows in the dashboard, integrates as much as the synthetic manifest allows.

This unification matters because:
- The brain's data model stays simple — one type of thing.
- A power user can paste a compose file today; the synthetic manifest is designed to *graduate* later (add backup hooks, request a managed DB, refine volumes) into a richer manifest of the same schema. **In-product editing of a synthetic manifest is deferred past v1** (`NEXT.md`) — v1's custom flow is install-only, and changing a custom app means uninstall + re-paste. See `DASHBOARD.md` # Door-2 custom container install flow.
- Door-1 is just "we wrote the manifest for you."

## Author philosophy

App authors **adapt their app to run on malmo.** This is an explicit design choice, not an accident.

- We provide thorough, friendly docs and examples.
- We expect authors to make small, well-defined changes — pointing env vars at malmo's injected values, splitting cache from data volumes, declaring permissions honestly.
- We do **not** auto-rewrite the compose file or guess at things. The manifest is the author's contract; if it lies, the app misbehaves and it's on the author.
- For popular OSS apps that don't know malmo exists, we maintain manifests ourselves in the official catalog repo. Same schema, same rules.

## Format

- **YAML.** Same mental space as `docker-compose.yml`. App authors are already in YAML when writing compose; one less context switch.
- **Schema-versioned from v1.** Top-level `manifest_version: 1`. We commit to backward compatibility for at least the previous two major versions. When we change semantics, old-version manifests keep working.
- **Public, versioned spec.** Third-party stores depend on this format. The schema is published, stable, and changes only in versioned increments.

## What's required, what's optional

**Required (the bare minimum to install an app):**
- `id`
- `manifest_version`
- `name`
- `version`
- `compose_file`
- `main_service`
- `main_port`

**Everything else is optional** with sensible defaults that do the right thing. A minimal valid manifest is ~7 lines.

## Field categories

### A. Identity and metadata

For the store and the dashboard UI. The brain mostly doesn't care; the store does.

```yaml
id: photoprism                    # globally unique slug
name: PhotoPrism                  # display
version: 2.4.1                    # app version
manifest_version: 1               # schema version
description:
  short: "Self-hosted photo library with AI tagging"
  long: |                         # markdown allowed
    PhotoPrism is an AI-powered app for browsing,
    organizing & sharing your photo collection...
icon: ./icon.png                  # bundled in the app package
icon_glyph: image                 # optional; Lucide icon name used as the store fallback when no `icon` is bundled (ignored when `icon` is set)
screenshots: [./shot1.png, ./shot2.png]
categories: [media, photos]
author:
  name: PhotoPrism Labs
  url: https://photoprism.app
license: AGPL-3.0
links:
  homepage: https://photoprism.app
  source: https://github.com/photoprism/photoprism
  support: https://docs.photoprism.app
changelog_url: https://github.com/photoprism/photoprism/releases  # optional; used by the "What's new" panel after an update
listed: true                      # optional, default true; `false` pulls the app from the store (hidden from browse + uninstallable) while keeping its manifest in the catalog
```

**`listed`** controls store visibility. Omitted (or `true`) ⇒ the app appears in the browse grid, has a detail page, and can be installed — the normal case. Setting `listed: false` **pulls the app from the store**: it's hidden from browse, its detail page and install paths return 404, but the manifest stays in the catalog directory — it still parses, lints, and serves its icons/screenshots, and an already-installed instance keeps its dashboard card and stays reconcilable (visibility is resolved by id, not via the filtered browse). This is how a `Blocked` or `Rejected` app is withdrawn without throwing away its adaptation work — e.g. an image that can't yet run under the sandbox, parked until the platform gap or upstream fix lands. It is a curation control, not a per-user or per-role one; there is no "show me unlisted apps" path in v1.

### B. Runtime

The minimum to actually launch the thing.

```yaml
compose_file: docker-compose.yml      # standard compose; never modified by malmo
main_service: photoprism              # which compose service is "the app"
main_port: 2342                       # port the main service listens on internally
preferred_slugs: [photos, photoprism] # subdomain priority list; OS picks first free
needs_secure_context: false           # optional; default false. See below.
timezone: system                      # optional; "system" (default) or "utc"
health_probe: /healthz                # optional; enables the "responding" check. See below.
service_user: false                   # optional; default false. Dedicated non-root identity for folderless apps. See below.
```

**`id` and `preferred_slugs`** must be strict kebab-case — lowercase alphanumerics joined by single internal hyphens (`home-assistant` ✓; `whoami-`, `-x`, `who--ami`, `xn--y`, `Foo` ✗). This keeps the `<slug>--<user>` personal-instance scheme parseable (`DASHBOARD.md` # instance naming): no leading/trailing hyphen and no `--` run (which would collide with the owner separator and also covers the reserved `xn--` prefix). Catalog CI and the manifest parser both reject violations.

**`timezone`** controls the container's TZ. Default `system` — the brain bind-mounts `/etc/localtime` and sets `TZ=<system_tz>`, so timestamps in app UIs match the user's wall clock. Set `utc` for apps that prefer UTC internally (databases, queues, anything that explicitly normalizes on UTC). Full model in `TIME.md`. Most apps should leave this unset.

The compose file is held **verbatim**. Authors test it with `docker compose up` and it behaves identically inside malmo. Malmo configures the surrounding environment; it does not edit the compose file.

**Image references in compose use version tags, not digests.** Authors write `image: photoprism/photoprism:2.4.1` — readable, portable, the same line that runs outside malmo. For **store apps**, malmo's catalog CI resolves each `image:tag` to a specific `sha256:` digest at publish time and writes it into the signed catalog (`APP_STORE.md` # Trust model). The brain pulls by digest derived from the catalog — the version tag is the author's API, the digest is the bytes-binding. For **Door-2 custom apps**, the brain falls back to trust-on-first-use: pull, resolve digest, pin in the override.

**`needs_secure_context`** signals that the app relies on browser APIs gated on a [secure context](https://developer.mozilla.org/en-US/docs/Web/Security/Secure_Contexts) (camera, mic, clipboard, service workers, PWA install, secure cookies, WebAuthn). It's an **author-provided hint**, used by the brain to warn the user at install time — not a routing instruction.

- `needs_secure_context: false` (default): no special treatment.
- `needs_secure_context: true`: at install time, if the user's current URL scheme is `.local` (toggle off or not enrolled), the install dialog warns *"This app uses features that need HTTPS — they may not work at the `.local` URL. Turn on secure URLs in Settings → Network."* The user can install anyway.

The field is **never a routing override.** The URL each app gets is determined entirely by the global "Use secure URLs" toggle in Settings — see `MALMO_NETWORK.md`. App authors should set this honestly: many apps work fine on HTTP and shouldn't set it; apps that genuinely depend on a secure-context API should.

Previously this field was named `requires_https` and gated install on un-enrolled boxes. Changed 2026-05-14 — see `DECISIONS.md`.

**`health_probe`** opts the app into malmo's *"up but not responding"* detection. It is **not** Docker `HEALTHCHECK`: malmo holds the compose file verbatim and cannot add a healthcheck for the author, so the probe is declared here, in malmo's contract, and executed by the brain. Absent (the default), the app is never probed and the `app-unresponsive` health issue is never raised for it — least surprise for the bulk of the catalog. Shorthand `health_probe: /healthz` expands to `{ path: /healthz }`; the full form:

```yaml
health_probe:
  path: /healthz                    # HTTP path to GET (required when the block is present)
  healthy_status: [200]             # optional; default: any status < 500
  start_period: 60s                 # optional; grace after container start before probing (default 60s)
```

When set, the brain probes the app on its health-poll tick and raises the **non-blocking** `app-unresponsive` warning (`HEALTH.md` # Version, Tier-2 action: view logs / restart) when the probe fails. Three things to know, all owned by `HEALTH.md` # Detector catalog:

- **No `port` field.** The probe targets the app's existing route (which already points at `main_service:main_port`) — it goes *through Caddy* with `Host: <slug>`, exactly like a browser request, not by the brain dialing the container. This is a security call: it keeps the brain (the control plane) off every app-reachable Docker network. See `DECISIONS.md` 2026-06-02.
- **Default healthy = any status < 500**, i.e. "the server answered coherently." An app that returns `401`/`403`/`404` on the probe path is still *responding*; `5xx`, a timeout, or a connection failure (Caddy's `502`) is not. Authors with a real health endpoint can narrow to `[200]`.
- **`start_period`** is the grace after the container starts before the probe counts, so a warming-up app doesn't flap the banner on install/update.

Door-2 synthetic manifests omit it; a power user can add it later by editing the manifest, same as any other optional field.

**`service_user`** declares that the app writes its data as a **non-root** user and should run under a dedicated, malmo-allocated service identity rather than the folderless default (the brain's euid — root in production). Set it for nginx+php-fpm / LinuxServer-style images whose processes drop to a service user and so can't write malmo's root-owned `data/` dir. When `true`, the brain allocates a stable per-instance UID/GID from its reserved app-service band, pins the container `user:`, and chowns the instance `data/` dir to it (`APP_ISOLATION.md` # Runtime identity & data ownership).

The field is a **boolean intent, never a number** — you cannot name a UID; malmo owns the value, precisely so a manifest can't alias a host principal (a numeric `user:` is an admission rejection). It is meaningful only for **folderless** apps: an app with `folders` already runs as a managed non-root identity (the owner, or the shared malmo-app identity), so combining `service_user: true` with a folder grant is rejected. It does **not** help an image that hardcodes a *different* internal UID and ignores the runtime user (a php-fpm pool pinned to `www-data`, an entrypoint that `setuid`-drops to a fixed user) — that class waits on user-namespace remap (`APP_ISOLATION.md` # Not in v1).

### B2. Resources (recommended, never a limit)

The author declares **recommended** specs — advice only, never a ceiling.

```yaml
resources:
  recommended:
    memory: 512M
    cpu: 1.0
```

Used for the install-time capacity check ("you have 800M free; this wants 512M; fine") and store display/sorting. **There is deliberately no `limit` field** — the author can't see the user's hardware, so a manifest-imposed cap would throttle legitimate usage peaks. Apps run with no cgroup cap by default and burst freely; limiting is the *user's* call (an optional per-app **memory** cap in the UI, default off), and the brain protects its own control plane via OOM priority rather than caps. Full runtime model — including why CPU is never capped and how the brain stays alive under memory pressure — is owned by `APP_ISOLATION.md` # Resource limits.

### C. Storage

Two distinct kinds of storage in every app — user content and app state. See `STORAGE.md` # Files are first-class.

**User content** is what the user owns — photos, music, notes, documents. Lives at `/home/<user>/Photos/`, `~/Music/`, etc. Apps reach it by **bind-mounting use-case folders**, declared in `permissions.folders` (next section). Survives app uninstall.

**App state** is the app's own working data — indexes, caches, databases, configs. Lives at `/var/lib/malmo/instances/<id>/data/`. Opaque to the user. Deleted on uninstall (or archived if the user picks "keep data").

The `storage:` block configures app state only.

```yaml
storage:
  data_volumes:                       # app state to back up (indexes, configs, app DB)
    - ./data/index
    - ./data/config
  cache_volumes:                      # transient app state → excluded from backup
    - ./data/cache
    - ./data/thumbnails
  tier: fast                          # fast | normal | any  (default: any)
  estimated_size: 10GB                # measured app-state on disk right after install
  app_managed_user_content: false     # opt-in; see "Apps that manage their own content tree"
```

**`data_volumes` vs `cache_volumes`** — the backup system uses this. Cache is regeneratable; data isn't. Without this distinction, we'd back up thumbnail caches. A `cache_volumes` path **may be nested inside a `data_volume`** — the common shape is a single `./data` bind with a `cache/` subdirectory (e.g. a downloaded model or an embeddings store the app re-fetches on demand): list `./data` under `data_volumes` and `./data/cache` under `cache_volumes`, and backup is the data tree **minus** the cache subtrees. Paths that don't nest must not otherwise overlap. (Note both lists are author-grade declarations: v1 parses neither into the Go struct yet — see `docs/dev/authoring-apps-with-an-agent.md` — so they document intent for the backup system that will consume them.)

**`estimated_size` is the *app-state baseline at install* — measured, not a usage projection.** It is the size of the app's own working data (indexes, databases, configs) under `/var/lib/malmo/instances/<id>/data/` **as it stands the moment install completes** (the main service first reports healthy), on a clean install. It is deliberately *not* a guess at how big the app might grow with use: if the app later downloads another model or the user uploads a 2 GB library, that growth is **not** counted here — that's a runtime disk-pressure concern (`HEALTH.md` # `disk-full`), not a pre-install figure. The goal is a number close to the real on-disk cost of *having installed* the app; undercounting (a first-boot download still in flight when the health probe passes) is acceptable, overcounting by speculating about use is not. It is **not** the container-image size and **not** the user's content (Photos/Music/Documents the app bind-mounts — that is first-class, unbounded, and survives uninstall, so it is never attributed to the app). Image size is **not** author-declared: the catalog build resolves it from the actual pinned images at publish time (`APP_STORE.md` # Catalog schema). The brain combines the two — image size + `estimated_size` — into the **on-disk footprint** it shows before install (store card + consent dialog; `BRAIN_UI_PROTOCOL.md` # GET /api/v1/catalog/:id/install-plan, `DASHBOARD.md` # Install authorization). It stays advisory: warn on a tight disk, never block.

**How to measure it (authoring).** Don't estimate — measure. The import smoke-test already boots the app on a clean install and waits for the health probe; at that point `du -sb` the instance's `data/` volumes and record the result. Apps with a live-boot test can assert the figure against drift on version bumps. Full per-app CI measurement isn't required (some apps need managed services to boot); the author-time measurement during import is the source.

**`app_managed_user_content: true`** is the opt-in for apps that genuinely can't expose user content via use-case folders (legacy apps with opaque libraries). Triggers an install-time warning to the user: *"This app stores your files in its own folder, not your malmo Photos/Music/Documents. You'll need this app to access them."* The malmo store prefers apps that don't set this; curation policy may reject third-party manifests that do (TBD, `NEXT.md`).

**Bind mounts only — no Docker named volumes.** All app state lives under the instance's `data/` directory via bind mounts. Compose uses `${MALMO_DATA_DIR}/foo:/foo` (absolute) or `./data/foo:/foo` (relative to the project dir). One backup root, one disk-usage view, one mental model. See `APP_LIFECYCLE.md` # on-disk layout per instance.

### D. Managed services

The "OS as platform" bet made concrete. Apps declare what infra they need; the brain provisions it.

```yaml
services:
  database:
    type: postgres
    version: "15"                     # version pin
    name: photoprism_db               # logical name within this app
  cache:
    type: valkey
    version: "8"
```

The brain provisions the resource (e.g., creates a database in the shared Postgres-15 instance with a scoped user) and **injects credentials as environment variables**.

Available types and versions (`SERVICE_PROVISIONING.md` # Catalog (v1)): `postgres` (15, 16), `mysql` (8.0, 8.4), `mariadb` (10.11, 11.4), `valkey` (8). `redis` (7) is accepted as a **compatibility alias for `valkey`** — it always provisions the BSD-3 Valkey engine underneath, never upstream Redis (`DECISIONS.md` 2026-06-13); new manifests should prefer `valkey`. A type/version outside this set is rejected at manifest parse time. The MySQL family injects port 3306 and a `mysql://` DSN for both engines (one wire protocol); Valkey injects port 6379 and a `redis://` DSN (the universal RESP scheme).

**Naming convention: app-defined.** The malmo brain exposes the credentials under stable, documented variable names (e.g., `MALMO_SERVICE_DATABASE_HOST`, `MALMO_SERVICE_DATABASE_USER`, `MALMO_SERVICE_DATABASE_PASSWORD`, `MALMO_SERVICE_DATABASE_NAME`, `MALMO_SERVICE_DATABASE_DSN`). The app's compose file maps these to whatever variables the app actually expects:

```yaml
# inside the app's docker-compose.yml
environment:
  PHOTOPRISM_DATABASE_DSN: ${MALMO_SERVICE_DATABASE_DSN}
```

This means the app is the one doing the wiring. The app remains portable (still runs outside malmo with a manually-set env var). It's a small adaptation — well-documented, explicit, no magic.

Apps that don't trust managed services can simply ship their own database in their own compose file. **Both paths work**; the manifest path is encouraged but not enforced.

### D2. Generated secrets

Many apps require a random, app-specific secret to sign auth tokens, sessions, or cookies — `BETTER_AUTH_SECRET` (Better Auth), `SECRET_KEY_BASE` (Rails), a JWT/HMAC signing key. The author can't ship a value (a public catalog secret signs nothing securely), and the non-technical user can't be asked to generate one. So the manifest *declares* the need and the brain generates the value.

```yaml
secrets:
  - name: auth          # → injected as MALMO_SECRET_AUTH
  - name: session_key
    bytes: 64           # entropy drawn before encoding; default 32, floor 16
```

At install the brain draws each secret from a CSPRNG, base64url-encodes it (32 bytes → a 43-char string, past the "32+ char" bar most libraries want), and injects it as `MALMO_SECRET_<NAME>` (uppercased). The app's compose maps that to whatever variable the app actually expects — the same app-defined wiring as `MALMO_SERVICE_*` and `MALMO_FOLDER_*`:

```yaml
# inside the app's docker-compose.yml
environment:
  BETTER_AUTH_SECRET: ${MALMO_SECRET_AUTH}
```

**The value is generated once and stays stable** for the life of the instance — it is persisted and re-emitted on every restart, never re-rolled, because a token-signing secret that changed underneath the app would invalidate every live session. `name` is lowercase snake_case (so the uppercased env-var suffix is unambiguous); names are unique within a manifest. See `SERVICE_PROVISIONING.md` # Env-var injection.

### D3. Outgoing mail

Apps that can *send* email — password resets, reminders, invites — declare it, and the admin decides at install (or later) which of the box's registered SMTP providers the app sends through (`SERVICE_PROVISIONING.md` # BYO outgoing mail).

```yaml
mail:
  optional: true       # the app must run fine unbound (email features off)
```

When the instance is bound to a provider, the brain injects `MALMO_MAIL_HOST/_PORT/_USER/_PASSWORD/_FROM/_ENCRYPTION` plus a Symfony-style `MALMO_MAIL_DSN`; unbound, **nothing is injected**. The compose maps the vars app-defined as usual, with a default for the unbound case so absence degrades to the app's own "email off" mode:

```yaml
# inside the app's docker-compose.yml (Kimai)
environment:
  MAILER_URL: "${MALMO_MAIL_DSN:-null://null}"
  MAILER_FROM: "${MALMO_MAIL_FROM:-kimai@example.com}"
```

v1 admits only `optional: true` — an app that *can't* run unbound (`optional: false` or a bare `mail: {}`) is rejected at parse, because a box with no registered providers couldn't install it. Required-mail semantics (blocking install until a provider is picked) is a possible later loosening; declare-and-degrade is the v1 contract.

### E. Permissions and capabilities

What the app is allowed to touch. Default is "very little"; manifest opts in to specific things.

**Granularity: medium.** Not coarse-grained-only (leaves real attack surface), not fine-grained Kubernetes-style (rabbit hole non-technical users would never understand).

```yaml
permissions:
  internet: true                      # outbound internet allowed
  lan: false                          # can talk to LAN devices (macvlan; see APP_ISOLATION.md)
  folders:                            # access to use-case content folders (see below)
    - { folder: photos, mode: write }
    - { folder: movies, mode: read }
  devices: [/dev/ttyUSB0]             # explicit device paths (Zigbee/Z-Wave dongles, webcams)
  gpu: true                           # platform-appropriate GPU runtime (NVIDIA / Intel / AMD)
  network_isolation: per_app          # per_app | shared
```

Permissions are **declared and enforced.** Not metadata — the brain actually configures Docker networks, bind mounts, and devices to match. Apps cannot reach what they didn't declare. The concrete Linux/Docker primitive behind each field is owned by `APP_ISOLATION.md` # Capabilities & privilege; this doc is the schema, that doc is the enforcement.

Store review checks the declared permissions match the app's actual usage.

**`devices` and `gpu`.** `devices` lists explicit `/dev/...` paths the app needs passed through (a Zigbee dongle, a webcam); the brain validates each exists before start. `gpu: true` is **separate from `devices`** because driver wiring is platform-specific — the OS selects the right runtime (NVIDIA container runtime, Intel/AMD `/dev/dri`) and the app introspects what's present via standard tooling; if no GPU exists the install fails at the capacity check.

**No added Linux capabilities for store apps.** The brain's override is `cap_drop: [ALL]` and adds none; admission rejects any `cap_add` (`APP_LIFECYCLE.md` # admission policy). Apps that genuinely need a capability, `privileged`, or the Docker socket do **not** get there through Door 2 — admission is door-symmetric and refuses those for custom compose exactly as for store apps (`APP_ISOLATION.md` # Trust tiers, `DECISIONS.md` 2026-06-02). They run as curated OS integrations (**Tier 2**, `SERVICE_PROVISIONING.md`) or the admin runs them over SSH. A raw-capability escape hatch *in a store manifest* is intentionally absent. (`APP_ISOLATION.md` sketches a reviewed `permissions.capabilities` list; that is not part of the v1 store schema and is tracked as an open item — see `NEXT.md`.)

#### `folders` — access to use-case content

How an app reads/writes content in the use-case folders (`STORAGE.md` # What apps and users actually see). For each folder, declare the folder name, mode, and subfolder scope:

```yaml
permissions:
  folders:
    - folder: photos                  # photos | music | movies | documents | notes | downloads
      mode: write                     # read | write  (default: read)
      scope: whole                    # whole | pick-subfolder  (default: whole)
    - folder: notes
      mode: write
      scope: pick-subfolder           # user picks the subfolder at install
      default: Notes/Obsidian         # default subfolder; user can override
```

- **`mode`** defaults to **`read`** when unspecified — least privilege, and `write` is a deliberate choice the catalog reviewer notices. `mode: write` shows up on the install screen as "this app can ADD, CHANGE, AND DELETE files in your X folder" — read-only declarations are visibly different.
- **`scope: whole`** (default) — brain bind-mounts the entire folder (e.g., all of the chosen `Photos/`) into the container.
- **`scope: pick-subfolder`** — install screen prompts the user: "Which folder should this app manage?" Default is the manifest's `default` (auto-created if absent), user can choose any path under the folder. Used for notes apps (one vault per "context"), media apps that should manage a subset of a library, etc.

**Source is the installer's choice, not the author's.** The manifest declares *what* content the app touches and *how* (`mode`/`scope`); it deliberately does **not** declare whether the folder is the user's **personal** `~/<Folder>/` or the **household-shared** `/srv/malmo/shared/<Folder>/`. The author can't know a given household's intent — "I want *my own* Jellyfin on *my* movies" and "I want it on the *family* library" are both valid and the app code is identical. So source is elected per folder at install (`DASHBOARD.md` # install authorization, `DECISIONS.md` 2026-05-30):

- **Personal instance** — the install screen offers, per folder, **your `<Folder>`** (default) or the **household Shared `<Folder>`**. Choosing shared adds the container to the `malmo-shared` group; it reaches exactly what the owner can already reach as a household member.
- **Household instance** — always the household Shared `<Folder>` (a shared instance has no single owner whose `~/` it could bind). No per-folder toggle.

This supersedes the earlier `user_folders` / `shared_folders` split, where the author picked the source by choosing the key.

**How it's mounted — fixed path + injected env var.** The brain bind-mounts each declared folder at a stable, documented path — `/malmo/<folder>` (e.g. `/malmo/photos`) — and injects the absolute path as `MALMO_FOLDER_<NAME>` (e.g. `MALMO_FOLDER_PHOTOS=/malmo/photos`). The app's compose maps that variable to whatever the app actually expects:

```yaml
# inside the app's docker-compose.yml
environment:
  PHOTOPRISM_ORIGINALS_PATH: ${MALMO_FOLDER_PHOTOS}
```

This is the same injection convention as managed services (`MALMO_SERVICE_*`) and `MALMO_DATA_DIR` — the manifest stays declarative about *intent*, the app does the wiring, and the app stays portable. The in-container mount path and the env var are stable regardless of the elected source or subfolder; only the **host source** varies (personal `~/<Folder>/` vs shared `/srv/malmo/shared/<Folder>/`, narrowed further by a `pick-subfolder` choice). The source side is resolved by the brain at install time — it learns the owner's home path and UID from host-agent (`BRAIN_HOST_PROTOCOL.md`), never declared by the author.

#### External-storage convention for popular apps

The malmo-tuned manifest for an app whose upstream supports external libraries (Immich, Photoprism, Jellyfin, Nextcloud, Navidrome, ...) declares `folders` and configures the app via env vars or post-install steps to **point its internal "library path" at the bind-mounted use-case folder**. The user's files stay at `~/Photos/`, the app indexes them there, uninstalling the app keeps the files. This is the path that earns the manifest a "files first-class" badge in the store.

Apps that don't support external libraries fall back to `storage.app_managed_user_content: true` (`STORAGE.md` # Files are first-class). For v1, the store catalog is hand-curated by malmo — we write manifests that follow the external-storage pattern wherever upstream supports it.

**No `cap_add` for store (Tier-3) apps.** The brain's override drops ALL capabilities and adds none. Apps that genuinely need Linux capabilities (VPN clients, FUSE mounts, raw sockets) belong in Tier 2 — OS integrations curated by malmo with a separate install path. See `SERVICE_PROVISIONING.md`. If a Tier-3 compose declares `cap_add`, the brain refuses to install it.

### F. Lifecycle hooks — deferred from MVP

The `hooks:` block is **not part of v1.** Apps already run their own migrations on container start; the brain's **pre-update snapshot** (`UPDATES.md` # Pre-update snapshot) is the v1 safety net for migrations that go wrong.

When hooks return, they will be designed as **one-shot container images** rather than in-container scripts:

```yaml
# Sketch, not v1 syntax
hooks:
  pre_update:          { image: photoprism/migrator:2.4.1 }
  post_update_rollback: { image: photoprism/migrator:2.4.1, args: ["rollback"] }
```

The brain will run the hook image as a transient container with the app's volumes attached. This respects closed-source images (no shell-in-app-container required) and gives commercial vendors a clean integration path. `pre_update`, when supplied, replaces the brain's brute-force tar for that app — the snapshot remains the default for apps without a hook. `post_update_rollback` fires only when the update fails after the new container started; it's the right shape for apps with bespoke recovery (e.g., a destructive schema migration that needs explicit reversal). Tracked in `APP_LIFECYCLE.md` # "Deferred: lifecycle hooks".

### G. Multi-user behavior — not a manifest concern

**The manifest does not decide how an app is shared across accounts.** Whether an instance is *household* (one shared instance, app-internal multi-user separates people inside it) or *personal* (one instance per owner, binding only the owner's folders) is **elected by the installing user**, not declared by the author (`DASHBOARD.md` # instances are owner-scoped, `DECISIONS.md` 2026-05-29):

- An **admin** chooses Household or "Just for me" at install.
- A **member** can only create a **personal** instance.
- Every user sees their own personal instances plus the household instances they're permitted to open.

There is deliberately **no `multi_user.mode` field** — an earlier draft had `shared | per_user`, removed because scope is a runtime election, not a static property of the app. The brain realizes a personal instance as the per-owner compose-project shape already locked in `APP_LIFECYCLE.md` # an app instance is a Docker Compose project; folder bindings resolve to the owner's `~/<Folder>/`.

Mesh-guest sharing (`guest_shareable`) and per-app household visibility (which members may open a given household instance) are **deferred** — guest sharing rides on the mesh (deferred), and the visibility/authorization model is owned by `AUTH.md` / `DASHBOARD.md`, not the manifest. Neither is a v1 manifest field.

## Complete sample manifest

PhotoPrism, end-to-end:

```yaml
id: photoprism
manifest_version: 1
name: PhotoPrism
version: 2.4.1
description:
  short: "Self-hosted photo library with AI tagging"
  long: |
    PhotoPrism is an AI-powered app for browsing, organizing, and sharing your photo collection without giving up control. It indexes your existing files, generates thumbnails, and uses TensorFlow to tag people, places, and subjects automatically. Works great alongside the Files app — your originals stay in your Photos folder and are never locked inside PhotoPrism.
icon: ./icon.png
categories: [media, photos]
author: { name: "PhotoPrism Labs", url: "https://photoprism.app" }
license: AGPL-3.0

compose_file: docker-compose.yml
main_service: photoprism
main_port: 2342
preferred_slugs: [photos, photoprism]

resources:
  recommended: { memory: 1G, cpu: 2.0 }  # advice only; never a cap

storage:
  data_volumes: [./index, ./sidecar]    # app's own index/metadata
  cache_volumes: [./cache, ./thumbs]
  tier: fast
  estimated_size: 10GB

services:
  database: { type: postgres, version: "15" }

permissions:
  internet: true
  folders:
    - { folder: photos, mode: write }   # PhotoPrism reads/writes the chosen Photos folder
  gpu: true                             # hardware-accelerated thumbnails / transcode
```

~28 lines for a real-world app. The compose file is what the author already had. Whether this installs as a household or personal instance is the installer's choice, not declared here (# G).

## Custom container — synthetic manifest

User pastes a compose file, names the app, picks the main port, and **elects the app's permissions** in the install form (`DASHBOARD.md` # Door-2 custom container install flow). The brain generates:

```yaml
id: my-thing-x4f7                     # auto: name + entropy
manifest_version: 1
name: my-thing
version: custom
compose_file: docker-compose.yml      # the user's pasted file
main_service: <inferred or asked>
main_port: <inferred or asked>
preferred_slugs: [my-thing]

storage:
  data_volumes: [<all volumes from the compose>]
  # cache_volumes: empty by default — best-effort backup of everything
permissions:
  internet: true                      # default-on for custom apps; form toggle
  lan: false                          # form toggle
  gpu: false                          # form toggle
  folders:                            # empty by default; one entry per form row
    - folder: photos
      mode: read
      target: /photoprism/originals   # Door-2-only: explicit in-container path
```

**The `permissions` block is admin-elected in the form, not hardcoded.** `internet` (default on), `lan`, `gpu`, and any `folders` rows are authored through the install screen's permission controls; `devices` and managed `services` are the long tail, reached through the form's **Edit as YAML** escape hatch rather than dedicated fields (`DASHBOARD.md` # Form is a projection of the synthetic manifest). The form is a friendly projection of *this* manifest; the YAML toggle edits the same overlay raw. No managed services by default; best-effort backup of all volumes (we can't tell cache from data without the author's input); scope (household vs. personal) is the installer's election, not a manifest field (# G). The richer-manifest *graduate-in-place* path — editing an already-installed instance's manifest — is the intended future shape but **deferred past v1** (`DASHBOARD.md` # Edit-after-install is deferred); install-time authoring (form + YAML toggle) is not that deferred feature.

**Door-2 folder grants carry an explicit `target`.** Store-app folder grants declare no in-container path — the brain mounts each at `/malmo/<folder>` + injects `MALMO_FOLDER_<NAME>`, and the author maps that env var (# Locked: folders mount at a fixed path). A Door-2 paste has no author to adapt: the verbatim third-party compose hardcodes its data path, so the synthetic manifest's folder entry carries an explicit `target` (the destination the admin typed) and the brain binds the elected source straight there. The `target` field is **Door-2-only** — store manifests omit it and keep the fixed-path + env-var convention (`DECISIONS.md` 2026-06-02). The *source* (personal vs. household) stays the installer's per-folder election, exactly as for store apps.

**What the brain infers vs. asks (Door-2 paste).** `main_service` is **autodetected** when the compose has exactly one service, and **asked** otherwise (a dropdown of the compose's services). `main_port` is the *container-internal* port Caddy routes to — **best-effort inferred** from every signal the compose carries: a single `expose:` value, or the *container side* of a published `ports:` mapping (`8080:80` ⇒ `80`), mined out for the prefill before the mapping itself is rejected. It is **asked** only when the compose is silent (malmo can't read the image's `EXPOSE` without pulling it) and is always editable. A published `ports:` is never *honored* (it's an admission rejection — Caddy fronts every app on internal networks); its container side is only read to prefill `main_port`. The full screen UX — where the flow lives, the permission controls and YAML escape hatch, inline admission-error coaching, the live URL preview, and the deferred edit-after-install path — is locked in `DASHBOARD.md` # Door-2 custom container install flow.

**Custom apps may request managed services.** Allowed, not encouraged. A power user pasting compose can manually add `services: { database: { type: postgres, version: "15" } }` and gets the same managed Postgres treatment. We document the path; we don't gate it.

## Locked decisions

- **Format: YAML.** `manifest.yml`.
- **Schema versioned from day one.** `manifest_version: 1`. Backward compatible for at least the previous two majors.
- **Most fields optional with sensible defaults.** Required: `id`, `manifest_version`, `name`, `version`, `compose_file`, `main_service`, `main_port`.
- **Compose file is verbatim.** Malmo doesn't rewrite it.
- **`resources.recommended` is advice, never a cap.** No `limit` field exists in the manifest; authors can't see the user's hardware. Default runtime is uncapped burst; user-set memory caps and control-plane OOM protection live in `APP_ISOLATION.md` # Resource limits.
- **Permissions are declared and enforced.** Not just metadata.
- **User content vs. app state are separate stores.** User content (`/home/<user>/Photos/`, etc.) accessed by manifest-declared bind mounts of use-case folders; app state in `/var/lib/malmo/instances/<id>/data/`. Apps reach user content by reference, never by copy.
- **`scope: pick-subfolder`** for `folders` — install-time prompt for apps that should manage a subset (notes apps, media subsets). Default is provided by the manifest; user can override.
- **Folder source (personal vs household-shared) is installer-elected, not a manifest field.** The manifest declares only the folder + `mode` + `scope`; whether it binds the owner's `~/<Folder>/` or the household `/srv/malmo/shared/<Folder>/` is the installer's per-folder choice (personal instances pick, defaulting to personal; household instances are always shared). Replaces the old `user_folders` / `shared_folders` keys. See `DECISIONS.md` 2026-05-30.
- **`folders` mount at a fixed path + injected env var (store apps).** A store manifest declares folder + `mode` + `scope` but no in-container path; the brain mounts each at `/malmo/<folder>` and injects `MALMO_FOLDER_<NAME>`. The app's compose maps that variable to its own library path. `mode` defaults to `read`. Same injection pattern as `MALMO_SERVICE_*` / `MALMO_DATA_DIR`. **Door-2 custom apps diverge:** their verbatim compose has no author to map the env var, so a Door-2 folder grant carries an explicit `target` (the destination path the admin types) and the brain binds straight there. `target` is Door-2-only; store grants omit it (# Custom container — synthetic manifest, `DECISIONS.md` 2026-06-02).
- **`gpu` is its own field, separate from `devices`.** `devices` passes through explicit `/dev/...` paths; `gpu: true` selects the platform GPU runtime. No-GPU box fails at the capacity check.
- **`app_managed_user_content: true`** is the opt-in for apps that don't expose user content via use-case folders. Triggers an install-time warning. Curated store prefers apps without it.
- **`service_user: true`** opts a *folderless* app into a dedicated, malmo-allocated non-root runtime identity — stable per instance, drawn from a reserved app-service band below the 3000 user floor; the brain pins `user:` and chowns `data/` to it. The manifest declares **intent only — no numeric UID is namable** (a host-namespace UID could alias a real principal under malmo's no-userns-remap model); a numeric `user:`, or `service_user: true` alongside a `folders` grant, is an admission rejection. Does not cover images that hardcode a *different* non-root internal UID (deferred to userns-remap, `NEXT.md`). `APP_ISOLATION.md` # Runtime identity & data ownership, `DECISIONS.md` 2026-06-10.
- **Scope (household vs. personal) is installer-elected, not a manifest field.** No `multi_user.mode`. Admins choose household or personal; members install personal only (`DASHBOARD.md`, `DECISIONS.md` 2026-05-29). Guest-sharing and household visibility are deferred and not manifest fields.
- **No added Linux capabilities for store apps.** Override is `cap_drop: [ALL]`, adds none; admission rejects `cap_add`. Capability / `privileged` / Docker-socket needs go through Door-2 or Tier 2. A reviewed `permissions.capabilities` escape hatch is not in the v1 store schema (open in `NEXT.md`).
- **Bind mounts only — no Docker named volumes for app data.** All data lives under the instance's `data/` dir.
- **Hooks deferred from MVP.** When reintroduced, they will be one-shot container images, not in-container scripts.
- **`health_probe` is opt-in and malmo-executed, not Docker `HEALTHCHECK`.** Optional `path` (+ `healthy_status`, `start_period`); the brain probes the app *through its Caddy route* on the health-poll tick and raises the non-blocking `app-unresponsive` warning (`HEALTH.md`) when it fails. Absent → no probe, issue never raised. Default healthy = any status < 500. Probing through Caddy (not by dialing the container) keeps the control plane off app-reachable networks. See `DECISIONS.md` 2026-06-02.
- **`needs_secure_context` is an install-time warning, not a routing override or install block.** Apps declare it honestly; the brain warns the user if the current URL scheme is HTTP. The URL each app uses is determined by the global toggle in Settings, not the manifest.
- **Public, versioned spec.** Third-party stores depend on it.
- **Env-var injection: app-defined naming.** App's compose maps malmo's stable `MALMO_SERVICE_*` variables to whatever names the app expects. No auto-rewrite. Authors adapt; we document.
- **Generated secrets are declared, brain-generated, and stable.** A manifest declares `secrets: [{name, bytes?}]`; the brain draws each from a CSPRNG once at install, persists it, and injects it as `MALMO_SECRET_<NAME>` — re-emitted verbatim on every restart so token-signing secrets don't rotate underneath live sessions. Same app-defined wiring as `MALMO_SERVICE_*` (# D2). Security hardening (delivery surface, at-rest, rotation) is tracked open in `NEXT.md` # App-secret injection hardening.
- **Outgoing mail is declared optional-only (`mail: {optional: true}`).** The declaration unlocks the install-time provider picker and per-instance `MALMO_MAIL_*` injection (# D3, `SERVICE_PROVISIONING.md` # BYO outgoing mail); unbound apps get nothing injected and must run with email off. `optional: false` (and a bare `mail: {}`) is rejected at parse in v1.
- **Permissions granularity: medium for v1.** Internet, LAN, shared storage, devices, privileged, network isolation. Not coarse-only, not fine-grained Kubernetes-style.
- **Custom apps can request managed services.** Allowed, not encouraged.
- **No inter-app dependencies in v1.** Apps are self-contained. If they need multiple services, they go in the same compose. Cross-app sharing only via shared use-case folders (two of the same user's apps both binding the same `folders` entry; the installer points each at the same personal or shared source).
- **Manifest can live in-repo or in malmo's catalog repo.** Both patterns supported indefinitely. Schema is identical in both cases. We bootstrap by writing manifests for popular apps; over time, upstreams ship their own.
- **Image references use version tags; the store catalog resolves digests.** Authors write `image: foo/bar:1.2.3`; malmo's CI pins the bytes via a `sha256:` digest in the signed catalog (`APP_STORE.md`). Door-2 custom apps fall back to TOFU digest pinning in the brain.

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
