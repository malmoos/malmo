# malmo App Manifest

> Working spec for the `manifest.yml` schema — the contract between an app and the malmo OS. Companion to `SPEC.md`, `CONTROL_PLANE.md`, and `APP_LIFECYCLE.md`.

## Core design principle: one model, two doors

The brain only ever knows about manifests. *Everything* installed on a malmo box has one. The user-facing UX has two entry points:

- **Door 1 — App store.** App author wrote a complete `manifest.yml` + `docker-compose.yml`. One-click install. Full integration (managed services, backup hooks, declared permissions).
- **Door 2 — Custom container.** User pastes/uploads a raw `docker-compose.yml`. The brain **generates a synthetic manifest** with sensible defaults. The app is a first-class citizen — it gets a subdomain, shows in the dashboard, integrates as much as the synthetic manifest allows.

This unification matters because:
- The brain's data model stays simple — one type of thing.
- A power user can paste a compose file today and later edit the synthetic manifest to graduate the app (add backup hooks, request a managed DB, etc.) without reinstalling.
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
```

### B. Runtime

The minimum to actually launch the thing.

```yaml
compose_file: docker-compose.yml      # standard compose; never modified by malmo
main_service: photoprism              # which compose service is "the app"
main_port: 2342                       # port the main service listens on internally
preferred_slugs: [photos, photoprism] # subdomain priority list; OS picks first free
needs_secure_context: false           # optional; default false. See below.
```

The compose file is held **verbatim**. Authors test it with `docker compose up` and it behaves identically inside malmo. Malmo configures the surrounding environment; it does not edit the compose file.

**Image references in compose use version tags, not digests.** Authors write `image: photoprism/photoprism:2.4.1` — readable, portable, the same line that runs outside malmo. For **store apps**, malmo's catalog CI resolves each `image:tag` to a specific `sha256:` digest at publish time and writes it into the signed catalog (`APP_STORE.md` # Trust model). The brain pulls by digest derived from the catalog — the version tag is the author's API, the digest is the bytes-binding. For **Door-2 custom apps**, the brain falls back to trust-on-first-use: pull, resolve digest, pin in the override.

**`needs_secure_context`** signals that the app relies on browser APIs gated on a [secure context](https://developer.mozilla.org/en-US/docs/Web/Security/Secure_Contexts) (camera, mic, clipboard, service workers, PWA install, secure cookies, WebAuthn). It's an **author-provided hint**, used by the brain to warn the user at install time — not a routing instruction.

- `needs_secure_context: false` (default): no special treatment.
- `needs_secure_context: true`: at install time, if the user's current URL scheme is `.local` (toggle off or not enrolled), the install dialog warns *"This app uses features that need HTTPS — they may not work at the `.local` URL. Turn on secure URLs in Settings → Network."* The user can install anyway.

The field is **never a routing override.** The URL each app gets is determined entirely by the global "Use secure URLs" toggle in Settings — see `MALMO_NETWORK.md`. App authors should set this honestly: many apps work fine on HTTP and shouldn't set it; apps that genuinely depend on a secure-context API should.

Previously this field was named `requires_https` and gated install on un-enrolled boxes. Changed 2026-05-14 — see `DECISIONS.md`.

### C. Storage

Two distinct kinds of storage in every app — user content and app state. See `STORAGE.md` # Files are first-class.

**User content** is what the user owns — photos, music, notes, documents. Lives at `/home/<user>/Photos/`, `~/Music/`, etc. Apps reach it by **bind-mounting use-case folders**, declared in `permissions.user_folders` (next section). Survives app uninstall.

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
  estimated_size: 10GB                # for warnings on small disks
  app_managed_user_content: false     # opt-in; see "Apps that manage their own content tree"
```

**`data_volumes` vs `cache_volumes`** — the backup system uses this. Cache is regeneratable; data isn't. Without this distinction, we'd back up thumbnail caches.

**`app_managed_user_content: true`** is the opt-in for apps that genuinely can't expose user content via use-case folders (legacy apps with opaque libraries). Triggers an install-time warning to the user: *"This app stores your files in its own folder, not your malmo Photos/Music/Documents. You'll need this app to access them."* The malmo store prefers apps that don't set this; curation policy may reject third-party manifests that do (TBD, `NEXT.md`).

**Bind mounts only — no Docker named volumes.** All app state lives under the instance's `data/` directory via bind mounts. Compose uses `${MALMO_DATA_DIR}/foo:/foo` (absolute) or `./data/foo:/foo` (relative to the project dir). One backup root, one disk-usage view, one mental model. See `APP_LIFECYCLE.md` # on-disk layout per instance.

### D. Managed services

