# M0 — build the malmo-brain + malmo-ui images and bake the control-plane bundle into the QEMU medium lane

- **Status:** done
- **Date:** 2026-06-13
- **Specs touched:** `CONTROL_PLANE.md` # Locked: Caddy is malmo substrate (admin-port bind + network-isolation invariant clarified). Otherwise realizes `BUILD.md` # 5 / # 5b, `WEB_UI.md` # deploy model, `TESTING.md` # Full-stack control-plane integration with no spec change.

Closes #163 (M0, part of #161), picking up the "author the Dockerfile + build wiring" next-step of [brain-image-base-slim.md](brain-image-base-slim.md) (#162, which fixed the *base decision*). M0 produces the two control-plane OCI images and the control-plane compose, and delivers them — plus the third-party control-plane images — into the medium-lane test image via the `docker save` / first-boot `docker load` bundle mechanism `TESTING.md` specifies. **No launch wiring**: the brain does not yet start the stack (that is M1a/M1b); M0's bar is buildable, loadable artifacts and a VM that runs `docker.service` with the images present.

## What was done

**The two malmo images (natively built + verified):**

- `cmd/brain/Dockerfile` (context = repo root) — multi-stage. Build stage `golang:1.25-bookworm` compiles the brain with `CGO_ENABLED=0` (the brain's only sqlite is `modernc.org/sqlite`, pure Go, so nothing links libc); runtime stage `debian:trixie-slim` installs `docker-ce-cli` + `docker-compose-plugin` from Docker's official apt repo (codename read from `/etc/os-release`), per the #162 decision. Built image **256 MB**, with `docker --version` (29.5.3) and `docker compose version` both resolving inside it and the brain binary running.
- `web-ui/Dockerfile` (context = `web-ui/`) + `web-ui/Caddyfile` — multi-stage. Build stage `node:20-alpine` runs `npm ci && npx vite build`; runtime stage `caddy:2-alpine` serves the bundle from `/srv/ui` with the SPA Caddyfile (fallback to `index.html`, `encode gzip zstd`, ETag-on). `vite build` directly, **not** `npm run build` — the image bakes the bundle; type-checking (`vue-tsc`) is a separate CI gate, not part of producing the artifact. Built image **90 MB**.
- `.dockerignore` (repo root) + `web-ui/.dockerignore` — keep the build contexts lean and, for the brain, structurally exclude `.dev/` so dev state (which can hold instance `.env` secrets) never enters the build.

**The control-plane compose (authored, consumed in M1b):**

- `dev/control-plane/compose.yml` — the three containers the brain will bring up: `caddy` (`malmo-caddy`, publishes 80/443, admin driven over the malmo network — admin port deliberately not host-exposed), `malmo-ui` (read-only, no ports), and `docker-proxy` (`tecnativa/docker-socket-proxy`, `/var/run/docker.sock:ro`, endpoint allowlist `CONTAINERS/IMAGES/NETWORKS/VOLUMES/INFO/VERSION/PING` + `POST`; EXEC and host-bind mounts left denied per `CONTROL_PLANE.md` # Docker socket exposure). Attaches to the brain-owned external `malmo-ingress` network.
- `dev/control-plane/caddy.json` — minimal bootstrap config that opens the admin endpoint on `0.0.0.0:2019` and installs the catch-all 404; everything else the brain adds live via the admin API (same pattern as the dev Caddy).

**Build + bundle plumbing (natively verified):**

- `Makefile`: `brain-image`, `ui-image`, and `control-plane-images` — the last builds both malmo images and `docker save`s all four control-plane images (the two built + `caddy:2-alpine` + `tecnativa/docker-socket-proxy`, both pulled first) into `.dev/control-plane/*.tar`. The third-party images must be in the bundle because the test VM has no network. `make control-plane-images` needs only Docker (the images build hermetically). Verified: produces a 127 MB four-tarball bundle.

**Bake Docker + the bundle into the medium-lane VM (authored to spec; see caveat):**

- `dev/test-qemu/mkosi.conf` — added `docker-ce`/`docker-ce-cli`/`containerd.io`/`docker-compose-plugin` to `Packages=` and `PackageManagerTrees=mkosi.pkgmngr` so Docker's apt repo is available to the build's package manager.
- `dev/test-qemu/bootstrap.sh` — new section 4b: runs `make control-plane-images`, stages the tarballs into the image at `/var/lib/malmo/control-plane-images/`, stages the first-boot loader, writes the `docker.service` storage-ordering drop-in, and populates `mkosi.pkgmngr/` with Docker's repo + fetched signing key (bookworm pocket — the image is `Release=bookworm`). `curl`/`docker` added to the host preflight; `CANARY_VERSION` bumped `v17`→`v18` (a clean rebuild is required for the new disk size + packages).
- `dev/test-qemu/malmo-load-images.service` + `dev/test-qemu/load-control-plane-images.sh` — a run-once first-boot oneshot (`After=docker.service`, gated on a marker) that `docker load`s every bundled tarball.
- `dev/test-qemu/mkosi.postinst.chroot` — enables `docker.service`/`containerd.service`/`malmo-load-images.service` (manual `.wants` symlinks + preset entries, the established chroot idiom).
- `dev/test-qemu/mkosi.repart/10-root.conf` — replaced `Minimize=guess` with `SizeMinBytes=8G`: the bundle (~130 MB) bakes under `/var/lib/malmo` and the first-boot `docker load` expands it into `/var/lib/docker`, so the runtime needs headroom Minimize would not leave (sparse — the raw image only consumes blocks actually written; also leaves room for M2's whoami).
- `.gitignore` — ignore the bootstrap-populated `dev/test-qemu/mkosi.pkgmngr/`.

## Verification

- **Natively verified (the buildable/loadable half):** `make control-plane-images` builds both malmo images and saves the four-tarball bundle; the brain image carries a working `docker` + `docker compose`; the ui image serves the SPA bundle; the control-plane compose and both Caddy configs validate; `make check-web`'s production build is green on top of #170. All three shell scripts pass `bash -n`.
- **Not verified here (the VM half):** the "Done when" boot criterion — a booted medium-lane VM runs `docker.service` and shows all four images in `docker images` after first boot — needs `sudo` + `mkosi` + `swtpm` + QEMU, which this environment can't run. The mkosi/bootstrap/repart/first-boot wiring is authored to spec but **not boot-verified**; it must be confirmed with `sudo make test-medium-qemu` on a Linux box with the outer-loop toolchain before the acceptance criterion is considered met.

## What's next

- **Boot-verify the medium-lane image** (`sudo make test-medium-qemu` on a host with mkosi/swtpm/QEMU): confirm `docker.service` is active and `docker images` lists `malmo-brain`, `malmo-ui`, `caddy`, and `docker-socket-proxy` after first boot. The in-VM assertion is intentionally not added here — `dev/test-qemu/medium-assertions.sh` is out of M0's touch scope; the automated control-plane assertions belong to the full-stack integration lane.
- **M1a/M1b — launch wiring:** `host-agent-real` launches the brain container; the brain brings up `dev/control-plane/compose.yml` (Caddy + malmo-ui + socket-proxy). The compose authored here is M1b's input.
  - **Known gap / M1b constraint — Caddy admin-port network isolation.** `caddy.json` binds the admin API to `0.0.0.0:2019` (it must, since the brain reaches it cross-container — Caddy's loopback isn't an option), and the admin API has no auth. The port is not host-published, so its only protection is network trust. M1b's network model **must keep app containers off whatever network carries `:2019`** — Caddy reaches app upstreams by joining each app's own per-app network, never by letting apps onto the control-plane/ingress network the admin port lives on. An app sharing that network could rewrite the entire route table. Spec invariant added to `CONTROL_PLANE.md` # Locked: Caddy is malmo substrate.
- **Pin exact image tags/digests** for the bundle (reproducible builds): `tecnativa/docker-socket-proxy` is currently `:latest`, and the malmo images are `:dev`. Release tagging (`vX.Y.Z`) and digest-pinning land with the distribution/release-manifest work (`BUILD.md` # 6, `RELEASE_MANIFEST.md`).
- **M2** bundles the `whoami` app image alongside these and adds the full-stack assertions.
