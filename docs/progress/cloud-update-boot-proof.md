# The updater, proven on a booted box: a real update and a real revert in CI

- **Status:** done
- **Date:** 2026-08-11
- **Specs touched:** docs/specs/UPDATES.md (# 3, # 8.4), docs/specs/TESTING.md

**It found a real bug on its first run, in merged code, and the spec's stated order is what was wrong.** That is the headline; the rest of this entry is the lane that found it.

Closes #382. It closes the gap the four merged updater slices all named in their own "what's next": [brain-healthz-probe.md](brain-healthz-probe.md), [control-plane-image-ledger.md](control-plane-image-ledger.md), [control-plane-update-transaction.md](control-plane-update-transaction.md) and [control-plane-update-trigger.md](control-plane-update-trigger.md) shipped the whole update path, and every one of them was proven only against a fake Docker. No real daemon, no real registry, no real brain restart, no real revert.

## The bug: two `docker compose up` on one project, at the same time

The first CI run of the new boot failed the happy path like this:

```
status "failed", error "resolve ui address: docker inspect malmo-ui: exit status 1"
result {brain_changed:true, ui_changed:true, reverted:true, failure_mode:"health"}
```

and one second earlier, in the guest's serial log, the newly started brain said:

```
control-plane stack up failed; continuing
  err="control-plane compose up: exit status 1
   Container 10490be176d1_malmo-ui Recreate
   Error response from daemon: Conflict. The container name "/4ded74550f8c_malmo-ui"
   is already in use by container 143d1be7..."
```

`cpupdate.recreate` started the new brain and **then** ran `ComposeUp`. But the brain runs `docker compose up -d` on that same `malmo-control-plane` project at every startup (`lifecycle.EnsureControlPlane`). So host-agent's compose and the brain's compose ran **at the same time, on one project**. Compose recreates a service by renaming the old container out of the way, creating the new one, then dropping the backup. Two of those interleaved, collided on the backup name, and left the box with **no container named `malmo-ui` at all**. The transaction's own health check then could not resolve the UI's address, called the update unhealthy, and reverted a change that was otherwise fine.

**The fix is the order: compose first, brain last** — on the apply path and on the revert path, which had the same order and the same race. Starting the brain last removes the concurrency rather than narrowing it: host-agent's compose runs while no brain exists, and the brain then boots into a stack that already matches the declaration, so its own reconcile is a no-op.

`UPDATES.md` # 3 step 3c specified the racing order in as many words — *"Recreate the changed containers in order: brain first (if changed), then UI"* — so the spec is corrected in this change, with the reason. The # 8.3 handoff makes the two actors agree on *what* should be running; it says nothing about *when* either may act, and concurrent compose on one project is what falls out of that gap.

**Only a real Docker daemon could surface this.** The transaction's fake Docker records calls in order, and both orders are equally valid to it — there is no second actor in a unit test, because the second actor is a brain container booting. Two new tests now pin the order on both paths (`TestComposeIsUpBeforeTheBrainIsStarted`, `TestRevertBringsComposeUpBeforeStartingTheOldBrain`); both were mutation-checked by restoring the old order and watching them go red — once here, and once independently by the maintainer in a separate worktree.

Two things worth recording from that red run, because they are results and not decoration:

- **The revert worked on a real box, under a real failure it was not designed for.** The job reported `reverted: true` with no `revert_error`, which means every revert step succeeded — including the `ComposeUp` that recreated the `malmo-ui` container the collision had destroyed. The box was put back on the old pair after a genuinely damaged intermediate state.
- **The failure was diagnosable straight from the serial log**, with no debugger and no second run: the job's error, the brain's compose error and the restored ledger were all in the captured output.

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
- `UPDATES.md` # 3 step 3c: **corrected** by this change. The recreate order is now UI first, brain last, with the race written down.
- `UPDATES.md` # 3 step 3d and step 4: the 60s health wait actually expires, and the revert of **both** refs plus the SQLite restore actually runs.
- `UPDATES.md` # 8.3: the two-file declaration is asserted on disk after the update, which is what makes the brain reconcile to the running version instead of reverting it.
- `UPDATES.md` # 8.4 step 3: pull by digest, on a hosted box, air-gapped except for a registry inside it.
- `TESTING.md` # Hosted cloud variant: one more scenario in the same serial-verdict lane, same conventions (`fail`/`ok`, the SMBIOS `malmo.assert` credential, one boot per scenario).

## Proven for the first time

With the fix in, the lane is green — all five boots, `unseeded seeded bios access update` ([run 31544941206](https://github.com/malmoos/malmo/actions/runs/31544941206), `publish=false`, nothing published). Three things are proven on a booted box that had never been proven anywhere:

1. **host-agent recreates the brain while the brain is serving the request that asked for it**, and the box comes back — new container, new image, routes re-installed, `/healthz` answering.
2. **A real pull by digest from a real registry.** The target images are removed from the local store first and their absence is asserted, so the pull is a fetch, not a lookup.
3. **A real revert**: a failed health check restores both refs, the two-file declaration, and the SQLite snapshot, and the box serves again on the old pair with the same session.

## Known gaps & deviations

- **The recreate-order fix is proven by this lane and by two unit tests, not by a longer soak.** A UI-only update still runs `ComposeUp` while the brain is up; that is safe today only because the brain reconciles the control-plane project **once, at startup**, and never on a timer. If that ever becomes periodic, the race comes back in a form this ordering does not fix.
- **The images are derived, not rebuilt.** The gen-2 brain is `docker commit` of the running brain with one added label, so it is byte-similar to the old one. That proves the recreate, the declaration and the pull; it does **not** prove a genuine schema migration, a brain with a different `internal/version` stamp, or a protocol-major bump being refused. `checkProtocolMajor` is exercised only on its passing path.
- **The snapshot restore is proven against a clobbered file, not a migrated one.** The broken brain truncates the database rather than migrating it, so what is proven is "the restore really overwrote what the new brain wrote and the result is a usable database". A rollback across a real forward migration is not exercised here.
- **Only the `health` failure mode is exercised.** `pull`, `snapshot`, `declare` and `recreate` failures still have unit coverage only.
- **The 7-day GC never runs**, since both generations are minutes old.
- **The registry runs as a plain HTTP localhost registry**, so nothing about TLS, auth or a remote registry's failure modes is covered.
- **One boot, two updates, sequentially.** Nothing here tests a second update arriving while one is running (the 409) on a real box.
- **The registry is in-guest and the box is air-gapped**, so no network failure is exercised: no slow or unreachable registry, no partial transfer, no auth, no TLS, no rate limit. A `pull` failure is still only covered by a unit test.
- **The pull moves one layer, not an image.** The derived images share every base layer with what the box already has, so the pull is a manifest plus a tiny diff. Nothing here says what a full multi-hundred-megabyte pull does to the 60s health budget or to disk on a small box.
- **This lane does not run on every PR.** It runs on the `CI / Cloud image` workflow's own triggers — a tag push, a manual `workflow_dispatch`, or the release call. A change that breaks the updater can land on `dev` and stay green until someone runs this workflow.
- This lane cannot be run locally without root + KVM + mkosi; per `CLAUDE.md` it is driven through the `CI / Cloud image` workflow with `publish=false`.

## What's next

1. **A "last update" read.** The job record lives in host-agent's memory, so after a host-agent restart the box cannot answer "what happened the last time someone updated me".
2. **The dashboard surface** (`UPDATES.md` # 6): where the update appears, the ~30s notice before a coordinated ship, and the failure-with-rollback status.
3. **Retry and the three-strikes pin** (`UPDATES.md` # 3 step 5), which needs a trigger that picks refs on its own.
4. **The box → cloud target and report** (`UPDATES.md` # 8.1, # 8.4 step 5), still blocked on the box↔cloud credential (`NEXT.md` Tier 1).
