# Cloud QEMU lane: green end-to-end on the hosted /setup gate (CL6)

- **Status:** done — `sudo -E make test-cloud-qemu` PASSES all three scenarios (unseeded → seeded → frozen) on a clean VM
- **Date:** 2026-06-22
- **Specs touched:** none (a test-harness fix; realizes the "VM-boot acceptance pending" gap left by [hosted-caddy-image-bake.md](hosted-caddy-image-bake.md))

Closes the VM-boot acceptance gap that [hosted-caddy-image-bake.md](hosted-caddy-image-bake.md) (#236) left open ("`make test-cloud-qemu` … has not been run in this slice's environment"). Running it for the first time on top of the post-C3a/C3b/C4 stack failed on the unseeded `/setup` gate (got `422`, want `503`). Tracing it surfaced two stacked build-staleness bugs and one stale harness assumption; this slice fixes all three so the lane is a real no-regression gate for the hosted on-ramp, not a never-run one.

## Three root causes

- **Stale brain image (the visible `422`).** `dev/cloud/test/bootstrap.sh` reuses the shared `.dev/control-plane/*.tar` bundle unless `MALMO_REBUILD_CP=1`. The cached `malmo-brain.tar` predated C3a's `/setup` gate + the C1a profile resolution, so the brain booting in QEMU resolved `appliance` and `/setup` fell through to the appliance-era request-validation path (`422` on an empty/partial body), never the hosted `503`. Confirmed by running the freshly-built brain image locally with a `hosted` marker mounted → correct `503`.
- **Canary masked every rebuild.** The `.cloud-boot-ready`/`CANARY_VERSION` idempotency gate skips the *entire* bootstrap when the version matches and the image exists — so a fresh brain tarball (or any staged-tree edit) never re-entered the image. The canary keys only on its own version + image existence; it has no knowledge of the control-plane bundle changing underneath it. Bumping the canary is the documented way to force a clean rebuild after staged content changes.
- **Provisioned dashboard host moved (the follow-on `404`).** Once the fresh brain booted, the seeded/frozen scenarios failed the dashboard-SPA / `/api` checks with Caddy's catch-all `404`: after C3b (#207) a provisioned hosted box serves the dashboard, `/api`, and `/setup` under its wildcard apex `<box-id>.malmo.network`, but `cloud-assertions.sh` hardcoded `DASH_HOST=malmo.local` for all boots. Unprovisioned still uses `malmo.local` (no box-id yet), so only the post-seed boots were wrong.

## What was done

- **`dev/cloud/cloud-assertions.sh`** — resolve `DASH_HOST` per scenario just before the HTTP checks (after `json_str` is defined): `malmo.local` for `unseeded`, `<box-id>.malmo.network` for `seeded` (box-id read from the just-materialized seed) and `frozen` (box-id from the persisted identity carried in `MODE`, since the brain ignores the re-delivered seed). Added two failure-path diagnostics to `diag()` — the brain's resolved-profile / seed-ingest log lines (grep'd, not `tail`'d, so they aren't scrolled off) and the brain container's bind-mount list (to confirm `/etc/malmo/profile` is mounted) — which pinpoint this exact class of failure next time.
- **`dev/cloud/test/bootstrap.sh`** — `CANARY_VERSION` v13 → v14 to force the clean rebuild that picks up the fresh brain bundle + the assertion change.

## Verification

- `sudo -E make test-cloud-qemu` green on a clean VM: boot 1 unseeded → `/setup` `503` (gate armed); boot 2 seeded → wrong secret `401`, correct `200` + `box_id=cindy-fox`; boot 3 frozen → identity held across reboot, re-delivered seed B (`rusty-hawk`) ignored, `box_id` still `cindy-fox`. Verdict: `cloud gate proof: PASS`.
- The fresh brain image's `/setup` behavior was first reproduced locally (hosted marker → `503`, appliance → proceeds past the gate) to isolate the cause before the QEMU rebuild.

## Known gaps & deviations

- **Bundle-cache staleness is mitigated, not eliminated.** A future brain/host-agent code change still won't refresh the cached `.dev/control-plane/*.tar` locally unless the dev runs `MALMO_REBUILD_CP=1` (or deletes the bundle); the canary forces an *image* rebuild but not a *bundle* rebuild — they are orthogonal caches. CI is unaffected (no cache; builds fresh). Left as-is to preserve the deliberate ~13 GB-rebuild-avoidance for the inner loop; the failure mode is now self-diagnosing via the added `diag()` profile/mount dump.
- **Still air-gapped.** As with the prior cloud-lane entries, this proves the control plane comes up and the hosted gate works end-to-end over the seed contract — not real ACME issuance, which remains the cloud-side CL6 live run (`malmoos/cloud` #18, `docs/ops/e2e-onramp.md`).
