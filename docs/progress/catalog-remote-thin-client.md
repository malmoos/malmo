# Catalog remote thin-client + last-good cache (OS half of cloud #62)

- **Status:** done (full cutover — no baked catalog in the image)
- **Date:** 2026-07-02
- **Specs touched:** `docs/architecture.md` (catalog package row + app-store deferred-list note); `docs/specs/DECISIONS.md` (the unify-both-profiles + no-signing decision); `docs/specs/APP_STORE.md` (superseded banner — the shipped model is the dynamic `/catalog/sync` thin-client fetch with a last-good cache and TLS + integrity digest, not the static minisign-signed CDN it described); `docs/specs/NEXT.md` (publish-mechanism "pinned"/"signed" language corrected). cloud `specs/CATALOG.md` is the contract this consumes.

The OS half of catalog "step 3" (cloud issue `malmoos/cloud#62`): turn the box brain from a disk-backed catalog reader into a thin HTTP client of the control plane's public-read catalog API, with a last-good on-disk cache — and complete the cutover so **no `catalog/` directory ships in the OS image**. The cloud half is already shipped and stable (cloud `docs/progress/catalog-thin-client-contract.md`); this is the coordinated box side. It supersedes `hosted-image-bake-catalog.md`, which baked the catalog into the hosted image as a stopgap.

This entry folds in a mid-flight design change (owner call, recorded in `DECISIONS.md` 2026-07-02): an earlier cut wired the remote client for hosted only and kept the appliance on the baked directory, deferring signing. The final shape is simpler — **every profile is a thin client, the baked catalog and `MALMO_CATALOG_DIR` are gone, and there is no Ed25519 signing** (TLS to the control plane + the integrity digest are the trust story).

## What was done

Everything lives in `internal/catalog`, behind the unchanged six-method surface (`List`, `Entry`, `Detail`, `IconPath`, `ScreenshotPath`, `Load`) that `internal/api` and `internal/lifecycle` consume. Those two packages — and all their tests — are untouched: they still hold a `*catalog.Catalog`.

- **`catalog.go` — facade.** `Catalog` is now a thin facade over a private `source` interface (the six methods). `New(root)` builds the disk source (unchanged behavior); `NewRemote(opts)` builds the control-plane client. `StartRefresh(ctx)` starts the remote's background sync loop and is a no-op on disk, so `cmd/brain` calls it unconditionally. The `Entry`/`Detail` types and the brain-origin `iconURL`/`screenshotURL` helpers stay here, shared by both sources.
- **`disk.go` — the original reader,** moved verbatim onto a `diskSource` (receiver rename only). Production no longer uses it; it is retained as the constructor `internal/api` / `internal/lifecycle` tests build a controlled catalog with via `catalog.New` off a temp dir (it earns its keep there — migrating ~10 test files to remote stubs would be pure churn for no coverage gain).
- **`wire.go` — the wire mirror.** A byte-faithful mirror of the cloud `CatalogFile`/`App` (cloud `internal/catalog/published.go`): same fields, same JSON tags, same declaration order, reusing the os `manifest` types (`Author`/`Links`/`Footprint`/`ImageRef`) the cloud itself mirrored. `indexDigest` recomputes the hex SHA-256 over `json.Marshal(apps)` and `verify` checks it against the stamped `index_sha256` plus the schema version. `parseSnapshot` = unmarshal + verify, the single door every snapshot enters through (network or cache).
- **`remote.go` — the thin client.** `remoteSource` fetches `GET {base}/catalog/sync`, verifies, swaps an indexed in-memory snapshot in under a write lock, and writes the verified bytes through to `CacheDir/catalog.json` (atomic temp+rename). Reads project from the snapshot under a read lock — never blocking on the network. `NewRemote` loads the last-good cache synchronously at construction (offline browse from boot); `startRefresh` runs one immediate sync then one per interval, bound to the process-lifetime context. `If-None-Match` against the stored ETag gives a cheap `304` no-op when nothing changed. Env filtering (`visibleIn`) is the box's only browse-time filter: `List`/`Detail` are env-gated, `Entry`/`Load` are not (installed-instance enrichment and install-by-known-id resolve any app in the snapshot). `Load` re-parses `app.manifest` with the box's own `manifest.Parse` — the box stays the sole enforcer of the manifest contract.
- **Asset proxy+cache.** `IconPath`/`ScreenshotPath` fetch the underlying control-plane asset once, cache it under `CacheDir/assets/<id>/<file>` (path-contained defensively), and return the local file path — so `internal/api`'s `http.ServeFile` handlers and the UI's hard-coded `/api/v1/catalog/{id}/icon` + `/screenshots/{n}` routes are unchanged. The box UI is never pointed at the public control plane directly (origin boundary).
- **`cmd/brain/main.go` — wiring.** Every profile constructs `NewRemote`. Env: `MALMO_CATALOG_URL` (default `https://malmo.network`), `MALMO_CATALOG_CACHE_DIR` (default `/var/lib/malmo/catalog-cache`), `MALMO_CATALOG_REFRESH` (default 15m). `cat.StartRefresh(pollCtx)` starts the loop alongside the other background pollers.
- **Env plumbing.** `MALMO_CATALOG_DIR` is retired everywhere: `cfg.catalogDir` (cmd/brain), `brainlaunch.Config.CatalogDir` + its env pass-through, `cmd/host-agent-real`'s default, and the `Makefile` export are gone. `brainlaunch` now carries `CatalogURL` + `CatalogCacheDir` (emitted to the brain container as `MALMO_CATALOG_URL` / `MALMO_CATALOG_CACHE_DIR` when set); `host-agent-real` defaults the cache dir under `DataDir` (so it rides the existing mount) and leaves the URL empty (the brain's own default stands). `make dev` points the cache at `./.dev/catalog-cache`.
- **Image debake.** The repo-root `catalog/` directory is deleted; the bake steps in `dev/cloud/stage-control-plane.sh` and `dev/test-qemu/bootstrap.sh` (and the staged `mkosi.extra.wiring` / `mkosi.extra` copies) are removed. No `catalog/` ships in any image — cloud #62's last acceptance criterion.

