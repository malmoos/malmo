# malmo-brain runtime base: `debian:trixie-slim` with the docker CLI bundled, not distroless

- **Status:** done
- **Date:** 2026-06-13
- **Specs touched:** `BUILD.md` (# 5 build line + locked build summary), `DECISIONS.md` 2026-06-13

Closes #162, a `[decision]` issue gating #163/M0: the brain image base couldn't be chosen — and M0 couldn't start — while `BUILD.md` # 5 leaned a **distroless** runtime that the brain's actual Docker integration cannot run on. The brain orchestrates apps by shelling out to the `docker` / `docker compose` CLI (~15 call sites in `internal/lifecycle/docker.go` — `exec.CommandContext(ctx, "docker", …)`), and a distroless image — no shell, no binaries — has nothing to exec. This entry records the resolution; it is docs-only (no brain code moves).

## What was decided

**Runtime stage is `debian:trixie-slim` with the `docker` CLI + Compose plugin bundled** (`docker-ce-cli` + `docker-compose-plugin` from Docker's official apt repo — the same trusted source as the host engine, per `BUILD.md`'s Docker-package-source decision). The multi-stage build is unchanged: the build stage still compiles the Go binary and the runtime stage takes only the binary plus the CLI, so the Go toolchain never ships.

Considered and rejected:

- **(b) keep distroless, rewrite the shell-outs onto the Docker Go SDK.** `docker compose up` has no SDK equivalent, so this means vendoring `compose-go` and reimplementing the multi-service orchestration (ordering, env-files, overrides, networks) the project deliberately delegates to the CLI — a large refactor that busts the size:S budget and walks away from the compose-CLI architecture the whole codebase + test suite is built on.
- **(c) keep distroless, bind-mount the host's CLI into the container.** Fragile: distroless lacks the loader/libs the CLI needs, and the brain image would couple to the host's Docker version (glibc / version skew).

Why the distroless wins don't pay for either: the **size** win is immaterial — multi-stage already keeps the toolchain out (~30 MB brain binary), and the bundled CLI is a ~170 MB runtime dependency multi-stage can't trim (image ~200 MB), which is noise against the multi-GB app images the box pulls. The **attack-surface** win is marginal for a daemon that already holds Docker API access via the socket proxy (`CONTROL_PLANE.md` # Locked: Docker socket exposure mitigated by socket proxy), and slim stays debuggable (it has a shell).

Orthogonal to the socket-proxy decision: the bundled CLI reaches the daemon through the same proxy via `DOCKER_HOST`, so the endpoint allowlist still governs what the brain can do.

## What was done

- `BUILD.md` # 5 build line: flipped the runtime stage from "`gcr.io/distroless/static-debian12` or `debian:trixie-slim`, lean distroless" to **slim with the CLI + Compose plugin bundled**, naming the CLI shell-out as the reason and the apt-repo source. Dropped the now-moot "ship a debug image variant if needed" note (it existed only because distroless has no shell; slim does).
- `BUILD.md` locked build summary: the `malmo-brain` bullet's "distroless runtime" → "`debian:trixie-slim` runtime with the `docker` CLI + Compose plugin bundled," with a pointer to the `DECISIONS.md` entry.
- `DECISIONS.md`: new top entry (2026-06-13) recording the flip off the locked distroless decision — Previously / Now / Why (the three options + why size & attack-surface don't justify (b)/(c)) / Affected docs.

No code, OpenAPI, protocol, or web-ui change. `docs/README.md` already maps `BUILD.md`; no new spec file.

## What's next

- **Author the Dockerfile + build wiring** when #163/M0 lands — this issue fixed the *base decision*, not the build itself. M0 owns the actual multi-stage `Dockerfile`, the apt-pin of `docker-ce-cli` + `docker-compose-plugin`, and the OCI image build/tag/bundle plumbing `BUILD.md` # 5 # Distribution describes.
- **Pin exact CLI + Compose plugin versions** at Dockerfile-authoring time (reproducible builds), against the same Docker apt repo the host uses, so the brain's CLI and the host engine don't skew.
