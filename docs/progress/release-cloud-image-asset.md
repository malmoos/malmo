# Release carries its own cloud-image asset (GitHub Release attachment)

- **Status:** done
- **Date:** 2026-07-21
- **Specs touched:** `docs/specs/BUILD.md` (# 6 Artifacts and channels — as-built bullet added, no locked decision changed)

## What was done

`CI / Cloud image` (`.github/workflows/ci-cloud-image.yml`) already builds the hosted cloud image, runs it through the seeded-boot gate, and publishes it to Hetzner as a snapshot on a real release. Until now that snapshot was the *only* place a built image lived — fetching it required a Hetzner account and API token, so a tagged release's source and its built artifact were not co-located, and nothing about the artifact was independently verifiable without that one provider.

Added a new step, **"Attach image + checksum to the GitHub Release"**, gated on the exact same `SHOULD_PUBLISH` condition as the existing Hetzner publish (real tag push, or a `workflow_dispatch`/`workflow_call` run with `publish: true`) — so it only ever runs on a genuine publish, never a build-only smoke run. It:

1. Reuses the `.raw.xz` the Hetzner-publish step already compressed (via a new `raw_xz` step output) instead of compressing the `.raw` a second time.
2. Names the asset `malmo-vX.Y.Z-amd64.raw.xz` following `BUILD.md` # 6's existing per-release artifact scheme, sourced from the repo-root `VERSION` file — `.raw.xz` rather than the doc's `.qcow2` bullet, since this lane's mkosi build actually emits `.raw` (`Format=disk`), not a qcow2 conversion; a checksum sidecar `malmo-vX.Y.Z-amd64.raw.xz.sha256` (`sha256sum` output) ships alongside it.
3. Resolves the target tag as `v$(cat VERSION)` — the same construction the tag-assert step and `release.yml`'s auto-tagging already use. Two integrity guards sit before the upload, because `SHOULD_PUBLISH` is also true for a manual `workflow_dispatch` that can run against any commit: (a) if the tag already points at a commit other than this build (an ad-hoc publish whose `VERSION` still matches a shipped release), the step skips rather than clobber that release's canonical image with a build from another source; (b) the create-if-missing only fires on a definitive `404` from the releases API — any other status (transient, permission) aborts loudly instead of being misread as absence and failing the create.
4. Uploads with `gh release upload --clobber`, so re-running a publish against this same commit's tag (e.g. retrying a failed run) replaces the asset instead of erroring on "already exists."

Added `permissions: contents: write` at the workflow root — the minimum grant `gh release create`/`gh release upload` need; nothing else in the workflow needs write access. No new secret: the step uses the job's own automatic token (`github.token`).

Purely additive: the existing Hetzner-snapshot publish step and its `HCLOUD_TOKEN` secret are untouched and still run — both paths ship on every real publish now, independently.

## How it maps to the specs

`BUILD.md` # 6 gained an "as-built" bullet documenting the new Release-asset path alongside the existing artifact list, without changing the qcow2/raw naming the section already locks for the planned artifact set.

## Known gaps & deviations

- The doc's `malmo-vX.Y.Z-amd64.qcow2` bullet still names the cloud-image artifact as a qcow2; the actual build (and now its Release asset) is `.raw` (`.raw.xz` compressed). This mismatch predates this change — the lane has always emitted `.raw` — and is called out inline in the new bullet rather than silently resolved, since reconciling the doc's format choice is a separate decision, not this slice's scope.
- Not verified on a real tagged release yet (the next real `VERSION` bump + dev->main merge exercises it end-to-end); verified locally only via `python3 -c 'import yaml,sys; yaml.safe_load(...)'` (parses clean) and by re-reading the step against `gh`'s documented flags.

## What's next

- Confirm on the next real release that both the Hetzner snapshot and the GitHub Release asset land, and that `--clobber` behaves as expected on a re-run.
