# Catalog image footprint: object-form `images` + size resolver

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** No spec change — `APP_STORE.md` # Catalog schema already specifies the object-form `images` map (`{digest, download_bytes, disk_bytes}`) and the per-app `footprint`; this realizes them in the skeleton. `NEXT.md` # Developer / app-author surface past-tensed (the size/digest resolver shipped). `docs/dev/authoring-apps-with-an-agent.md` updated (the manual digest step is now a CLI command; `storage.estimated_size` is now read).

Closes issue #69 (part of #68, "on-disk footprint before install"). The catalog schema promises each image's download/disk size and a per-app footprint so the store can show on-disk cost before install; the skeleton carried only hand-curated digests in a string-form `images` map. This migrates the data model to the object form, adds the `molma manifest resolve` subcommand that fills the sizes from the registry, and regenerates the three sample manifests. It is the root of the footprint chain — #70 (brain serves the footprint) and #71 (UI shows it) were blocked on it.

## What was done

### Data model (`internal/manifest`)

- **`Images map[string]ImageRef`** replaces `map[string]string`. `ImageRef{Digest, DownloadBytes, DiskBytes}` is the catalog's per-image object (`APP_STORE.md` # Catalog schema). Its `UnmarshalYAML` accepts **both** the object form and the legacy scalar shorthand (`image:tag: sha256:…`, digest only), mirroring the `HealthProbe` string-or-mapping pattern, so pre-#69 manifests still parse during the migration.
- **`Storage{EstimatedSize}`** — the manifest now reads `storage.estimated_size` (`APP_MANIFEST.md` # Storage). Only that one key; `data_volumes`/`cache_volumes` stay held by the compose, unparsed.
- **`Footprint{ImageDownloadBytes, ImageDiskBytes, EstimatedState}` + `(*Manifest).Footprint()`** — the per-app summary, **derived** (sums the `Images` entries, hoists `estimated_size`), never hand-authored. This matches the spec: the real `catalog.json` footprint is CI-generated, not an author field. #70 will surface it on the catalog browse `Entry`.
- **`ComposeImages(composeBytes)`** (in `synthesize.go`, next to `ComposeServiceNames`) — the distinct, sorted `image:` refs the resolver sizes. Distinct because services share images (the hermes gateway + dashboard run one binary).

### Resolver (`cmd/molma`)

- **`molma manifest resolve <manifest.yml>`** — the second `manifest` subcommand. It reads the sibling compose, resolves each distinct image's `{digest, download_bytes, disk_bytes}`, rewrites the manifest's `images` block in place, and prints the per-image sizes + the derived footprint. **Fails loudly** — a single unsizable image aborts the whole run with no write, so the catalog never carries a bogus zero (Done-when #3).
- **`dockerSizer`** drives the local Docker daemon (the maintainer's call over adding `go-containerregistry`; consistent with `internal/lifecycle/pinning.go`, which already shells to docker, and no new dependency): `docker manifest inspect` for the compressed download size (the amd64 sub-manifest's layer sizes — molma is x86-only), a `--platform linux/amd64` pull + `docker image inspect {{.Size}}` for the uncompressed disk size, and the pulled image's RepoDigest for the pinned (index) digest. It is behind an `imageSizer` interface so the parse/footprint/write-back logic is unit-tested with a fake.
- **Comment-preserving write-back** — `replaceImagesBlock` rewrites only the `images:` block as surgical text (block = the `images:` line through every following indented/blank line; appended at EOF if absent), leaving every other byte — the explanatory comment above the block, blank lines — untouched. A YAML re-encode was rejected because it collapses the curated manifests' blank lines and comment spacing. The regenerated content is re-`Parse`d before the file is written, so a malformed block can never land on disk.

### Install-time consumer + samples

- `internal/lifecycle/pinning.go` — the catalog-digest verification reads `man.Images[img].Digest` (was the bare string). Behaviour identical; the mismatch error still names the promised vs. served digest.
- `catalog/{whoami,files-demo,hermes-agent}/manifest.yml` — migrated to the object form by running the new subcommand. Real resolved sizes: whoami/files-demo (`traefik/whoami:v1.10.3`) 2.85 MB download / 2.85 MB disk; hermes-agent 1.13 GB / 1.13 GB. The resolved index digests match the values that were hand-curated, confirming the resolver agrees with the prior pins.

## How it maps to the specs

- `APP_STORE.md` # Catalog schema — the `images` object and `footprint` summary are now real types. # Trust model — sizes are display-only (`omitempty`, gate nothing); only `digest` binds bytes, enforced unchanged at install.
- `APP_MANIFEST.md` # Storage — `estimated_size` hoisted verbatim into `footprint.estimated_state`.
- `CLAUDE.md` # Go discipline — export-on-second-consumer (`ComposeImages`); no new dependency; the resolver's docker calls sit behind a consumer-side interface so the logic stays testable; `cmd/` owns file I/O, `internal/manifest` stays filesystem-free.

## Known gaps & deviations

- **Footprint is derived, not stored.** The spec puts `footprint` in the generated `catalog.json`, not the manifest. The skeleton has no `catalog.json` yet, so the manifest's `images` block stands in for it and `Footprint()` computes the summary on demand. #70 wires the derived footprint onto the catalog `Entry` and the install-plan response. No `DECISIONS.md` entry — nothing flipped; this realizes the locked schema.
- **Cross-image layer dedup is not implemented.** `disk_bytes` is each image's own `docker image inspect .Size` (uncompressed, already deduped *within* that image). When one app declares multiple **distinct** images sharing base layers, summing their `.Size` slightly over-counts the shared base. All three samples are single-distinct-image, so it is a no-op today; the spec's "dedup within the app's image set" is satisfied for them. Sizes are advisory, so the over-count is a cosmetic bug, not an integrity one — true cross-image dedup is a follow-up for the first multi-image curated app.
- **Resolver needs a Docker daemon + network.** `dockerSizer` is therefore not unit-tested (the `imageSizer` seam is faked in tests); it was verified by resolving the three real samples. A daemon-free registry client (`go-containerregistry`) would suit a no-Docker CI better — deferred with the catalog-repo CI.
- **No `catalog.json` generation.** This sizes the per-app manifest; rolling the tree into a signed `catalog.json` is still the follow-up the lint/resolve CLIs feed (`APP_STORE.md` # What we run).
- **Legacy scalar `images` form still parses.** Kept for migration back-compat; the curated catalog is fully object-form now.

## Tests

`go test ./cmd/molma/ ./internal/manifest/` green (plus the full non-PAM suite — the `pinning.go` consumer change rippled cleanly).

- `internal/manifest/images_test.go` — object-form parse; legacy scalar parse (sizes zero); `Footprint` sums + hoists `estimated_size`; the unset-`estimated_size` case; `ComposeImages` distinct/sorted + dedup + rejects (no services, no image).
- `cmd/molma/resolve_test.go` — `resolve` via a fake sizer: appends the object block + footprint, replaces an existing block keeping its comment, and **fails loud leaving the file untouched** on a sizer error or an empty digest. Pure `replaceImagesBlock`: trailing-newline preserved, append-when-absent, idempotent. `renderImagesBlock` sorts; `repoOf` table; resolve dispatch usage errors.
- `dockerSizer` end-to-end: verified by hand resolving the three committed samples (the resolved sizes/digests above).

## What's next

- **#70 — brain serves the footprint:** parse the object form in `internal/catalog`, add the coarse `footprint` to the browse `Entry`, and the incremental (already-pulled-aware) footprint + `free_bytes` to the install-plan response.
- **#71 — UI:** size on the store card, Storage block in the consent dialog.
- **Cross-image layer dedup** for `disk_bytes` once a multi-image app with shared bases enters the catalog.
- **Catalog-repo CI** that runs `molma manifest lint` + `molma manifest resolve` over the tree and regenerates `catalog.json` (`APP_STORE.md` # CI on the repo); a daemon-free sizer fits there.
