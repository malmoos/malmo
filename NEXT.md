# Next

Prioritized open design topics. The only place open items live — never add them to individual docs.

## `molma resolve`: daemon-free registry sizer for `disk_bytes`

PR #120 (fix/117) fixed the containerd-store bug by streaming `docker save` and decompressing layer blobs locally. It works and is store-agnostic, but it is slow — a multi-GB image like open-webui takes ~a minute to stream and decompress just to count bytes.

The clean path: fetch the image manifest directly from the registry (no local pull, no `docker save`), walk the layer descriptors, fetch each compressed blob, decompress on the fly, and sum the bytes. Same decompressed-tar number, no Docker daemon required, and it is the natural home for catalog CI where no daemon is available anyway. The registry client work was already scoped in [catalog-image-footprint.md](docs/progress/catalog-image-footprint.md) — this is the next concrete step on that path.
