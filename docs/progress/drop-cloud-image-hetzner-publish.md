# CI / Cloud image publishes only the GitHub Release asset (Hetzner publish removed)

- **Status:** done
- **Date:** 2026-07-21
- **Specs touched:** none (the release-asset behaviour was already as-built in `release-cloud-image-asset.md`)

## What was done

`CI / Cloud image` (`.github/workflows/ci-cloud-image.yml`) briefly did two publishes on every release: it uploaded the built image to a hosting provider as a snapshot AND attached the same image to the tagged GitHub Release (`release-cloud-image-asset.md`). Running both meant every release produced two artifacts for the same build — a duplicate that served no purpose once the downstream consumer of the Release asset owns the provider upload.

Removed the provider-upload half so a publish emits exactly one artifact:

1. Deleted three steps — `Set up Go (for hcloud-upload-image)`, `Install hcloud-upload-image`, and `Publish image to Hetzner + resolve snapshot id`. The last held the only provider credential the workflow read.
2. The surviving `Attach image + checksum to the GitHub Release` step previously borrowed the `.raw` discovery and `xz` compression from the deleted publish step (via its `raw_xz` output). It now does that itself: finds the newest `.dev/cloud/*.raw`, compresses it once with `xz -T0`, and attaches `malmo-vX.Y.Z-amd64.raw.xz` plus its `.sha256` sidecar. The `SHOULD_PUBLISH` gate, tag resolution, clobber-guard, and create-if-missing logic are unchanged.
3. Dropped the now-dangling `image_id` job output (its `steps.publish` source is gone) and updated the header and inline comments that described the provider-upload path — including the intro, the `publish` input descriptions, and the required-secret note — so the file no longer names a hosting provider or claims to hold provider credentials. The workflow now runs on the job's own `github.token` alone.

The build, the seeded-boot gate (`unseeded seeded bios access`), the lean check, and the `contents: write` grant are untouched. Verified via YAML parse; a real tagged release is the end-to-end proof.
