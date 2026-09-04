# Pin the build inputs the first pass left out

- **Status:** done
- **Date:** 2026-09-04
- **Specs touched:** docs/specs/BUILD.md

Follows [third-party-image-pins.md](third-party-image-pins.md), which pinned the four upstream images the control plane ships and listed two gaps in its own "What's next". This closes both. No issue — the maintainer asked for it directly off that entry.

## What was still floating

- **The hosted Caddy's plugin.** `xcaddy build --with github.com/caddy-dns/acmedns` names no version, so xcaddy took whatever was latest on the day of the build. Both base images were pinned, so this was the one thing that could still change the binary under a frozen recipe. Caddy's own version was never the problem: it comes from the pinned `caddy:2.10.0-builder`, and the shipped binary reports `v2.10.0`.
- **The bases under our own two images.** `cmd/brain/Dockerfile` (`golang:1.25-bookworm`, `debian:trixie-slim`) and `web-ui/Dockerfile` (`node:20-alpine`, `caddy:2-alpine`) named all four by mutable tag.

The plugin version pinned here, `v0.7.0`, was **read out of the binary** (`caddy build-info` on the image the recipe builds), not taken from upstream's releases page. Pinning to whatever is newest would have been a silent version bump wearing the clothes of a pin.

## What was done

### Four more entries in `dev/control-plane/images.lock`

`BRAIN_BUILDER_IMAGE`, `BRAIN_RUNTIME_IMAGE`, `UI_BUILDER_IMAGE` by digest, and `CADDY_ACMEDNS_MODULE=github.com/caddy-dns/acmedns@v0.7.0` by version. The file's header now says it holds build inputs rather than only images, since a Go module is not one.

`malmo-ui`'s runtime base is **not** a new entry: it takes `CADDY_IMAGE`, the same pin the proxy runs. One Caddy for both, not two pins to keep level — and the built image confirms it, reporting `v2.11.4`, which is what the pinned `caddy:2-alpine` currently is.

### Both malmo Dockerfiles take their bases as build args

Same rule the acmedns recipe already followed: `ARG` with **no default**, fed by `make brain-image` / `make ui-image`. A default would be a second copy of a pin, free to drift, and a bare `docker build` would then bake unpinned bytes. Both files gain the `# check=skip=InvalidDefaultArgInFrom` directive, because BuildKit lints exactly the state we want. Nothing else in the tree builds these two Dockerfiles — every path goes through `make control-plane-images` — so there is one caller to keep correct.

### Two more guards in `imagepins_test.go`

- `CADDY_ACMEDNS_MODULE` must be `module@vX.Y.Z`.
- **No `FROM` in the three Dockerfiles may name a base directly.** That is the regression this change removes, and it is invisible in a green build — the image simply holds different bytes. The test walks an explicit list of three files rather than the tree, because the working tree also collects gitignored mkosi build residue with Dockerfiles in it.

## How it maps to the specs

`BUILD.md` # 5c is widened from "third-party images" to "third-party build inputs" and gains two bullets: the bases-as-build-args rule, and the module pin. It says plainly that this is **pinning, not reproducibility** — the brain's runtime stage still `apt-get`s `docker-ce-cli` from a live index, so two builds of one commit can still differ. Pinning fixes the base bytes and records them, which is the question a supply-chain audit actually asks; claiming a reproducible build here would be false.

## Verified

- `make caddy-acmedns-image` rebuilds and the result is unchanged where it should be: `caddy version` → `v2.10.0`, `build-info` → `caddy-dns/acmedns v0.7.0`, `list-modules` → `dns.providers.acmedns`.
- `make brain-image` builds and the binary still stamps its identity (`malmo 0.10.0 (gc79e058)`), so the ARG rewrite did not disturb the `-ldflags` path. `make ui-image` builds; its runtime Caddy is `v2.11.4`, the pinned `caddy:2-alpine`.
- Both new guards fail when mutated: an unversioned module pin, and a `FROM caddy:2-alpine` put back into `web-ui/Dockerfile`.
- Go suite green (`make test-nopam`, `fmt-check`, `openapi-check`). Full `make check` still does not complete locally for the two reasons recorded in the previous entry; CI runs the full gate.

## Known gaps & deviations

- **`dev/docker-compose.yml` is still on the bare `caddy:2-alpine` tag.** Unchanged from the previous entry: it is the inner-loop dev proxy on a developer's machine and ships to nobody.
- **The brain image is pinned but not reproducible**, as above. Pinning the apt side would mean holding `docker-ce-cli` at a version and hand-bumping it for every security release, which trades a real security property for a build property we do not have a consumer for.
- **Transitive Go modules are not pinned individually.** `libdns/acmedns v0.5.0` comes in through the plugin's own `go.mod`, resolved by MVS from the pinned direct dependency. That is deterministic for a given plugin version, but it is not a pin of its own.

## What's next

Nothing queued. If a bump is ever needed under time pressure, `BUILD.md` # 5c holds the recipe.