The "OS as platform" bet made concrete. Apps declare what infra they need; the brain provisions it.

```yaml
services:
  database:
    type: postgres
    version: "15"                     # major version pin
    name: photoprism_db               # logical name within this app
  cache:
    type: redis
    version: "7"
```

The brain provisions the resource (e.g., creates a database in the shared Postgres-15 instance with a scoped user) and **injects credentials as environment variables**.

**Naming convention: app-defined.** The malmo brain exposes the credentials under stable, documented variable names (e.g., `MALMO_SERVICE_DATABASE_HOST`, `MALMO_SERVICE_DATABASE_USER`, `MALMO_SERVICE_DATABASE_PASSWORD`, `MALMO_SERVICE_DATABASE_NAME`, `MALMO_SERVICE_DATABASE_DSN`). The app's compose file maps these to whatever variables the app actually expects:

```yaml
# inside the app's docker-compose.yml
environment:
  PHOTOPRISM_DATABASE_DSN: ${MALMO_SERVICE_DATABASE_DSN}
```

This means the app is the one doing the wiring. The app remains portable (still runs outside malmo with a manually-set env var). It's a small adaptation — well-documented, explicit, no magic.

Apps that don't trust managed services can simply ship their own database in their own compose file. **Both paths work**; the manifest path is encouraged but not enforced.

### E. Permissions and capabilities

What the app is allowed to touch. Default is "very little"; manifest opts in to specific things.

**Granularity: medium.** Not coarse-grained-only (leaves real attack surface), not fine-grained Kubernetes-style (rabbit hole non-technical users would never understand).

```yaml
permissions:
  internet: true                      # outbound internet allowed
  lan: false                          # can talk to LAN devices
  user_folders:                       # access to per-user content (see below)
    - { folder: photos, mode: write }
  shared_folders:                     # access to /srv/malmo/shared/* (see below)
    - { folder: movies, mode: read }
  devices: [/dev/dri]                 # GPU access etc.
  privileged: false                   # almost never true
  network_isolation: per_app          # per_app | shared
```

Permissions are **declared and enforced.** Not metadata — the brain actually configures Docker networks, bind mounts, and capabilities to match. Apps cannot reach what they didn't declare.

Store review checks the declared permissions match the app's actual usage.

#### `user_folders` — access to per-user content

How an app reads/writes the user's use-case folders (`STORAGE.md` # What apps and users actually see). For each folder, declare scope and mode:

```yaml
permissions:
  user_folders:
    - folder: photos                  # photos | music | movies | documents | notes | downloads
      mode: write                     # read | write
      scope: whole                    # whole | pick-subfolder  (default: whole)
    - folder: notes
      mode: write
      scope: pick-subfolder           # user picks at install
      default: Notes/Obsidian         # default subfolder; user can override
```

- **`scope: whole`** (default) — brain bind-mounts the entire folder (e.g., all of `~/Photos/`) into the container.
- **`scope: pick-subfolder`** — install screen prompts the user: "Which folder should this app manage?" Default is the manifest's `default` (auto-created if absent), user can choose any path under the folder. Used for notes apps (one vault per "context"), media apps that should manage a subset of a library, etc.

`mode: write` shows up on the install screen as "this app can ADD, CHANGE, AND DELETE files in your X folder" — read-only declarations are visibly different.

#### `shared_folders` — access to household-shared content

Same shape as `user_folders`, but bind-mounts from `/srv/malmo/shared/<folder>/`. The container is added to the `malmo-shared` group. Typical use: a Tier-1 household Jellyfin reading from `Shared/Movies/`.

```yaml
permissions:
  shared_folders:
    - { folder: movies, mode: read }
    - { folder: music, mode: read }
```

#### External-storage convention for popular apps

The malmo-tuned manifest for an app whose upstream supports external libraries (Immich, Photoprism, Jellyfin, Nextcloud, Paperless-ngx, Navidrome, ...) declares `user_folders` and configures the app via env vars or post-install steps to **point its internal "library path" at the bind-mounted use-case folder**. The user's files stay at `~/Photos/`, the app indexes them there, uninstalling the app keeps the files. This is the path that earns the manifest a "files first-class" badge in the store.

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

### G. Multi-user behavior

How the app is shared across malmo accounts.

```yaml
multi_user:
  mode: shared                        # shared | per_user
  guest_shareable: true               # can be shared with mesh guests
  default_visibility: household       # household | admin_only
```

- **`shared`** — one app instance, multiple malmo users log in to it (e.g., a household grocery list). Default for most apps.
- **`per_user`** — each malmo user gets their own instance, with separate data. For inherently personal apps (private notes).

## Complete sample manifest

PhotoPrism, end-to-end:

