# The box keeps no catalog on disk; assets expire after a day

- **Status:** done
- **Date:** 2026-08-17
- **Specs touched:** `APP_STORE.md`, `DECISIONS.md` (new 2026-08-17 entry), `NEXT.md`, `architecture.md`

Follow-up to [`catalog-remote-thin-client.md`](catalog-remote-thin-client.md), which made every box a thin client of the control plane's catalog API with a last-good on-disk cache, and to [`catalog-wire-guards.md`](catalog-wire-guards.md), which added the unknown-key guard and argued for pinning a copy of a real published snapshot as its fixture. Both are frozen records of what we thought then; this slice changes two of those choices.

The box no longer writes the catalog snapshot to disk at all. It holds one in memory, re-fetches it, and shows an empty store when it cannot. Icons and screenshots are still cached, now with a 24-hour expiry. The pinned test fixture is now synthetic rather than a copy of a published snapshot.

## What was done

### The snapshot lives in memory only (`internal/catalog/remote.go`)

`loadCache`, `cachePath`, `cacheFileName` and the write-through in `syncOnce` are gone. `RemoteOptions.CacheDir` became `AssetCacheDir`, which is now what its name says: proxied icons and screenshots, never the catalog. A failed sync still leaves the snapshot the process already holds in place, so a control-plane blip during uptime changes nothing; what changed is that nothing survives the process.

The reasoning is in `DECISIONS.md` 2026-08-17, but the short form: browsing a stale catalog was never the same as being able to install from it, because installing pulls images over the same network; and a box pinned to an old snapshot can offer a manifest the store no longer publishes. Empty is a state that recovers on the next sync. `TestRemoteKeepsNoSnapshotOnDisk` pins this — it syncs, asserts a fresh source over the same directory starts empty, and globs for a stray `*.json`.

### A dev/test seam replaces the pre-seeded cache (`MALMO_CATALOG_FILE`)

Three lanes booted a brain from a pre-seeded cache file plus a dead catalog URL: `make dev-app` / `make seed-catalog`, `dev/test-qemu/bootstrap.sh`, and `dev/cloud/test/bootstrap.sh`. They now pass the snapshot explicitly as `MALMO_CATALOG_FILE`, read once at construction by `loadSnapshotFile` and never written back. (`dev/test-health.sh` set a dead URL but never seeded a cache, so it needs no change.) It is an input, not a cache: `TestRemoteSnapshotFileSeed` asserts the file's mtime and size are untouched after a sync attempt. A missing or corrupt seed leaves the store empty rather than failing construction — a dev seed must not be able to stop a box booting.

Plumbed through `cmd/brain` (`catalogSnapshotFile`), `brainlaunch.Config.CatalogFile` → `MALMO_CATALOG_FILE`, and `cmd/host-agent-real`. Production sets none of it. The Makefile writes its seed to `.dev/catalog-seed.json` (`CATALOG_SEED`) instead of into the cache dir.

### Assets expire after 24 hours

`cachedAsset` used to treat "file exists" as a hit, forever. Asset filenames are stable per app (`icon.png`, `screenshots/0.png`), so the first icon a box ever fetched was the icon it served for the life of the box, and republished artwork never reached the fleet. `fresh()` now checks mtime against `assetTTL`; an expired file is refetched, and a refetch that fails serves the expired copy rather than an error, because stale artwork beats a broken image. Covered by `TestRemoteAssetExpires` and `TestRemoteExpiredAssetSurvivesFailedRefresh`.

### The fixture is synthetic (`internal/catalog/testdata/snapshot.json`)

It was a pinned copy of the control plane's published `dist/catalog.json` — by the end, 35 apps with every authored `manifest`, `compose` and description, in a public repo. App artifacts are authored in `malmoos/store` and reach a box only through the published snapshot; this repo holds the schema and the tooling, not the artifacts. The fixture is now three hand-written fake apps (`alpha-notes`, `beta-media`, `gamma-demo`) covering the full key surface, including the nested `footprint`/`author`/`links`/`images` shapes, `external_costs`, `home` and `categories`.

`TestVerifyRealSnapshot` became `TestVerifyFixtureSnapshot` and gained a `-update` flag that re-stamps `index_sha256` from the fixture's own apps array, so editing the shape by hand doesn't mean computing a SHA-256 by hand. It rewrites that one value with a regexp rather than re-marshalling, so the hand-authored file keeps its formatting.

## How it maps to the specs

`APP_STORE.md` carried the cache in six places — the superseded banner, Failure modes, the digest-failure note, the Landing page projection, What the box models, and Locked decisions — all rewritten. Its Failure modes section now states plainly that empty beats stale and why. `architecture.md`'s catalog row and app-store bullet, and `NEXT.md`'s publish-mechanism sentence, follow. `DECISIONS.md` gets a 2026-08-17 entry, since this flips part of 2026-07-02.

On the dev side, `CLAUDE.md` # Catalog apps and `docs/dev/contributing.md` now state the boundary where someone lands before writing anything: artifacts are authored in the store, a box pulls the snapshot from a malmo endpoint and keeps no copy, and fixtures here are synthetic. `docs/dev/running-locally.md` documents `MALMO_CATALOG_FILE` next to `MALMO_CATALOG_CACHE_DIR` and says which is which, and its stale "Sample manifests — `catalog/`" line (wrong since the thin-client cutover) is fixed.

## Known gaps & deviations

- **`TestNoUnmodeledFields` lost its upstream reach.** Against a real snapshot it could catch the published shape moving. Against a synthetic one both sides of the comparison live in this repo, so it now only catches a `json` tag changing in `wire.go` without the pinned shape following. Detecting that a newly published field is one the box does not model has to be a publish-side check, on the side that holds the published snapshot. That is a cloud-side change and is **not done** — no issue filed yet. The test's doc comment and `APP_STORE.md` # What the box models both say this outright rather than implying the old guarantee still holds.
- **A digest-mismatch freeze now empties the store instead of freezing it.** A box that rejects a snapshot for an unmodeled per-app field used to keep serving its cache and look healthy. It now shows an empty store after a restart. Worse cosmetics, better signal, and recorded as such in `APP_STORE.md` — but it is a user-visible change in a rare case, not a pure improvement.
- **The 24-hour asset TTL is a proxy for the right key.** The wire carries no per-asset digest, so the box cannot tell that an icon actually changed. A digest on the wire would let the cache be exact; that is a two-repo change and was not attempted here.
- **The fixture was public for about six weeks** (2026-07-02 to 2026-08-17, `origin/main` and `origin/dev`). History was deliberately not rewritten, so the old blobs stay reachable in git history and in existing clones and forks. This slice stops it going forward; it does not undo it.
- `make check` passes except the PAM package, which needs `libpam0g-dev` (`make test-nopam` is green, plus `fmt-check`, `vet`, `openapi-check`). The QEMU lanes were syntax-checked, not booted — `dev/test-qemu` is local-only and the cloud lane runs in CI.

## What's next

- File a cloud-side issue for the publish-side shape check that replaces what the real fixture used to give us, and link it from `APP_STORE.md` # What the box models once it exists.
- Consider a per-asset digest on the catalog wire, which would replace the TTL with an exact invalidation key and let the cache be both fresher and quieter.
- The cloud QEMU lane (`CI / Cloud image`) exercises `MALMO_CATALOG_FILE` on a real boot; run it on this branch before merge.
