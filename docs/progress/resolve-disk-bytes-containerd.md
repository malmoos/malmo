# malmo resolve: store-agnostic disk_bytes (containerd image store fix)

- **Status:** done
- **Date:** 2026-06-10
- **Specs touched:** No design change — `APP_STORE.md` # Catalog schema already defines `disk_bytes` as the sum of the image's uncompressed layer sizes; this fixes the resolver to actually compute that on every Docker store.

Closes issue #117, the review flag from #113. `dockerSizer` (`cmd/malmo/resolve.go`, built in [catalog-image-footprint.md](catalog-image-footprint.md)) derived `disk_bytes` from `docker image inspect --format '{{.Size}}'`. Under the classic overlay2 graph driver that is the unpacked layer total — the number the install-plan disk-space math wants. Under the **containerd image store** (`io.containerd.snapshotter.v1`), `.Size` reports the *compressed content size* instead, so `disk_bytes` came out ≈ `download_bytes` and the install plan undercounted the real footprint 2–4×. Kimai (#113) and Ghost (#112) caught it at review with hand-corrected values; memos and open-webui were already merged with the bad numbers.

## What was done

### Store-agnostic sizer (`cmd/malmo/savesize.go`)

`disk_bytes` is now computed by streaming `docker save <image>` and **decompress-counting the layer blobs** — the save stream carries the original blobs on both stores (gzip registry blobs under containerd, already-unpacked `layer.tar` files under the graph drivers), so the decompressed sum is the same number everywhere:

- `unpackedDiskBytes` runs `docker save` with a streamed stdout pipe (no temp file — open-webui's save is multi-GB) and hands the tar stream to the pure parser.
- `sumUnpackedLayers` walks the stream once: the save's own `manifest.json` names which entries are layers (both save formats write it — the legacy layout points at `<id>/layer.tar`, the containerd OCI layout at `blobs/sha256/<digest>`), everything else (config, `index.json`, `repositories`) is ignored. Each layer is counted through a gzip reader when its magic bytes say so, raw otherwise. **Exactly one image entry is required** — a stream describing several (a multi-platform copy of the tag in the local store) fails loudly rather than over-counting, keeping #69's "never write a bogus number" posture.
- zstd-compressed layers are recognized by magic and rejected loudly (stdlib has no zstd reader; no curated image ships zstd layers — the dependency isn't worth it until one does). A zstd blob that isn't a layer is ignored, not fatal.
- `dockerSizer.Size` keeps its shape: pull `--platform linux/amd64` → `unpackedDiskBytes` (replacing the `.Size` inspect) → index digest → registry download bytes. `download_bytes` and `digest` resolution are untouched.

### Authoring-time sanity warning

`resolve` now prints a warning under any image whose `disk_bytes < 1.2× download_bytes` (`lowExpansion`) — genuine images expand 2–4× when unpacked, so a ≈1× ratio is the bug's signature (or, rarely, an image of mostly pre-compressed content). Advisory print only, never a failure; it would have caught this defect at authoring time on #113.

### Back-fill

Re-resolved with the fixed sizer on a containerd-store machine (digests and `download_bytes` unchanged — only the disk measurement moved):

- `catalog/memos`: `disk_bytes` 22,882,516 → **65,770,496** (2.9× expansion; was ≈1× download).
- `catalog/open-webui`: `disk_bytes` 1,709,363,460 → **4,846,628,352** (2.8×; the install plan had been undercounting this app by 3.1 GB).

### Verification

- Unit tests (`cmd/malmo/savesize_test.go`): synthetic save streams in both formats — containerd gzip blobs (sum must be decompressed sizes, the exact #117 confusion), classic raw `layer.tar`, stream-order independence, and the loud-error paths (no `manifest.json`, layer missing from stream, multi-image stream, zstd layer) + the zstd-non-layer ignore + a `lowExpansion` table built from #117's real Kimai numbers.
- **Cross-store check:** resolving `traefik/whoami:v1.10.3` on this containerd-store machine gives 7,006,720 vs the committed 6,581,646 resolved on an overlay2 machine — ~6% apart (tar-stream headers vs the graph driver's content-sum; see gaps), versus the old code which would have recorded ≈2,850,040 (the compressed size). The bug signature is gone; expansion reads 2.46×.
- Full non-PAM Go suite + gofmt + vet green.

## How it maps to the specs

- `APP_STORE.md` # Catalog schema — `disk_bytes` = "sum of its uncompressed layer sizes": now computed literally (decompressed layer tar bytes) instead of trusting a store-dependent inspect field. # Trust model — sizes stay display-only; `digest` resolution is untouched and still the only thing that gates the pull.
- `DECISIONS.md` "Image size is CI-derived, not author-declared" — unchanged; this fixes the derivation, not the model.

## Known gaps & deviations

- **Definition is now the uncompressed tar-stream size,** which runs a few percent above a graph driver's content-sum (tar headers + 512-byte padding; the whoami delta is ~6%). This matches both the spec's wording and how #117 measured "real". Entries resolved pre-fix on overlay2 machines (whoami, files-demo, calibre-web, docuseal, mealie, hermes-agent, jotty) are left as-is — the drift is cosmetic for an advisory display number, and re-resolving them would churn every manifest for no user-visible change.
- **Cross-image layer deduplication is not done.** `APP_STORE.md` # Catalog schema calls for deduping layers shared within the app's own image set; `dockerSizer.Size` is called per image, so a base layer shared by two images in the same app counts twice. This predates this PR (the old `inspect` path had the same gap) and is cosmetic for display-only advisory numbers, but it means multi-image apps over-report `disk_bytes` by the size of shared layers.
- **zstd layers are a loud error,** not supported. First curated image that ships them forces the call: add a zstd dependency or keep rejecting.
- **A multi-platform copy of the tag in the local store fails the resolve** (the save stream then describes several images). The resolver itself only pulls amd64, so this only bites an author whose daemon already holds another platform of the same tag; the error says so, and removing the other copy is the remedy.
- **Kimai (#113, open) keeps its hand-corrected 887,843,328** — measured by the same decompressed-tar method this implements, so re-resolving would reproduce it; no action needed there.
- **Resolve is slower:** a full save + decompress pass (~seconds for small images, ~a minute for multi-GB ones). Authoring-time cost only; nothing at install time changed.

## What's next

- Nothing new. The daemon-free registry sizer for a catalog-repo CI ([catalog-image-footprint.md](catalog-image-footprint.md) "What's next") remains the eventual home for this computation — a registry client reads the same blobs without a local pull.