## Seam decisions

- **Env detection = the resolved profile.** The box already resolves `profile.Read` to `appliance`|`hosted` at startup; those are exactly the cloud's `environments` values, so `Environment: string(prof)` needs no new signal.
- **Asset proxying, not redirect.** The brain fetches+caches the CP asset and serves the local file, keeping the box UI on the box origin and giving offline browse for free once an asset is cached. A redirect would leak the box UI onto the public CP origin.
- **Unify both profiles; the last-good cache is the offline story.** Both appliance and hosted construct `NewRemote`. There is no profile that has, by construction, no network path to the (public-read) control plane, and installing an app needs internet to pull images regardless — so a per-profile split earned only a second code path and a baked artifact to keep in sync. A synced-then-offline box browses from cache; a never-synced box shows an empty store (documented, accepted). See `DECISIONS.md` 2026-07-02.
- **No signing.** The box only ever fetches the catalog from the malmo control plane over TLS, which authenticates that origin; the integrity digest catches truncation/corruption. An Ed25519 signature would re-authenticate bytes TLS already covers, for a key-distribution cost and no threat it closes — dropped, not deferred (owner call).
- **Refresh cadence 15m + immediate first sync.** The catalog changes rarely (a store publish) and every poll is a cheap `304`, so the cadence is loose; the immediate first sync means a freshly provisioned box populates its store promptly rather than waiting an interval.

## Air-gapped QEMU lane (`dev/test-qemu`)

