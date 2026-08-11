# The updater, proven on a booted box: a real update and a real revert in CI

- **Status:** done
- **Date:** 2026-08-11
- **Specs touched:** docs/specs/UPDATES.md (# 3, # 8.4), docs/specs/TESTING.md

Closes #382. It closes the gap the four merged updater slices all named in their own "what's next": [brain-healthz-probe.md](brain-healthz-probe.md), [control-plane-image-ledger.md](control-plane-image-ledger.md), [control-plane-update-transaction.md](control-plane-update-transaction.md) and [control-plane-update-trigger.md](control-plane-update-trigger.md) shipped the whole update path, and every one of them was proven only against a fake Docker. No real daemon, no real registry, no real brain restart, no real revert.

## What was done

A fifth boot scenario, `update`, in the existing hosted cloud lane — beside `unseeded`, `seeded`, `frozen`, `bios` and `access`. It runs in CI on a stock GitHub runner (`.github/workflows/ci-cloud-image.yml`), on its own fresh overlay and its own box-id, and it is in the publish gate: an image whose updater cannot replace the brain, or cannot put the box back when the new brain does not serve, must not be published.

The scenario drives the real admin trigger, `POST /api/v1/system/update`, from inside the guest. It needs an admin session to do that, so the boot is seeded with a test-portal key and given a signed owner assertion, exactly as the `access` boot is (`dev/cloud/mkassertion` — the private key never enters the VM).

**The happy path.** The guest starts a registry on `127.0.0.1:5000`, derives a new brain and a new UI image from the ones the box is running, pushes both into it, and drops both from the local image store. Then it asks the box to move to that pair and asserts:

- the job reaches `completed` with `brain_changed` and `ui_changed` true and `reverted` false;
- the brain container **id changed** and the running brain is on the target ref and carries a marker label only the new image has — the riskiest step in the whole design is host-agent recreating the brain while the brain is serving the request that asked for it, and this is written so that a failure there cannot read as a pass;
- the declaration is in **both** files (`UPDATES.md` # 8.3): `images.json` names the new pair and records the previous one, and the staged `compose.yml` pins the new UI ref;
- the new brain answers `/healthz` on its own container address — the same probe the updater uses;
- `GET /api/v1/system/version` reports the new UI image.

**The revert.** The same boot then points an update at a brain that starts but never serves, and that does damage on the way: its entrypoint truncates the brain's SQLite database and leaves a marker file. That turns two silent claims into observable facts. The assertions are:

- the job is `failed` with `failure_mode: health`, `reverted: true`, and no `revert_error`;
- the marker file exists, so the broken brain really ran — without this the whole revert half could pass on a box where the bad image never started;
- the brain is back on the previous good ref and the ledger names it again, and does **not** name the failed ref (the next boot would launch it);
- `/var/lib/malmo/state/malmo.db` starts with `SQLite format 3` again, so the snapshot was restored **over** what the broken brain wrote;
- the box answers an authenticated `/api/v1/me` with the same session cookie, and the dashboard serves — a restored database that could not answer a signed-in request would be a restore in name only.

Supporting changes: the full-response HTTP and cookie helpers move from inside the `access` branch to the shared scope (both scenarios drive the SSO landing); `json_str_of` reads a JSON field out of a response body; `http_status_addr` probes an arbitrary address, since neither the brain's `/healthz` nor the registry is reachable through Caddy; and the failure diagnostic block now dumps `images.json`, the compose image lines, the snapshot dir and host-agent's update log lines, so a red update boot is diagnosable from the serial log alone.

## The design fork: a real registry inside the guest

The lane is air-gapped by design, so a real `docker pull` needs something to pull from. The issue allowed a `docker load` + retag fallback, on condition that the limitation be stated plainly. **We did not take the fallback.** The guest runs its own registry, and the proof is stronger than "a pull happened": both target images are removed from the local image store after the push, and the scenario **asserts they are absent** before triggering the update. So the updater's `docker pull <ref>@sha256:…` has to fetch them back over HTTP, which is what a production box does (`BUILD.md` # 6 — boxes pull by digest). Docker treats a localhost registry as insecure by default, so this needs no daemon configuration.

Two things kept the cost low. The registry image lands in the **test-only** tree (`dev/cloud/test/mkosi.extra`, at `/var/lib/malmo/test-images/`), never in the production image that gets published and lean-checked — and not in `control-plane-images/` either, whose first-boot loader globs `*.tar` and would then load it on every boot of every scenario. And the new generations are made with `docker commit` from the images the box is already running, not `docker build`: the guest has no build context and no toolchain, labels merge on commit so the derived brain keeps `malmo.protocol.major` and passes the lockstep guard, and the new brain is the real brain plus exactly one changed thing.

## How it maps to the specs

- `UPDATES.md` # 3 step 3: pull, snapshot, declare, recreate only what moved, health-check — all four now run against a real Docker daemon.
- `UPDATES.md` # 3 step 3d and step 4: the 60s health wait actually expires, and the revert of **both** refs plus the SQLite restore actually runs.
- `UPDATES.md` # 8.3: the two-file declaration is asserted on disk after the update, which is what makes the brain reconcile to the running version instead of reverting it.
- `UPDATES.md` # 8.4 step 3: pull by digest, on a hosted box, air-gapped except for a registry inside it.
- `TESTING.md` # Hosted cloud variant: one more scenario in the same serial-verdict lane, same conventions (`fail`/`ok`, the SMBIOS `malmo.assert` credential, one boot per scenario).

## Known gaps & deviations

- **No bug was found in the merged updater.** The proof passed against `cpupdate`, `controlplane` and the trigger as merged, so this PR changes no Go code. That is a real result, but it is worth saying plainly rather than implying the lane found something.
- **The images are derived, not rebuilt.** The gen-2 brain is `docker commit` of the running brain with one added label, so it is byte-similar to the old one. That proves the recreate, the declaration and the pull; it does **not** prove a genuine schema migration, a brain with a different `internal/version` stamp, or a protocol-major bump being refused. `checkProtocolMajor` is exercised only on its passing path.
- **The snapshot restore is proven against a clobbered file, not a migrated one.** The broken brain truncates the database rather than migrating it, so what is proven is "the restore really overwrote what the new brain wrote and the result is a usable database". A rollback across a real forward migration is not exercised here.
- **Only the `health` failure mode is exercised.** `pull`, `snapshot`, `declare` and `recreate` failures still have unit coverage only.
- **The 7-day GC never runs**, since both generations are minutes old.
- **The registry runs as a plain HTTP localhost registry**, so nothing about TLS, auth or a remote registry's failure modes is covered.
- **One boot, two updates, sequentially.** Nothing here tests a second update arriving while one is running (the 409) on a real box.
- This lane cannot be run locally without root + KVM + mkosi; per `CLAUDE.md` it is driven through the `CI / Cloud image` workflow with `publish=false`.

## What's next

1. **A "last update" read.** The job record lives in host-agent's memory, so after a host-agent restart the box cannot answer "what happened the last time someone updated me".
2. **The dashboard surface** (`UPDATES.md` # 6): where the update appears, the ~30s notice before a coordinated ship, and the failure-with-rollback status.
3. **Retry and the three-strikes pin** (`UPDATES.md` # 3 step 5), which needs a trigger that picks refs on its own.
4. **The box → cloud target and report** (`UPDATES.md` # 8.1, # 8.4 step 5), still blocked on the box↔cloud credential (`NEXT.md` Tier 1).
