# Next

Prioritized open design topics. The only place open items live — never add them to individual docs.

## `malmo resolve`: daemon-free registry sizer for `disk_bytes`

PR #120 (fix/117) fixed the containerd-store bug by streaming `docker save` and decompressing layer blobs locally. It works and is store-agnostic, but it is slow — a multi-GB image like open-webui takes ~a minute to stream and decompress just to count bytes.

The clean path: fetch the image manifest directly from the registry (no local pull, no `docker save`), walk the layer descriptors, fetch each compressed blob, decompress on the fly, and sum the bytes. Same decompressed-tar number, no Docker daemon required, and it is the natural home for catalog CI where no daemon is available anyway. The registry client work was already scoped in [catalog-image-footprint.md](docs/progress/catalog-image-footprint.md) — this is the next concrete step on that path.

## Admin-port isolation: Caddy connects outward to per-app networks ([#187](https://github.com/malmoos/malmo/issues/187))

App `main_service` containers join `malmo-ingress` (`internal/lifecycle/lifecycle.go`), the network that also carries Caddy's unauthenticated admin API (`0.0.0.0:2019`). `CONTROL_PLANE.md` # Locked: Caddy is malmo substrate makes this a hard invariant violation: a compromised app could rewrite the entire Caddy route table via `malmo-caddy:2019`. Harmless in dev, but M1b (#165) makes it production-real.

There is no small fix — Caddy's admin must bind `0.0.0.0:2019` (the brain is a separate container), so it is exposed on every network Caddy joins. The spec-prescribed model is the fix: apps stay **off** `malmo-ingress`, and the brain connects `malmo-caddy` **outward** into each `malmo-app-<id>` network (with reconnect-on-Caddy-restart in `Reconcile` and disconnect-before-teardown). Must land before any production exposure; v1 is closed-by-default / pre-production, so nothing is live in the interim. Tracked in #187.
