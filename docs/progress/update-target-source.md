# One update-target seam: the box learns which control plane to run, and applies it

- **Status:** done — hosted path proven on a booted box in the cloud lane; the appliance implementation is real but cannot produce a target yet (see Known gaps)
- **Date:** 2026-08-12
- **Specs touched:** `docs/specs/UPDATES.md` (# 3 # Update window, # 8.1, # 8.4 — "as built" notes), `docs/specs/RELEASE_MANIFEST.md` (# What the manifest is), `docs/specs/NEXT.md` (new Tier 2 item), `docs/architecture.md`, `CLAUDE.md` (two new slog fields)

Closes #401. [control-plane-update-transaction.md](control-plane-update-transaction.md) and [control-plane-update-trigger.md](control-plane-update-trigger.md) built the expensive half — pull by digest, snapshot SQLite, declare, recreate what moved, health-check, revert both on failure — and [release-manifest-poll.md](release-manifest-poll.md) gave an appliance a way to hear that a release exists. Nothing picked a target. This is the cheap half that does, and the cloud side it talks to was shipped in `malmoos/cloud` #128 (`GET /api/updates/target`, serving v0.6.0 since 2026-08-12).

## What was done

**One seam, two implementations** (`internal/hostagent/updatetarget`). `Source` is a single method — "what should this box be running?" — with three distinct outcomes: a target, `ErrNoTarget`, or a failure. `HTTPSource` reads the hosted update-target URL; `ManifestSource` reads the appliance release manifest off the poller that already fetches and verifies it. **One** `Loop` consumes them: compare, validate, hold for the window, apply. Neither profile has its own copy of the poll, the compare, the window or the apply, which was the point of the issue.

**The box never resolves a tag.** The answer carries full pinned references (`repository@sha256:<64 hex>`) resolved once by the sender. `Validate` runs before anything is pulled and refuses: an unpinned reference, a truncated or uppercase digest, an answer naming only one of the two images, a reference pointing at an unexpected repository, and a carried digest that disagrees with the digest inside its own reference. A refusal is logged loudly and changes nothing. The hosted source is unauthenticated today, so its answer is untrusted input and is treated as such.

**Half-answers are refused rather than half-applied.** The brain and the UI move together in one transaction (# 8.3), so a target naming only the brain is not "a brain-only update" — it is an answer nobody should act on.

**Unreachable and "no target" are different, and both are no-ops.** A 404 from the source is `ErrNoTarget` ("nothing is published"), which is why the cloud answers 404 rather than an empty 200. Anything else — DNS, refused connection, a 500, junk instead of JSON — is a failure. Both leave the box running what it runs, and the journal says which happened.

**The window is the hosted apply gate.** The user's call: a hosted box holds a new target until 03:00–04:00 local (`MALMO_UPDATE_WINDOW`) instead of restarting the control plane at noon. That is why the poll is every 15 minutes and not hourly — an hourly tick can land at 02:58 and 04:01 and step straight over an hour-wide window. Within one window a box makes **one** attempt per target version: a failed update has already reverted the box, so the next tick sees the same difference again, and without the guard a bad target would be retried every quarter of an hour until 04:00. A new version is still tried in the same window, because that is the fix arriving.

**The apply goes through host-agent's job lock.** `Agent.StartUpdate` was factored out of the `POST /v1/jobs/system-update` handler and is now the one way an update starts. Reaching past it into `cpupdate` would have stepped around the single global job lock, and an admin clicking Update during a target-driven apply would have put two transactions on the same containers.

**Appliance learns; it does not apply.** `autoApply` is false there, because the control plane on a box we do not operate stays admin-prompted (# 3). Only the hosted profile patches itself (# 8.2).

**Configuration, not constants.** `MALMO_UPDATE_TARGET_URL` (default the control plane's public endpoint), `MALMO_UPDATE_WINDOW`, and `MALMO_UPDATE_BRAIN_REPO` / `MALMO_UPDATE_UI_REPO` for the expected repositories. A box must be pointable at another source to prove a release before the fleet gets it, and the boot proof needs exactly the same knobs.

## Testing

31 tests in `internal/hostagent/updatetarget`, covering every case the issue asks for: no change (nothing applied, nothing logged twice), a change applied with the refs handed over verbatim, an unreachable source, a "no target" answer, seven shapes of bad answer, holding outside the window then applying inside it, one attempt per window, a new version inside the same window, an appliance never applying, and the HTTP source against a real `httptest` server (404, 500, junk, oversize body, unknown fields ignored).

**Checked against the live control plane**, not only against fakes: `HTTPSource` with its production default URL was run against `https://malmo.network/api/updates/target`, parsed the real v0.6.0 answer, and passed `Validate` with the default repositories. That is the whole cross-repo contract — field names, digest shape, repository — verified against the deployed sender rather than a fixture copied from it. The scratch test was removed afterwards.

**The booted-box lane now drives this loop, not just the admin trigger** (`dev/cloud/cloud-assertions.sh`, the `update` boot). After the existing happy-path and revert proofs it publishes a gen-3 pair to the in-guest registry, serves an update-target answer from a file server on the loopback, and restarts host-agent to force a tick. Two things are asserted: an answer naming **tags** is refused with the brain container untouched, and a pinned answer is pulled and applied with no admin in the loop — ledger, container generation label, and an authenticated request afterwards. The test image also points `MALMO_UPDATE_TARGET_URL` at a dead port for every boot, so no scenario can quietly pull the live fleet images if a CI run happens to land inside the window.

`make check` cannot run whole locally here (`libpam0g-dev` cgo fails in this environment on a pre-existing build error unrelated to the change); `gofmt`, `go vet` and the full suite were run with cgo disabled, both build tags of `cmd/host-agent-real` compile, and CI runs the real gate.

## How it maps to the specs

`UPDATES.md` # 8.4 steps 1 and 2 are now built, and the section says how. Three "as built" notes were added rather than changing what the spec decides: the seam is shared with appliance, the hosted target is currently **fleet-wide** rather than per-box (cloud #128 — per-box needs the box↔cloud credential `NEXT.md` Tier 1 parks), and the hosted apply waits for the 03:00–04:00 window even though # 3 says the control plane has no fixed window. That last one is a real tension in the spec as written: # 8.4 said "the next window", # 3 said there is none because an admin clicks. On hosted nobody clicks, so the window applies, and # 3 now says so.

`RELEASE_MANIFEST.md` gains a note that the manifest's version-only schema is what stops the appliance updater, with the fix parked in `NEXT.md`.

## Known gaps & deviations

- **The appliance source cannot produce a target.** It reads the verified manifest and refuses it as unpinned, because the manifest names versions and the box will not resolve a tag. This was a deliberate choice over two alternatives: composing `registry.malmo.network/malmo/brain:v1.4.2` (breaks the digest rule the whole issue rests on) and adding pinned-reference fields to the manifest now (designs the appliance publisher before one exists, and bakes the registry host into every signed file, which `RELEASE_MANIFEST.md` explicitly avoids). So the seam has two implementations and only one of them can currently answer. It is masked today — no signing key is baked into any build, so no appliance verifies a manifest at all — and it is written up in `NEXT.md`.
- **The report-back is not built.** `UPDATES.md` # 8.4 step 5 ("version now running, success or failure, the failure mode") and the fleet-side auto-halt (# 8.5) both need the box↔cloud credential. A box therefore applies its target and nobody on the fleet side learns whether it worked.
- **Per-box targeting is not built**, for the same reason. Every hosted box reads the same fleet-wide answer, so there is no pinning one box to an old version and no staged rollout.
- **Nothing surfaces the target in the dashboard.** On appliance the loop's only output is a journal line ("a different control plane is available"); the "Update available" prompt `UPDATES.md` # 3 promises is a separate slice, and so is the post-update notification on hosted.
- **A box that has never updated compares against its baked ref.** Before the first update there is no ledger, so `LedgerPair` falls back to `MALMO_BRAIN_IMAGE` — a local tag on a fresh hosted image, never equal to a pinned target. The first window after a box comes up therefore always applies, which is correct (it converges to the fleet) but means the very first update is a guaranteed one rather than a no-op.
- **ghcr packages are created private on first push.** Nothing here fixes that, and a box pulling a private image will fail at the pull step — logged as a failed job, reverted, retried the next night. Called out in the cloud-side entry too.
- **Job outcomes are still in-memory only.** A target-driven update leaves the same trace an admin-triggered one does, which is a journal line and the ledger (`NEXT.md` # Where a control-plane update's outcome lives after it finishes).

## What's next

1. **Pinned references in the release manifest** (`NEXT.md`), which is what makes the appliance half of this seam able to answer.
2. **The report-back and per-box targeting**, once the box↔cloud credential exists — that is one design, and it unlocks both plus the fleet-side auto-halt.
3. **The dashboard surfaces**: the appliance "Update available" prompt, and the hosted "we updated your box" notification.
