# molma App Store

> How molma publishes and serves the catalog of installable apps to every box. Companion to `APP_MANIFEST.md` (the contract being published), `APP_LIFECYCLE.md` (what the box does after fetching), `UPDATES.md` (the update flow that consumes catalog version bumps), and `RELEASE_MANIFEST.md` (the sibling doc this one borrows its publishing shape from).

The scope here is the **app catalog**: how apps reach a molma box, what the box trusts, and what infrastructure we run to publish it. Container images themselves are not hosted by us — they live in their authors' registries (Docker Hub, GHCR, …). What we publish is the **signed metadata** that tells the box which image bytes to trust for each app version.

## What the store is

A static, signed JSON catalog served from a CDN, backed by a git repo. Same shape as `RELEASE_MANIFEST.md`:

```
https://store.molma.network/catalog.json
https://store.molma.network/catalog.json.minisig
https://store.molma.network/apps/<id>/manifest.yml
https://store.molma.network/apps/<id>/docker-compose.yml
https://store.molma.network/apps/<id>/icon.png
https://store.molma.network/apps/<id>/screenshots/...
```

`catalog.json` is the **index** — one entry per app with the current published version, content hashes, and resolved image digests. Per-app `manifest.yml` and `docker-compose.yml` are fetched on demand at install time. The catalog itself is small even at scale (~300 bytes per entry — see Scaling below).