The full-stack lane installs `whoami` end-to-end **air-gapped** (`restrict=on`), so it can't reach a control plane. Rather than run a stub catalog server, it **pre-seeds the brain's last-good cache**: `dev/test-qemu/mkcatalog` generates a one-app snapshot (the `whoami` package + a `documents:write` folder grant, digest stamped) into `/var/lib/malmo/catalog-cache/catalog.json` at image-build time, and the brain reads it at boot exactly as it would a synced-then-offline snapshot (`remote.go` # `loadCache`) and installs from it. The catalog URL is pointed at an inert address (`http://127.0.0.1:9`) so the background sync fails fast; `MALMO_OFFLINE_INSTALL` stays on (the whoami image is a docker-loaded tarball, digest-trusted). This exercises the real remote read path (verify → project → `Load`) with no `catalog/` directory in the image. `mkcatalog` reuses the `internal/manifest` types and mirrors `wire.go`, so its digest reproduces the box's; a throwaway test confirmed the box's real `parseSnapshot` + `Load("whoami")` accept its output.

## Tests

`internal/catalog` unit tests, all green (`go test ./internal/catalog/`):

- **Digest reproduction against the real snapshot.** `testdata/snapshot.json` is a pinned copy of the control plane's published `dist/catalog.json` (19 apps); `TestVerifyRealSnapshot` asserts the mirror re-marshals to the exact stamped `index_sha256`. This is the box↔cloud contract in a test — if the wire shape drifts from the cloud's, it fails.
- **Verify refusals** — wrong schema version, digest mismatch, truncated body.
- **Remote projections + env filtering** — an appliance-env source sees only its apps, a hosted-env source sees all; `Entry` resolves cross-env, `Detail` is env-gated; `Load` re-parses the manifest and returns verbatim compose.
- **Last-good fallback** — a box synced-then-offline still browses from cache; a failed sync keeps the last-good snapshot; a never-synced box is an empty store.
- **Integrity refusal** — a tampered served body fails verify and does not become the read source.
- **Asset proxy+cache** — icon fetched once then served from cache (hit count asserted); unknown/no-icon/out-of-range are `ErrNotFound`.
- **`304` path** — a matching `If-None-Match` is a no-op that keeps the snapshot.

`internal/api`, `internal/lifecycle`, `internal/hostagent/brainlaunch` (updated for the `CatalogURL`/`CatalogCacheDir` env), and `cmd/brain` suites all green. `gofmt`/`go vet` clean on the touched files; `bash -n` clean on the three edited shell lanes; `mkcatalog` runs and its output verifies against the box's real `wire.go`.

## Review follow-ups (PR #294)

A review found one Block and four Notes; all fixed on this branch:

- **Block — `cmd/malmo` suite broken by the `catalog/` deletion.** `cmd/malmo/check_test.go` / `main_test.go` read `../../catalog/{whoami,files-demo}/manifest.yml`, which this PR deleted (`go build` passed but the suite failed). Recovered the two known-good packages from git into `cmd/malmo/testdata/{whoami,files-demo}/` (local fixtures, decoupled from the QEMU lane) and repointed both tests. `go test ./...` is green now (bar the pre-existing `msteinert/pam` cgo gap).
- **Note — unbounded `io.ReadAll` on network bodies.** `syncOnce` and the asset fetch now read through `readLimited` (an `io.LimitReader` with an overflow check): `maxSnapshotBytes` = 32 MiB, `maxAssetBytes` = 16 MiB — far above any real payload, so a compromised/MITM'd control plane can't pressure box memory; exceeding is a fetch failure (last-good stands).
- **Note — concurrent cache-miss double-fetch.** `cachedAsset` gained a per-asset mutex map (`assetLock`), so N simultaneous first-time requests for one asset do one fetch, not N; the fast-path cache hit stays lock-free. Dep-free (`golang.org/x/sync` isn't in `go.mod`).
- **Note — `StartRefresh` double-call.** `remoteSource.started` (an `atomic.Bool` CAS) makes a repeat `startRefresh` a no-op — one sync loop only.
- **Note — stale signing docs.** `APP_STORE.md` + `NEXT.md` updated (above).

New tests: `TestRemoteSnapshotSizeCapRejects`, `TestRemoteAssetFetchCollapsesConcurrent` (asserts one fetch under 20 concurrent requests, `-race`), `TestRemoteStartRefreshIsIdempotent`.

## Known gaps / not verified here

- **QEMU + cloud boot-acceptance is deferred**, as with every sibling `dev/cloud` / `dev/test-qemu` change — those lanes need root + KVM + swtpm and aren't in the normal loop. The migration is `bash -n`-clean and the snapshot generator is verified against the box code, but the end-to-end air-gapped install (`whoami` from the pre-seeded cache) and the cloud control-plane-up proof are validated on the next `make deploy-image` + redeploy, not here. The one behavioral thing they rely on — the remote client degrading to an empty store (no hard error) when `/catalog/sync` is unreachable — is covered by the unit tests.
- **App authoring workflow.** `catalog/` was also the door-1 authoring surface (`docs/dev/authoring-apps-with-an-agent.md`). Apps are now authored in the cloud-side store and published to the catalog API; reconciling that how-to doc is a cloud-side follow-up, out of this change's scope.
