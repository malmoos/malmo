# Update-target read: what the seam decided, on the socket and through the brain

- **Status:** done
- **Date:** 2026-09-07
- **Specs touched:** docs/specs/UPDATES.md, docs/specs/BRAIN_HOST_PROTOCOL.md

Closes #443. It is the last unbuilt piece of #399; the seam itself shipped in #401, and #404 / #407 / #408 moved the target URL to the seed, added the box id to the ask, and let the answer carry a window. This adds the read on top of it, and no UI.

## What was done

`updatetarget.Loop` decided every 15 minutes and told nobody. Its whole state was private — `lastQuiet`, `lastWindow`, `attempted` — and its only output was the journal. On an appliance that is a dead end: `AutoApply` is false there, so the loop stops at the log line "a different control plane is available", and the admin prompt `UPDATES.md` # 3 promises has nothing to read.

The loop now records each tick behind a mutex (`internal/hostagent/updatetarget/state.go`): the outcome, the answer, when it checked, why it produced nothing, and the window that was in force. host-agent serves that at `GET /v1/system/update-target`, and the brain re-serves it admin-only at `GET /api/v1/system/update-target`.

Four decisions are worth naming, because each is a case the obvious implementation gets wrong.

**One `state` field, seven values, none of them merged.** `current`, `available`, `none`, `unreachable`, `refused`, `disabled`, `unknown`. The four the loop produces are the three ways the `Source` contract can end plus the one the box itself produces — an answer it read and then refused. A source that is down and a source stuck on a bad answer must never read the same: only one of them is a fleet-wide problem, and the cloud lane already proves the two separately.