The brain polls the catalog on the same hourly cadence as the release manifest. When an app's version in the catalog advances past what's installed, the per-app update flow fires (`UPDATES.md` # 4). When the user opens "Browse store," the brain hands the cached catalog to the UI.

## Catalog schema (v1)

```json
{
  "manifest_version": 1,
  "generated_at": "2026-05-17T08:00:00Z",
  "apps": {
    "photoprism": {
      "version": "2.4.1",
      "name": "PhotoPrism",
      "categories": ["media", "photos"],
      "short_description": "Self-hosted photo library with AI tagging",
      "icon_url": "/apps/photoprism/icon.png",
      "manifest_url": "/apps/photoprism/manifest.yml",
      "manifest_hash": "sha256:def456...",
      "compose_url": "/apps/photoprism/docker-compose.yml",
      "compose_hash": "sha256:fed321...",
      "images": {
        "photoprism/photoprism:2.4.1": { "digest": "sha256:abc123...", "download_bytes": 612000000, "disk_bytes": 1480000000 },
        "mariadb:11.0.2": { "digest": "sha256:789xyz...", "download_bytes": 118000000, "disk_bytes": 402000000 }
      },
      "footprint": {
        "image_download_bytes": 730000000,
        "image_disk_bytes": 1882000000,
        "estimated_state": "10GB"
      },
      "files_first_class": true
    },
    "immich": { ... }
  }
}
```

Fields per app:

- **`version`** — semver of the currently-promoted version. Bumping this is the publish action.
- **`name`, `categories`, `short_description`, `icon_url`** — for the browse UI. The browse view is rendered from these alone, without fetching individual manifests.
- **`manifest_url` / `manifest_hash`** — content-addressed pointer to the full manifest. On install, brain fetches and verifies the hash matches.
- **`compose_url` / `compose_hash`** — same, for the compose file.
- **`images`** — map of `image:tag` (as referenced in the compose) → `{ digest, download_bytes, disk_bytes }`. CI resolves all three at catalog-build time from the registry: `digest` (the pinned bytes — see Trust below; the brain pulls by digest, not by tag), `download_bytes` (sum of the image's compressed layer sizes — the bandwidth/time cost), and `disk_bytes` (sum of its uncompressed layer sizes, deduping layers shared *within this app's own image set* — the on-disk cost). Sizes are **display-only and advisory** (# Trust model); only `digest` gates the pull.
- **`footprint`** — per-app summary so the **browse grid renders the size without fetching the full manifest**: `{ image_download_bytes, image_disk_bytes, estimated_state }`. CI computes the two image totals by summing the `images` entries and hoists `estimated_state` verbatim from the manifest's `storage.estimated_size` (`APP_MANIFEST.md` # Storage; absent if the manifest omits it). The image totals are an **upper bound** — they assume nothing is cached locally; the install dialog shows a sharper, box-specific number that subtracts already-present images (`BRAIN_UI_PROTOCOL.md` # GET /api/v1/catalog/:id/install-plan). `estimated_state` is the **measured app-state baseline at install** (`DECISIONS.md` 2026-06-09), not a usage projection — the same value on the card and in the dialog.
- **`files_first_class`** — true when the manifest declares `folders` and does not set `storage.app_managed_user_content`. Surfaces as a badge in the UI; not a gate.

Top-level fields:

- **`manifest_version`** — schema version. Brain ignores unknown fields so new optional fields land additively.
- **`generated_at`** — informational; not used for any gating.

Anything not in the schema is implicit (per-app file paths follow the URL convention) or out of scope (rollout pacing, telemetry — not in scope for the store).

## Trust model — what's signed, what's pinned

The catalog is **signed with minisign (Ed25519)** by the molma store key. Brain verifies on every fetch and refuses to act on an unsigned or invalidly-signed catalog.

- **Pubkey is baked into the brain image** at build time. Same forward-compat pattern as `RELEASE_MANIFEST.md`: verifier accepts a **list** of pubkeys, so rotation is dual-sign-then-drop without a flag day.
- **Store signing key is separate from the release-manifest signing key.** Different blast radius — a compromised store key lets an attacker publish a malicious app manifest; a compromised release key lets them ship a malicious brain. Separating them limits damage.

**Image bytes are pinned by digest in the catalog, not in the manifest.** Authors declare `image: photoprism/photoprism:2.4.1` (version, ergonomic). CI resolves the digest at catalog-build time and writes it into the `images` map. The brain pulls by `@sha256:...` derived from the catalog. The signed catalog is the binding from "the molma store promises version 2.4.1" to "these specific bytes."

Consequences:

- Tag mutation on an upstream registry (intentional or compromised) does not affect installed boxes — they pulled the digest the catalog promised.
- A new release of the app is a CI run that resolves the new digest and a PR that bumps `version` + `images` in the catalog.
- Authors never manage SHAs; manifest stays readable and portable (the same manifest still runs outside molma with normal `docker compose pull`).

**Image sizes are display-only, not part of the trust binding.** The same CI run that resolves a digest also records the image's `download_bytes` / `disk_bytes` (# Catalog schema). These exist purely to tell the user the on-disk footprint before they install; they gate nothing — a size that drifts from reality is a cosmetic bug, not an integrity failure. Only the digest binds bytes.

**What we don't sign:** individual manifests / compose files don't carry their own signature. Their integrity is bound to the catalog via the `manifest_hash` / `compose_hash` fields. One signed root, hash-chained leaves — same shape as the well-known package-manager pattern.

**What we don't host:** container images live wherever the author publishes them. We don't mirror Docker Hub. The "your app keeps working if the original developer disappears" pitch is delivered by the **running box's local image cache**, not by us re-hosting upstream artifacts. Mirroring is a Tier-3 future concern.

## Verification lives in the brain

`host-agent` verifies the release manifest because the release manifest controls *the brain itself* — verifying there avoids "brain verifying its own upgrade." The app catalog is a higher-frequency, lower-stakes feed about *apps the brain manages*. Verification belongs in the brain:

- The brain already speaks to Docker, owns the install transaction (`APP_LIFECYCLE.md`), and is the place per-app state lives.
- Keeps host-agent narrow — its job is host-level concerns, not app-catalog parsing.
- The store pubkey baked into the brain image cycles on the brain's release cadence, which is exactly where we want it.

`host-agent` stays the verifier for the release manifest. Two verifiers, two keys, two scopes — the small extra surface buys clear layering.

## Submission and promotion

The catalog source of truth is a git repo (`github.com/molma/store` or similar). Each app is a directory:

```
github.com/molma/store
├── apps/
│   ├── photoprism/
│   │   ├── manifest.yml
│   │   ├── docker-compose.yml
│   │   ├── icon.png
│   │   ├── screenshots/
│   │   └── CHANGELOG.md
│   ├── immich/
│   └── ...
├── catalog.json            ← generated by CI from the tree
└── catalog.json.minisig    ← signed by maintainer offline
```

A publish — new app or new version of an existing app — is a pull request that updates the app's directory. CI on the PR validates:

- Manifest parses against the schema (`APP_MANIFEST.md`).
- Compose passes the admission rules (`APP_LIFECYCLE.md` # admission policy) — no host port bindings, no `cap_add`, no host networking, etc.
- All images referenced in the compose are pullable from their registries.
- Image digests resolve and are recorded in `catalog.json`.
- Hashes recomputed; signature on `catalog.json.minisig` verifies.

The maintainer signs `catalog.json` offline (hardware token), commits `.minisig` alongside, opens the PR. On merge, the CDN syncs within seconds and boxes pick up the change on their next hourly poll.

For the v1 single-maintainer phase, self-merge is fine. Branch protection (require an additional reviewer) is a one-setting change with no doc impact.

## v1 catalog is hand-curated by molma

The first apps are written by us — manifests wrapping popular open-source projects (Immich, Paperless-ngx, Jellyfin, Navidrome, etc.). Authors aren't yet submitting their own manifests; the store repo is the catalog.

This shapes the v1 trust model intentionally:

- **Every manifest is signed-by-molma** because every manifest is *authored-by-molma*.
- **Curation policy is enforced by review**, not by automation — we set the bar (`files_first_class` preferred; `app_managed_user_content` rare and labeled; stdout logging; declared-vs-actual permission match).
- **Third-party authorship** lands later — when the catalog ecosystem matures, app authors will submit PRs against `molma/store` (still signed by us) before the model evolves further into per-store keys.

The data model below already accommodates additional catalogs from day one, so the transition is additive when it happens.

## Multiple catalogs — data model only in v1

`SPEC.md` and `APP_MANIFEST.md` both commit to third-party stores as a long-term shape. v1 does **not** ship UI for adding them, but the brain's data model treats "the catalog" as one entry in a list:

```
catalogs:
  - id: molma
    name: molma
    url: https://store.molma.network/catalog.json
    pubkeys: [<minisign-pubkey>]
    builtin: true
```

A third-party catalog later is the same row with `builtin: false` and its own URL + pubkeys, added through a settings flow that doesn't exist yet. The brain's verify-fetch-install pipeline already operates per-catalog. Apps include their `catalog_id` in SQLite so "this app came from store X" is recorded from day one — avoiding a retrofit when the second catalog ships.

The UI in v1 shows one tab: the molma store. No settings affordance to add another.

## Scaling: when single-file catalog becomes too much

Numbers: each catalog entry is ~300 bytes JSON. 100 apps = ~30 KB. 1000 apps = ~300 KB. 10,000 apps = ~3 MB. Single-file is fine well past the realistic v1 horizon. Hourly fetch of a few hundred KB is unremarkable.

Migration when it eventually bites is additive because consumers already fetch through an index:

- Today: `catalog.json` contains all entries inline.
- Later: `catalog.json` becomes a shard index pointing to per-category files (`/shards/media.json`, `/shards/productivity.json`), each independently signed. Brain learns to follow shard pointers.

Older brains that don't know about shards keep working as long as we keep serving the flat form during the transition window. Defer until the catalog crosses ~1 MB or hourly fetch latency becomes user-visible.

Browse UI groups by category regardless of file shape — the grouping is a UI concern, not a transport concern.

## Failure modes

- **Box can't reach `store.molma.network`:** brain keeps the last-known catalog cached at `/var/lib/molma/store-cache/catalog.json` (with its signature). Browse view shows cached entries with a "last updated X ago" notice. Installs of cached apps still work as long as upstream image registries are reachable. Updates pause until the catalog refresh succeeds.
- **Signature verification fails:** brain logs and ignores the fetched catalog. Previous valid catalog stays in effect. Persistent signature failure surfaces as a dashboard warning after 24 hours.
- **Manifest or compose hash mismatch at install time:** install refuses. The catalog promised specific bytes; if the per-app fetch doesn't match, something is wrong (CDN corruption, tampered file). User sees an error; brain logs the mismatch.
- **Image pull fails at install time:** standard install failure, surfaced per `APP_LIFECYCLE.md` # install transaction.
- **Image digest changes upstream between catalog publish and box pull:** the box pulls by digest, so the upstream's new bytes don't affect it. The box installs the bytes the catalog promised. If the digest was *deleted* from the upstream registry (rare — most registries keep digests addressable), the install fails with a registry-side error.

## What we run

For v1, end-to-end:

1. **Git repo `molma/store`** (free on GitHub).
2. **CI on the repo** — schema lint, admission check, image-pullability check, digest resolution, catalog regeneration. GitHub Actions is sufficient.
3. **CDN at `store.molma.network`** — same provider class as `releases.molma.network`. Cloudflare R2 / Pages, or whatever the release-manifest CDN ends up on. The two CDNs can share infra; they don't have to.
4. **One signing keypair** for the store catalog — offline, hardware-token-protected. Separate from the release-manifest key.
5. **Store pubkey baked into the brain image** at brain build time.

No application server. No backend service. Static files, signed, served from a CDN. The publish flow is git-driven; the verification flow is offline + bake-into-binary. Costs are dominated by CDN egress, which at v1 scale is rounding error.

## Locked decisions

- **Catalog is a static signed JSON file** (`store.molma.network/catalog.json`) served from a CDN, backed by a git repo. Mirrors `RELEASE_MANIFEST.md` in shape.
- **Signed with minisign (Ed25519).** Store key is **separate from the release-manifest key**. Verifier accepts a **list of pubkeys** for forward-compatible rotation.
- **Authors declare image versions; CI resolves digests into the catalog.** Brain pulls by digest. The signed catalog is the binding from version to specific bytes — tag mutation on upstream registries can't ship malicious code to molma boxes.
- **Per-app manifest and compose files are bound to the catalog by content hash**, not individually signed. One signed root, hash-chained leaves.
- **Verification happens in the brain** (not host-agent). Brain owns app lifecycle; the store pubkey ships with the brain image.
- **We don't host container images.** Authors publish to their own registries. The box's local image cache delivers the "app keeps working if the developer disappears" property. Image mirroring is deferred.
- **v1 catalog is hand-curated by molma.** Every manifest is molma-authored and molma-signed. Third-party authorship (PRs against `molma/store`) lands later; per-store keys land later still.
- **Data model supports multiple catalogs from day one** (catalog id, URL, pubkey list). v1 ships UI for one catalog only.
- **Single `catalog.json` file in v1.** Sharding is additive when the file crosses ~1 MB or fetch latency becomes user-visible.
- **Promotion is a PR against `molma/store`** with regenerated catalog + signature. CI validates schema, admission rules, image reachability, hashes, signature. Merge to `main` is the publish action.

## Open questions

Tracked centrally in [`NEXT.md`](NEXT.md). Resolutions land back here (or in `DECISIONS.md` if they flip a position).
