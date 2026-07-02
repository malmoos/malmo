# Catalog remote thin-client + last-good cache (OS half of cloud #62)

- **Status:** in progress (source built, wired for hosted; final cutover deferred)
- **Date:** 2026-07-02
- **Specs touched:** `docs/architecture.md` (catalog package row + deferred-list note); cloud `specs/CATALOG.md` is the contract this consumes.

The OS half of catalog "step 3" (cloud issue `malmoos/cloud#62`): turn the box brain from a disk-backed catalog reader into a thin HTTP client of the control plane's public-read catalog API, with a last-good on-disk cache. The cloud half is already shipped and stable (cloud `docs/progress/catalog-thin-client-contract.md`); this is the coordinated box side. It follows `hosted-image-bake-catalog.md`, which baked the catalog into the hosted image as a stopgap and named "a hosted catalog-sync path is separate future work" — this is that work.

## What was done

Everything lives in `internal/catalog`, behind the unchanged six-method surface (`List`, `Entry`, `Detail`, `IconPath`, `ScreenshotPath`, `Load`) that `internal/api` and `internal/lifecycle` consume. Those two packages — and all their tests — are untouched: they still hold a `*catalog.Catalog`.

- **`catalog.go` — facade.** `Catalog` is now a thin facade over a private `source` interface (the six methods). `New(root)` builds the disk source (unchanged behavior); `NewRemote(opts)` builds the control-plane client. `StartRefresh(ctx)` starts the remote's background sync loop and is a no-op on disk, so `cmd/brain` calls it unconditionally. The `Entry`/`Detail` types and the brain-origin `iconURL`/`screenshotURL` helpers stay here, shared by both sources.
- **`disk.go` — the original reader,** moved verbatim onto a `diskSource` (receiver rename only). This is the appliance's baked `catalog/` and the source every existing test constructs via `catalog.New`.
- **`wire.go` — the wire mirror.** A byte-faithful mirror of the cloud `CatalogFile`/`App` (cloud `internal/catalog/published.go`): same fields, same JSON tags, same declaration order, reusing the os `manifest` types (`Author`/`Links`/`Footprint`/`ImageRef`) the cloud itself mirrored. `indexDigest` recomputes the hex SHA-256 over `json.Marshal(apps)` and `verify` checks it against the stamped `index_sha256` plus the schema version. `parseSnapshot` = unmarshal + verify, the single door every snapshot enters through (network or cache).
- **`remote.go` — the thin client.** `remoteSource` fetches `GET {base}/catalog/sync`, verifies, swaps an indexed in-memory snapshot in under a write lock, and writes the verified bytes through to `CacheDir/catalog.json` (atomic temp+rename). Reads project from the snapshot under a read lock — never blocking on the network. `NewRemote` loads the last-good cache synchronously at construction (offline browse from boot); `startRefresh` runs one immediate sync then one per interval, bound to the process-lifetime context. `If-None-Match` against the stored ETag gives a cheap `304` no-op when nothing changed. Env filtering (`visibleIn`) is the box's only browse-time filter: `List`/`Detail` are env-gated, `Entry`/`Load` are not (installed-instance enrichment and install-by-known-id resolve any app in the snapshot). `Load` re-parses `app.manifest` with the box's own `manifest.Parse` — the box stays the sole enforcer of the manifest contract.
- **Asset proxy+cache.** `IconPath`/`ScreenshotPath` fetch the underlying control-plane asset once, cache it under `CacheDir/assets/<id>/<file>` (path-contained defensively), and return the local file path — so `internal/api`'s `http.ServeFile` handlers and the UI's hard-coded `/api/v1/catalog/{id}/icon` + `/screenshots/{n}` routes are unchanged. The box UI is never pointed at the public control plane directly (origin boundary).
- **`cmd/brain/main.go` — wiring.** Profile-gated source selection (see seam decisions): a hosted box with a catalog base URL gets `NewRemote`; everything else keeps `New(cfg.catalogDir)`. New env: `MALMO_CATALOG_URL` (default `https://malmo.network`), `MALMO_CATALOG_CACHE_DIR` (default `/var/lib/malmo/catalog-cache`), `MALMO_CATALOG_REFRESH` (default 15m). `cat.StartRefresh(pollCtx)` starts the loop alongside the other background pollers.

## Seam decisions

- **Env detection = the resolved profile.** The box already resolves `profile.Read` to `appliance`|`hosted` at startup; those are exactly the cloud's `environments` values, so `Environment: string(prof)` needs no new signal.
- **Asset proxying, not redirect.** The brain fetches+caches the CP asset and serves the local file, keeping the box UI on the box origin and giving offline browse for free once an asset is cached. A redirect would leak the box UI onto the public CP origin.
- **Profile-gated selection (materially better than a blanket replace).** The appliance ships with no guaranteed internet and installs offline from its baked catalog; making its store depend on the network would regress that. So the remote client is wired for `hosted` only; the appliance keeps the baked `catalog/` directory. Both back the same `*catalog.Catalog`, so the swap is invisible above the facade.
- **Refresh cadence 15m + immediate first sync.** The catalog changes rarely (a store publish) and every poll is a cheap `304`, so the cadence is loose; the immediate first sync means a freshly provisioned box populates its store promptly rather than waiting an interval.
- **Integrity, not authenticity.** The digest catches truncation/corruption/tamper of a fetched-or-cached snapshot; Ed25519 signing is the separate next step (below). A snapshot that fails verify never becomes the read source and is never cached.

## Tests

`internal/catalog` unit tests, all green (`go test ./internal/catalog/`):

- **Digest reproduction against the real snapshot.** `testdata/snapshot.json` is a pinned copy of the control plane's published `dist/catalog.json` (19 apps); `TestVerifyRealSnapshot` asserts the mirror re-marshals to the exact stamped `index_sha256`. This is the box↔cloud contract in a test — if the wire shape drifts from the cloud's, it fails.
- **Verify refusals** — wrong schema version, digest mismatch, truncated body.
- **Remote projections + env filtering** — appliance sees only its apps, hosted sees all; `Entry` resolves cross-env, `Detail` is env-gated; `Load` re-parses the manifest and returns verbatim compose.
- **Last-good fallback** — a box synced-then-offline still browses from cache; a failed sync keeps the last-good snapshot; a never-synced box is an empty store.
- **Integrity refusal** — a tampered served body fails verify and does not become the read source.
- **Asset proxy+cache** — icon fetched once then served from cache (hit count asserted); unknown/no-icon/out-of-range are `ErrNotFound`.
- **`304` path** — a matching `If-None-Match` is a no-op that keeps the snapshot.

`internal/api`, `internal/lifecycle`, and `cmd/brain` suites still green (unchanged consumers). `gofmt`/`go vet` clean on the touched files.

## What's next (the full cutover)

- **Retire the baked `catalog/` dir and its bake wiring.** Left in place deliberately so this change is reviewable and the disk fallback stays proven. The final cutover points the appliance at the CP too (or a signed offline bundle) and removes `catalog/`, the `stage-control-plane.sh`/`bootstrap.sh` catalog-bake steps (`hosted-image-bake-catalog.md`), and eventually `MALMO_CATALOG_DIR`.
- **Ed25519 signature verification.** This step ships integrity-digest verification only; authenticity (a forged snapshot from a MITM) needs signing + a box-side key-distribution contract, sequenced next (cloud `NEXT.md`).
- **`MALMO_CATALOG_DIR` retirement** — once the appliance no longer reads a baked dir.
- **Appliance-on-remote** — decide whether the appliance syncs from the CP (public-read, no account needed) or ships a signed offline bundle; today it stays on the baked dir.