**A box with no loop says `disabled`.** When a seeded `update_target_url` is unusable, host-agent refuses it and starts no loop at all (`UPDATES.md` # 8.4 — refused, never resolved away). Without a state of its own that box reports "never checked", which is what a box that booted a second ago also reports. "This box will never update" and "this box has not checked yet" are different facts and only the first needs a human, so `cmd/host-agent-real` wires a reporter with the reason on the refusal path.

**The running pair is read when the request arrives, not carried from the tick.** A tick that ends early — `ErrNoTarget`, an unreachable source, a refusal — never calls `Current.Running()`, and host-agent outlives a control-plane update, so a stored pair would be stale exactly after an apply. It is two small file reads (`images.json` plus the staged `compose.yml`), so the handler does them. `TestReport_RunningIsReadPerRequest` moves the pair under a report with no new tick and asserts the state follows.

**The two `from` settings stay apart.** `from` is where the box's update-target URL came from (`seed`, `env`, `default`, from `updateconfig.go`); `window_from` is where the window came from (`answer`, `env`, `default`, from `Loop.windowFor`). The values look alike and the settings are not: a window can come from the source's answer, a URL never can. The issue as first written listed all four values under one field, which no single setting has. The loop knew the window's source and not the URL's, so `updateTargetSource` now returns a small `targetSource` struct carrying it.

Two smaller things came with it. `windowFor` is resolved right after validation instead of on the apply path, so a box that is already current still reports the hour it would update in — the log line was already deduped, so this costs nothing and the ordering change is only that the window line can now appear before "already on the target version". And the field carrying the underlying error is named `detail`, not `error`, because it holds raw Go error text: it is a diagnostic for an admin, and `state` is what the dashboard writes its sentence from.

The fake host-agent defaults to `none` — the honest answer for a dev box with no control plane behind it — and `MALMO_FAKE_UPDATE_TARGET` selects any other state, following the `MALMO_FAKE_NO_GPU` precedent. A stub stuck on "nothing to offer" would leave the dashboard prompt nothing to be built against; one that always claimed a target would lie in every session.

## How it maps to the specs

- `UPDATES.md` # 8.4 gains the "as built (#443)" bullet: the loop's decision is readable, the running pair is read on request, a box with no loop reports it, and the two `from` settings are separate on the wire.
- `BRAIN_HOST_PROTOCOL.md` # Pattern A gains the endpoint with its payload and all seven states. It is **200 always**, like `/v1/health/system` and for the same reason: every failure this read can have is a state in the payload, so an HTTP error would tell the brain "ask again later" about facts that will not change on their own.
- `UPDATES.md` # 3's "control plane is admin-prompted" is what this unblocks. The prompt is a later slice; this is the read it consumes.
- The wire is digests, never tags (`UPDATES.md` # 8.4). A refused answer is reported with the refs it named, because that is what an operator needs to fix the source — nothing acts on them.

## Tested

- Unit: the four loop outcomes and their reasons, the window resolved from an answer versus the setting, and a `-race` test running ticks against reads, since the writer is the tick and the reader is an HTTP handler.
- Adapter: all seven states, the pinned pair on the wire, no target object on `none`, the per-request running-pair read, an unreadable declaration, and the two `from` fields staying apart. Runs under both build tags.
- host-agent: the route with no reporter wired (`unknown`, not an error) and with one.
- Brain API: the admin gate, a member's 403 with no audit row (a pure read), the pass-through of an available answer, `none` as a normal answer, `unreachable` keeping its reason, and a host failure as 502.
- Against a real socket: the fake binary was run for each `MALMO_FAKE_UPDATE_TARGET` value and the payload read over the UNIX socket with `curl --unix-socket`.
- `fmt-check`, `vet`, `openapi-check` and the full Go suite green. `make check`'s `vet ./...` step fails on this machine for an unrelated reason — a gitignored `dev/cloud/mkosi.tools/` left over from an old local mkosi build contains vendored Go with unreachable code — so vet and the suite were run over the real package list instead. CI checks out clean and does not see it.

## What the review changed

Four findings, all taken. The fresh agent found two: `disabled` was losing to an unreadable declaration (the running-pair read returned before the `loop == nil` check, so a box with a bad seed **and** a missing compose reported the transient-looking fault instead of the permanent one), and the `target` field's doc claimed it is present only for `current` and `available` when `refused` carries it too — a client trusting that comment would read a rejected answer as an applicable pair.

Greptile found the one that matters. **The update-target URL is operator-settable, so it can carry credentials, and the errors that name it now reach an API response.** This change had deliberately kept the URL off the wire for exactly that reason, and then leaked it anyway through `detail`: `HTTPSource.Target` formats `fetch %s` with the raw URL, and `checkTargetURL` quotes the seeded value verbatim into what becomes the `disabled` state's reason. `updatetarget.RedactURL` now strips userinfo everywhere the URL is written for a person to read, including the two startup log lines — so the journal is covered too, where the exposure already existed before this change. A URL that will not parse is replaced rather than passed through: bytes we could not read are bytes we cannot prove are safe, and the caller's own message already names the setting at fault.

Two smaller ones from the same review: one tick now writes **one** snapshot (recording the good answer and then overwriting it published an intermediate "all fine" a concurrent reader could catch), and `state` carries an `enum` tag, so the generated client is a union of the seven values and a dashboard that forgets one fails to typecheck. The disabled path also reports its `profile` now, taken from a build-tagged constant — the profile is a build-time fact, so it survives a source the box could not build.

## Known gaps & deviations

- **Not run in the QEMU cloud lane.** The `update` boot gains four assertions (the read reports the refusal and `from=seed`, refuses an unauthenticated caller, and names the pinned pair the in-guest source served before the apply lands), and they are written but unproven — that lane needs root and `/dev/kvm`, or a `CI / Cloud image` run. This is the issue's real acceptance gate for the hosted half.
- **No UI.** By design. Nothing in `web-ui` reads the endpoint; only the generated client moved.
- **An appliance with a signing key would sit on `refused`, permanently.** The manifest names versions, not pinned references, so `ManifestSource` answers `ErrNotPinned` — an error, which lands in `refused` rather than `none`. No build bakes a key today, so every appliance reads `none`; the moment appliance signing lands, the read says `refused` until #400 is unparked. That is honest, and it is why the state is on the wire rather than flattened, but the later UI slice has to know it before writing the copy.
- **The read says nothing about whether the last update worked.** Job records are in-memory in host-agent and die with it, which is the open `NEXT.md` item on where a control-plane update's outcome lives. The endpoint is named `update-target` and not `update-status` so it does not appear to answer that.
- **Nothing proves the brain and host-agent agree about the running UI image.** `GET /api/v1/system/version` reads it from the staged compose brain-side; this read gets it from host-agent, which reads the ledger for the brain and the same compose for the UI. They should agree, and no test asserts they do.
- **`unreachable` covers two things**: a source that could not be read, and a box that could not read its own declaration. `detail` says which. An eighth state was not worth it, and both mean "the box cannot tell you".

## What's next

- The dashboard surface. `UPDATES.md` # 6 promises Settings → Updates, and on appliance # 3 promises the admin prompt. Both read this endpoint; `MALMO_FAKE_UPDATE_TARGET` exists so all seven states can be built against in the inner loop.
- Run `CI / Cloud image` on this branch (`-f publish=false`) to close the QEMU gap above.
- The report-back the other way (`UPDATES.md` # 8.4 step 5) is still blocked on the box↔cloud credential (`NEXT.md` Tier 1). This read is the half that needs no authentication.
