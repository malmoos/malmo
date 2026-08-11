# Publish the brain + UI images to ghcr on release

- **Status:** done
- **Date:** 2026-08-11
- **Specs touched:** docs/specs/BUILD.md, docs/specs/UPDATES.md

First implementation slice of the update work whose design landed in [#369](https://github.com/malmoos/malmo/pull/369) (`UPDATES.md` # 8, `DECISIONS.md` 2026-08-11). Closes #370.

## What was done

Stream B — the containers — had nowhere to pull from. `malmo-brain` and `malmo-ui` existed only as `docker save` tarballs baked inside the disk image, so a running box could not obtain a newer control plane at all: the only path to new malmo was reprovisioning from a fresh image.

`.github/workflows/ci-cloud-image.yml` now pushes both images to ghcr on release:

- **Gated on the existing `SHOULD_PUBLISH`**, so it fires only on a real release (a `v*` tag push, or a `workflow_dispatch`/`workflow_call` with `publish: true`) — never on an ordinary CI run.
- **Placed after the seeded-boot proof**, so the only control plane we can ever publish is one that was proven to boot.
- **Placed last, after the Release-asset attach.** Every step in this job is a hard sequential gate, and the disk image on the Release is the artifact that actually gets consumed today — these ghcr images have no consumer until the updater lands. Pushing first would let a transient registry failure leave a release with a tag, a Release object, and no downloadable image, which is worse than the state before this step existed.
- **Needs `packages: write` in two places.** `ci-cloud-image.yml` requests it, but a called reusable workflow can only *narrow* its caller's permissions, never widen them. `release.yml` — the only path a real release takes — grants `contents: write` alone at the workflow level, which sets `packages` to `none` for every job it owns. So the `cloud-image` job in `release.yml` carries its own `contents: write` + `packages: write` grant. Without it the push 403s on every real release while a hand-run `workflow_dispatch` against `ci-cloud-image.yml` succeeds, because that run has no caller narrowing it.
- **Reuses the local images the build already produced** (`malmo-brain:dev`, `malmo-ui:dev` from `make control-plane-images`). It tags and pushes those exact images rather than rebuilding, so what lands in the registry is byte-identical to what was baked into the disk image that just booted.
- Tags `vX.Y.Z` (from `VERSION`) and `latest`, per `BUILD.md` # 6.
- **Carries the same tag-points-at-another-commit guard** the Release-asset step uses. `SHOULD_PUBLISH` is also true for a manual `workflow_dispatch`, which can run against any commit while `VERSION` still names an already-shipped release; without the guard that would overwrite a released version's images with ones built from another source.
- Writes the pushed digests to the job summary. Boxes pull by digest (`BUILD.md` # 6), so the digest is the number that matters, not the tag.
- Needs `packages: write`, which the job's own `GITHUB_TOKEN` satisfies. The lane still holds no long-lived registry credential.

`BUILD.md` # 6's as-built bullet claimed the Release asset was "the **only** published image artifact." That is now false, and it is corrected: the Release asset is the only published *disk-image* artifact, and the same lane also publishes the two control-plane images.

## How it maps to the specs

- `UPDATES.md` # 8 — stream B needs a registry to pull from. This is that registry.
- `BUILD.md` # 6 — realizes the per-release control-plane image artifacts (`registry.malmo.network/malmo/brain:vX.Y.Z` in the spec's naming; `ghcr.io/malmoos/brain` is the first concrete realization of that name, which the spec already says can be repointed later). Also realizes the "published publicly, pulled by digest" decision recorded in that section.

## Known gaps & deviations

- **The packages are private until someone flips them.** ghcr creates a package private on first push, and changing it to public is a one-time manual change in the package settings needing org/repo admin. The workflow cannot do it. **Until that flip, no box can pull anonymously** and the "public images" decision in `BUILD.md` # 6 is written down but not in effect.
- **Untested against the real registry.** The step's logic was exercised against stubbed `docker` and `curl` across three cases — tag absent (pushes both images, digests captured), tag pointing at a different commit (skips cleanly, exits 0, nothing pushed), tag pointing at this commit (pushes, so a failed publish is retryable). A real push has not happened, because the trigger is a release and this branch is not one. The first real release is the acceptance.
- **The first push is a package-*creation* event, not just a push.** Some orgs restrict who may create a new package even when the token carries `packages: write`. Both package names are new, so if `malmoos` has such a policy the first release fails here. Worth checking the org's package settings before cutting the next release rather than discovering it mid-release.
- **The permissions bug above was found in review, not in testing**, and it is the shape of bug this slice cannot test for: the broken path (`workflow_call` from `release.yml`) is the one that only ever runs on a real release, while the path a person would naturally test by hand (`workflow_dispatch` directly against `ci-cloud-image.yml`) works. Worth remembering when the updater slice adds more release-path-only behavior.
- **Nothing consumes these images yet.** The updater that pulls them does not exist. This slice only makes the artifact available.
- **`latest` is pushed but has no defined consumer.** `BUILD.md` # 6 says it advances on the stable channel, so it is pushed for consistency with the spec. Boxes will pull by digest, so nothing should ever resolve `latest` in production.
- **Multi-arch is not addressed.** amd64 only, matching the BYO-x86 scope in `SPEC.md`.

## What's next

1. **Flip the two ghcr packages to public** (manual, needs admin) — at `https://github.com/orgs/malmoos/packages` → the package → Package settings → Danger Zone → Change visibility. The packages do not exist until the first release pushes them, so this cannot be done in advance. Verify with a logged-out `docker pull`, which is the only real proof anonymous pull works. Until then the artifact exists but is unreachable.
2. **Expose the running versions over the brain's API.** `internal/version` already holds `Version` and `Commit`, stamped at build time, and nothing serves them. Small and independent of the box↔cloud credential.
3. **The updater** — pull by digest, snapshot the brain's SQLite, write the staged control-plane compose, recreate, health-check, revert both on failure. This is the expensive half and it is shared by both profiles, so it can be built and tested with a local target before any cloud trigger exists.
4. **Box → cloud version report and trigger.** Blocked on the box↔cloud credential design (`NEXT.md` # Box ↔ cloud API authentication, Tier 1).
