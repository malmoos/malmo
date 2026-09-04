# Pin the third-party control-plane images by digest

- **Status:** done
- **Date:** 2026-09-04
- **Specs touched:** docs/specs/BUILD.md

Closes [#432](https://github.com/malmoos/malmo/issues/432). The two third-party control-plane images were named by mutable tag (`caddy:2-alpine`, `tecnativa/docker-socket-proxy:v0.4.2`) and pulled at image-build time, and the hosted Caddy build (`dev/control-plane/caddy-acmedns/`) took its two upstream bases by tag too. So two builds of the same malmo commit could contain different bytes, with nothing recording which.

## Step 1 — what the check found

The issue asked to confirm its claims against the tree first. All of them hold, and one is worth stating precisely:

- Both refs are still tag-only in the `Makefile` (`CADDY_IMAGE`, `PROXY_IMAGE`), and nothing else pins them.
- The acmedns Dockerfile had the same gap through `ARG CADDY_VERSION=2.10.0` → `caddy:2.10.0-builder` + `caddy:2.10.0-alpine`. It is built by `dev/cloud/stage-control-plane.sh` (not `dev/cloud/test/bootstrap.sh`, which the issue named — the build moved when the staging was shared between the two cloud lanes in #242).
- **This is a build problem, not a live-box one.** `brainlaunch` only ever `docker load`s the bundled tarball when an image is absent (`internal/hostagent/brainlaunch/brainlaunch.go`); there is no pull path for either image. A box is air-gapped at first boot and never resolves these tags against a registry.

## What was done

### One pin file (`dev/control-plane/images.lock`)

Four `NAME=name:tag@sha256:...` lines, one per third-party image, with the reasoning and the bump recipe in the header. Plain `NAME=value` with no spaces, so the same file is `include`-d by the `Makefile` and `source`-d by shell. Each digest is the multi-arch **index** digest, so a pin does not assume an architecture.

Recording the digests is the file itself: it is checked in, so `git show v0.4.0:dev/control-plane/images.lock` answers "which Caddy was in v0.4.0?" from a version number alone. They are deliberately kept **out** of the release manifest, which is about the two images an update can move (`RELEASE_MANIFEST.md` # Fields); these bytes change only when someone edits this file, which is exactly what git already records.

### Pull by digest, save by tag (`Makefile`)

`control-plane-images` now pulls by digest and re-tags to the plain tag before `docker save`. The re-tag is load-bearing, not cosmetic: a box loads the tarball offline and the control-plane compose looks the image up by tag (`dev/control-plane/compose.yml`), so a tarball saved under a digest reference would leave the compose naming an image that is not there. `docker pull name:tag@sha256:…` does not create the tag by itself — it records the image under `name@sha256:…` only.

### No default for the hosted Caddy's bases

`dev/control-plane/caddy-acmedns/Dockerfile` takes both base images as build args with **no default**, and a new `make caddy-acmedns-image` target feeds them from the pin file. A default would be a second copy of the pin, free to drift, and a bare `docker build` would then quietly bake unpinned bytes; with no default it fails instead. `stage-control-plane.sh` now calls that make target rather than running `docker build` itself — it already shells out to `make control-plane-images` a few lines above, so this is the same seam. The file opens with `# check=skip=InvalidDefaultArgInFrom`, because BuildKit lints a default-less `ARG` used in `FROM` and that hint is the intended state here.

### A guard test (`internal/hostagent/controlplane/imagepins_test.go`)

Two failures the pin file can produce silently:

1. An entry loses its digest and goes back to a mutable tag. The test checks every pin against `name:tag@sha256:<64 hex>`, and that the four names the `Makefile` reads are all present.
2. A tag is bumped in the pin file but not where a box names that image **by tag**. Nothing interpolates the pin file into `compose.yml`, the two systemd drop-ins, or host-agent's `MALMO_PROXY_IMAGE` default, so those four places are asserted to carry the pinned tag. Drift there is the failure that breaks a boot: the tarball loads under one tag and the control plane asks for another.

It lives beside `compose_test.go`, which already reads the committed `dev/control-plane/` files for the same reason.

## How it maps to the specs

`BUILD.md` gains **# 5c Third-party image pins** — what is pinned, why a build-time pull still needs a digest, how the save-by-tag step works, where the digests are recorded, and the two-line bump recipe (with upstream Caddy security releases named as the case that matters). A locked-decisions bullet points at it. The reasoning is not new: it is `APP_LIFECYCLE.md` # Locked: image digest pinning applied to the box's own TLS terminator and to the container that holds its Docker socket.

## Verified

- `make caddy-acmedns-image` builds against the pinned bases, and `caddy list-modules` in the result still lists `dns.providers.acmedns` — the build-arg rewrite did not lose the module.
- Pull → re-tag, twice, with `docker rmi` + `docker image prune -f` in between: identical image IDs both times (`caddy:2-alpine` → `af555904…`, the socket proxy → `16bbd120…`). The saved `caddy.tar` carries `RepoTags: ["caddy:2-alpine"]`, which is what the box looks up.
- The guard test fails as intended when a pin is edited down to a bare tag, and when a pinned tag stops matching `compose.yml`.
- The Go suite is green (`make test-nopam`, plus `make fmt-check` and `make openapi-check`). Full `make check` does not complete on this machine for two reasons that predate this branch and reproduce on a clean `dev`: `go vet` on the PAM binding (`C.RTLD_NEXT`) and gitignored `dev/cloud/mkosi.tools/` build residue that `./...` walks into. CI runs the full gate.

Not exercised: a full VM boot. Nothing about what a box does changed — the bundle holds the same four images under the same four tags — so the medium and cloud lanes have nothing new to prove, and no image canary is bumped.

## Known gaps & deviations

- **The malmo-built images still ride mutable bases.** `cmd/brain/Dockerfile` (`golang:1.25-bookworm`, `debian:trixie-slim`) and `web-ui/Dockerfile` (`node:20-alpine`, `caddy:2-alpine`) are not pinned here. That is deliberately out of this issue's scope, and pinning them alone would not buy a reproducible image anyway: both run `apt-get`/`npm ci` against live indexes. Left for its own issue rather than half-done here — see "What's next".
- **`dev/docker-compose.yml` (the inner-loop dev Caddy) is still on the bare `caddy:2-alpine` tag.** It is a developer-machine container that ships to nobody, and pinning it would mean a second consumer of the pin file with no supply-chain gain.
- **The xcaddy build is still not reproducible**, pinned bases or not: `xcaddy build --with github.com/caddy-dns/acmedns` resolves the module at build time with no version constraint. The pin fixes the bases, which is what the issue asked for; the module version is a separate question.
- The pinned `caddy:2-alpine` currently resolves to Caddy 2.11.4 while the acmedns build stays on 2.10.0. That split predates this change (the Dockerfile's header explains the Go-toolchain reason) and, per the issue, "the image is old" is not a finding here.

## What's next

- Decide whether the acmedns build should pin the `caddy-dns/acmedns` module version as well, so the hosted Caddy binary is reproducible and not only its bases.
- Consider pinning the base images of `malmo-brain` and `malmo-ui`, together with whatever else the two Dockerfiles would need to make a rebuild meaningful.
