# Something can finally start an update: the system-update job and the admin endpoint

- **Status:** done
- **Date:** 2026-08-11
- **Specs touched:** docs/specs/BRAIN_HOST_PROTOCOL.md (# Pattern B — as built), docs/specs/BRAIN_UI_PROTOCOL.md (# Pattern A system routes), docs/architecture.md (# What is not built yet)

Sixth implementation slice of the update work designed in [#369](https://github.com/malmoos/malmo/pull/369), after [control-plane-update-transaction.md](control-plane-update-transaction.md) shipped the transaction with no way to start one. Closes #381.

## What was done

The trigger. An admin can now move the box's control plane, and watch it succeed or roll back.

**host-agent — the first job on the socket.** `POST /v1/jobs/system-update` takes `{brain_image?, ui_image?}` and answers `202 {job_id, kind, status, started_at}`; `GET /v1/jobs/{id}` returns the record, with `error {code, message}` and `result {brain_changed, ui_changed, reverted, failure_mode, revert_error}` once it ends. `internal/hostagent/jobs.go` holds an in-memory registry, a 30-minute `MaxDuration`, and one global lock. `cpupdate.Runner` is the provider behind the new `hostagent.SystemUpdater` seam; `cmd/host-agent-real` wires it from the **same** `brainLaunchConfig` it uses to launch the brain at boot.

**Brain — the admin route.** `POST /api/v1/system/update` (admin only) forwards the pair and returns the host job id; `GET /api/v1/system/update/{job_id}` polls it. `hostclient` gains `StartSystemUpdate` and `Job`, with two typed sentinels (`ErrUpdateInProgress`, `ErrJobNotFound`) so the brain can answer 409 and 404 instead of a flat 502.

**This is the minimum job surface, not Pattern B's framework.** The spec describes a kind registry with typed attributes, resource-class serialization, cancel, and an SSE log stream with 256 KB replay. There is exactly one job kind, so all of that would be an abstraction with a single consumer, which `CLAUDE.md` # Go code discipline says not to build. `BRAIN_HOST_PROTOCOL.md` # Pattern B now carries an "as built" block that says plainly what exists and what does not, rather than leaving the spec claiming something untrue.

**Deferred, and what would bring it back:** the kind registry with required attributes, resource classes and queue positions, the cross-class dangerous lock, cancel (SIGTERM → 10s → SIGKILL), the per-job SSE log with `Last-Event-ID` replay, and the `stalled` status. **A second job kind is the trigger to generalize** — `enroll-drive` is the likely one, and it is the case that actually needs classes (`disk` vs `apt`) and queueing rather than a flat refusal. Building it now would be guessing at the shape of a consumer that does not exist.

**A second update is refused with 409, not queued.** That is `Dangerous: true` ("never run two destructive ops concurrently") realized for one kind. Queueing is the right answer when two *different* dangerous ops collide; when the same op arrives twice, the second is almost always a double click, and an admin who clicks Update twice wants one update.

**The run gets its own context, not the request's.** The caller of this endpoint is the brain — which this very update may be about to replace. If the run inherited the request context, the brain's own recreate would cancel the transaction that was recreating it, mid-flight. The only bound on the run is `MaxDuration`; past it the context is cancelled and `cpupdate` rolls back on a context of its own.

**No `stalled` status.** The spec reserves `stalled` for "we are not sure — it is running too long". This job is sure: past `MaxDuration` it is cancelled, the transaction reverts, and the record says `failed` with `error.code = "job-timeout"`. That names both what happened and which rule fired.

**The job record lives in host-agent, and the brain does not wrap it in a job of its own.** The brain already has a Pattern B registry (`internal/api/jobs.go`), and using it here would have been the obvious move. It is also wrong: a brain-side record dies the moment the update recreates the brain, halfway through the operation it tracks. host-agent is the process that stays up, so the host job id is the handle, and a poll still answers after the brain has been replaced.

**Audit covers the start and every refusal.** `system.update` is a new elevation-class action: 403 for a member, 422 for no refs or a malformed one, 409 for the conflict, 502 for an unreachable host — each records `success=false`, and the accepted start records `success=true`. **`success=true` means "the update started", not "it worked"**: the brain cannot audit the outcome of an operation that replaces the brain. The outcome lives on the job record.

## How it maps to the specs

- `BRAIN_HOST_PROTOCOL.md` # Pattern B — `POST /v1/jobs/system-update` and `GET /v1/jobs/{id}` in the specified shapes; the as-built block records the subset.
- `BRAIN_HOST_PROTOCOL.md` # Failure semantics A — `MaxDuration` enforced uniformly by host-agent; `Dangerous: true` realized as one global lock.
- `UPDATES.md` # 3 (admin-triggered) and # 8.4 step 3 — one actor runs the transaction, and the trigger passes it a target pair.
- `CLAUDE.md` # Go code discipline — consumer-side `SystemUpdater` in `internal/hostagent`, concrete `cpupdate.Runner` in the provider; `slog` with `image`, `step`, `err`; elevation-class auditing on success and failure.

## Known gaps & deviations

- **The refs come from the caller — nothing picks them.** No release manifest, no cloud target poll, no hourly check, no "malmo X.Y.Z is available" notification. `UPDATES.md` # 3's steps 1 and 2 do not exist. The box↔cloud credential is Tier-1 undesigned (`NEXT.md`), and this slice was deliberately built so it does not need one.
- **No dashboard surface.** API only. Nothing in `web-ui/` calls these routes; the regenerated client types are the only UI-side change.
- **No retry, and no "three consecutive failures then pin"** (# 3 step 5). One attempt per request. The counter belongs with whatever decides *when* to try, which does not exist yet.
- **Job records are in memory and never evicted.** They are lost on host-agent restart, which matches "Dangerous: crash mid-flight = no auto-resume" — but it also means an admin who loses the job id has no way to ask "how did the last update go" apart from the ledger and the journal. A box runs a handful of updates in its life, so the map that only grows is not the problem; the missing "last update" read is the gap.
- **No elevation re-prompt.** The endpoint is admin-only, per the issue. `USERS_AND_GROUPS.md` # Elevation in the UI asks for a 5-minute re-prompt window on destructive UI operations, and replacing the control plane arguably qualifies. `requireElevated` was not added because the issue specified admin-only and there is no UI gesture yet to elevate from — it is a question for the maintainer, not a decision made here.
- **The `409` carries no `retry_after` and no running job id in a typed field.** The message names the running job; a caller that wants to follow it has to parse prose. Nothing needs it yet.
- **Proven against fakes on both sides of the socket.** The host tests drive a stub updater; the brain tests drive a canned host-agent (and a real one, over a real UNIX socket, in `internal/hostclient/jobs_test.go`). No test starts a real update on a real box — that is #382, and it stays the biggest gap in this whole stream.
- **`cmd/host-agent-real` was not compiled locally.** The PAM cgo dependency does not build on this machine (pre-existing on `dev`). The wiring was type-checked with `CGO_ENABLED=0 go build ./cmd/host-agent-real/`; CI does the real build.

## What's next

1. **#382 — the QEMU proof.** A real update and a real failed-update-then-revert on a booted box, driven through this endpoint against a registry inside the guest.
2. **A "last update" read.** Even without a manifest, the box should be able to answer "what happened the last time someone updated me" after a restart.
3. **The dashboard surface** (`UPDATES.md` # 6): where the update appears, the ~30s notice before a coordinated ship, and the failure-with-rollback status.
4. **The box → cloud target and report** (`UPDATES.md` # 8.1, # 8.4 step 5), still blocked on the box↔cloud credential.
