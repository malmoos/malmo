# Next

Prioritized open design topics. The only place open items live — never add them to individual docs.

## `malmo resolve`: daemon-free registry sizer for `disk_bytes`

PR #120 (fix/117) fixed the containerd-store bug by streaming `docker save` and decompressing layer blobs locally. It works and is store-agnostic, but it is slow — a multi-GB image like open-webui takes ~a minute to stream and decompress just to count bytes.

The clean path: fetch the image manifest directly from the registry (no local pull, no `docker save`), walk the layer descriptors, fetch each compressed blob, decompress on the fly, and sum the bytes. Same decompressed-tar number, no Docker daemon required, and it is the natural home for catalog CI where no daemon is available anyway. The registry client work was already scoped in [catalog-image-footprint.md](docs/progress/catalog-image-footprint.md) — this is the next concrete step on that path.

## Admin-port isolation: Caddy connects outward to per-app networks ([#187](https://github.com/malmoos/malmo/issues/187))

App `main_service` containers join `malmo-ingress` (`internal/lifecycle/lifecycle.go`), the network that also carries Caddy's unauthenticated admin API (`0.0.0.0:2019`). `CONTROL_PLANE.md` # Locked: Caddy is malmo substrate makes this a hard invariant violation: a compromised app could rewrite the entire Caddy route table via `malmo-caddy:2019`. Harmless in dev, but M1b (#165) makes it production-real.

There is no small fix — Caddy's admin must bind `0.0.0.0:2019` (the brain is a separate container), so it is exposed on every network Caddy joins. The spec-prescribed model is the fix: apps stay **off** `malmo-ingress`, and the brain connects `malmo-caddy` **outward** into each `malmo-app-<id>` network (with reconnect-on-Caddy-restart in `Reconcile` and disconnect-before-teardown). Must land before any production exposure; v1 is closed-by-default / pre-production, so nothing is live in the interim. Tracked in #187.

## Public acme-dns API face for hosted boxes (cross-repo, gates #207 acceptance) — DEPLOYED

C3b (#207, [hosted-wildcard-cert.md](docs/progress/hosted-wildcard-cert.md)) configures the hosted box's Caddy to push its `_acme-challenge` TXT to an acme-dns endpoint — a box-side constant `MALMO_ACMEDNS_ENDPOINT`, default `https://auth.malmo.network`. **Deployed and confirmed (2026-06-23, `malmoos/cloud` #14):** the cloud control-plane VM now fronts acme-dns with Caddy, exposing only `/update` + `/health` over real Let's Encrypt TLS for `auth.malmo.network` (404 on `/register`, which stays loopback-only), with the authoritative `:53` face delegated and answering publicly. The default `https://auth.malmo.network` is therefore a **confirmed live endpoint**, not a chosen value — the box-side default needs no change. The one remaining piece is the joint real-issuance run: a live box actually obtaining/renewing its `*.<box-id>.malmo.network` wildcard against this endpoint, verified at cloud #6 / CL6 (not an OS-side task — the box-side wiring is complete).

## Encrypt hosted enrollment credentials at rest (box-side)

The per-box acme-dns credentials the brain ingests from the seed are persisted plaintext in `box_meta` (`store.BoxMetaEnrollment`), matching the cloud producer's MVP posture. The threat is loss of the brain's SQLite: a leaked subdomain/username/password lets an attacker renew certs for that one box, not escalate beyond it. Encrypt at rest when the box's DB lands on shared/backed-up infra. The cloud side tracks the symmetric item for its own `boxes` table (`malmoos/cloud` NEXT.md).

## Per-app disk quota for hosted tenants

`ENVIRONMENT.md` # Per-instance resource limits names a per-app **disk quota** as the third dimension the hosted control plane needs to bound a paying tenant. The memory + CPU cgroup limits landed with #211, but disk quota was deferred: the locked storage stack (`ext4` + Docker's `overlay2`, `STORAGE.md`) cannot enforce a per-container write-layer quota portably. Docker's `--storage-opt size=` works only on `xfs`-with-`pquota` or the `devicemapper`/`btrfs` drivers — none of which the appliance or the cloud image use.

The realistic path is **XFS project quotas** on the data tree, driven through the host-agent (a privileged op — set/clear a project ID + hard limit on an app's `/var/lib/malmo/instances/<id>/` subtree and its bound use-case folders). It needs: a `BRAIN_HOST_PROTOCOL.md` verb, a `host-agent-real` implementation, and the same store-backed-policy + reconcile seam the cgroup limits already use (`internal/store` `instance_resource_limits` would gain a `disk_bytes` column). Hosted-only in practice; out of scope until the cloud image's storage layout is fixed. Deferred from #211; tracked in #221.