```yaml
id: photoprism
manifest_version: 1
name: PhotoPrism
version: 2.4.1
description:
  short: "Self-hosted photo library with AI tagging"
icon: ./icon.png
categories: [media, photos]
author: { name: "PhotoPrism Labs", url: "https://photoprism.app" }
license: AGPL-3.0

compose_file: docker-compose.yml
main_service: photoprism
main_port: 2342
preferred_slugs: [photos, photoprism]

storage:
  data_volumes: [./index, ./sidecar]    # app's own index/metadata
  cache_volumes: [./cache, ./thumbs]
  tier: fast
  estimated_size: 10GB

services:
  database: { type: postgres, version: "15" }

permissions:
  internet: true
  user_folders:
    - { folder: photos, mode: write }   # PhotoPrism reads/writes ~/Photos/
  devices: [/dev/dri]

multi_user:
  mode: shared
  guest_shareable: true
```

~30 lines for a real-world app. The compose file is what the author already had.

## Custom container — synthetic manifest

User pastes a compose file, names the app, picks the main port. The brain generates:

```yaml
id: my-thing-x4f7                     # auto: name + entropy
manifest_version: 1
name: my-thing
version: custom
compose_file: docker-compose.yml      # the user's pasted file
main_service: <inferred or asked>
main_port: <user-provided>
preferred_slugs: [my-thing]

storage:
  data_volumes: [<all volumes from the compose>]
  # cache_volumes: empty by default — best-effort backup of everything
permissions:
  internet: true                      # default-on for custom apps
  lan: false
multi_user:
  mode: shared
```

No managed services by default. Best-effort backup of all volumes (we can't tell cache from data without the author's input). User can edit the synthetic manifest later to add managed services, hooks, refined storage classification — same schema, same fields.

**Custom apps may request managed services.** Allowed, not encouraged. A power user pasting compose can manually add `services: { database: { type: postgres, version: "15" } }` and gets the same managed Postgres treatment. We document the path; we don't gate it.

## Locked decisions

- **Format: YAML.** `manifest.yml`.
- **Schema versioned from day one.** `manifest_version: 1`. Backward compatible for at least the previous two majors.
- **Most fields optional with sensible defaults.** Required: `id`, `manifest_version`, `name`, `version`, `compose_file`, `main_service`, `main_port`.
- **Compose file is verbatim.** Malmo doesn't rewrite it.
- **Permissions are declared and enforced.** Not just metadata.
- **User content vs. app state are separate stores.** User content (`/home/<user>/Photos/`, etc.) accessed by manifest-declared bind mounts of use-case folders; app state in `/var/lib/malmo/instances/<id>/data/`. Apps reach user content by reference, never by copy.
- **`scope: pick-subfolder`** for `user_folders` — install-time prompt for apps that should manage a subset (notes apps, media subsets). Default is provided by the manifest; user can override.
- **`app_managed_user_content: true`** is the opt-in for apps that don't expose user content via use-case folders. Triggers an install-time warning. Curated store prefers apps without it.
- **No `cap_add` for Tier-3 store apps.** Apps needing Linux capabilities belong in Tier 2.
- **Bind mounts only — no Docker named volumes for app data.** All data lives under the instance's `data/` dir.
- **Hooks deferred from MVP.** When reintroduced, they will be one-shot container images, not in-container scripts.
- **`needs_secure_context` is an install-time warning, not a routing override or install block.** Apps declare it honestly; the brain warns the user if the current URL scheme is HTTP. The URL each app uses is determined by the global toggle in Settings, not the manifest.
- **Public, versioned spec.** Third-party stores depend on it.
- **Env-var injection: app-defined naming.** App's compose maps malmo's stable `MALMO_SERVICE_*` variables to whatever names the app expects. No auto-rewrite. Authors adapt; we document.
- **Permissions granularity: medium for v1.** Internet, LAN, shared storage, devices, privileged, network isolation. Not coarse-only, not fine-grained Kubernetes-style.
- **Custom apps can request managed services.** Allowed, not encouraged.
- **No inter-app dependencies in v1.** Apps are self-contained. If they need multiple services, they go in the same compose. Cross-app sharing only via shared use-case folders (`user_folders` for same-user apps; `shared_folders` for household-wide apps).
- **Manifest can live in-repo or in malmo's catalog repo.** Both patterns supported indefinitely. Schema is identical in both cases. We bootstrap by writing manifests for popular apps; over time, upstreams ship their own.
- **Image references use version tags; the store catalog resolves digests.** Authors write `image: foo/bar:1.2.3`; malmo's CI pins the bytes via a `sha256:` digest in the signed catalog (`APP_STORE.md`). Door-2 custom apps fall back to TOFU digest pinning in the brain.

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
