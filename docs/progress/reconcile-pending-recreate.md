# Reconcile converges env-restamping drift on a running container

- **Status:** done
- **Date:** 2026-06-29
- **Specs touched:** `APP_LIFECYCLE.md` # reconciliation pass (the drift list gained the pending-recreate case)

Closes #268. The `manifest-config-block.md` entry (#264) left a documented gap, reprised by `RebindMail`: `SetConfig`/`RebindMail` are brain-commits-first — they write the store + override/.env, then `compose up -d` a running instance. If that follow-up `compose up` fails, the job reports failure and the committed override/.env already hold the desired env, so a container that *fell over* (or a brain restart) converges via the reconcile pass's "no containers" branch. But a container that *kept running* stayed on its old env, because reconcile re-created an already-running container only on resource-limit drift, never on env drift. The gap "spans config + mail" and was tracked nowhere until #268. This slice makes the reconcile pass converge it for both, in one place.

## What was done

- **`internal/store/` (`store.go`)** — a `pending_recreate` boolean column on `instances` (`INTEGER NOT NULL DEFAULT 0`, in the `CREATE TABLE`, the idempotent ALTER-TABLE migration list, `instanceColumns`, and `scan`), surfaced as `Instance.PendingRecreate`. `SetInstancePendingRecreate(id, bool)` sets/clears it (`ErrNotFound` on a missing row, same shape as `SetServiceIdentity`). `Create` is untouched — new rows default to `0`.
- **`internal/lifecycle/` (`lifecycle.go`)** — two helpers. `recreateRunning(ctx, inst)` replaces the bare `compose up` in the running-instance edit path: on a `compose up` failure it marks the instance `pending_recreate` (the brain has already committed the override/.env, so the marker is the reconstructible intent), on success it clears the marker. `clearPendingRecreate(inst)` is the shared no-op-if-clear clear. The `Reconcile` already-up branch now recreates when the resource-limit policy drifted **or** `inst.PendingRecreate` is set — one `compose up -d` converges both (env is read at container-create) — and clears the marker on success; the `restore()` rewind is guarded to the resource-stanza patch (`restore != nil`) while the marker, not a file rewind, is what makes the env recreate retryable. The "no containers" branch clears the marker after it brings a drifted instance up, so a pending instance whose container also fell over is satisfied there without a redundant recreate on a later pass.
- **`internal/lifecycle/` (`config.go`, `mail.go`)** — `SetConfig` and `RebindMail` call `recreateRunning` instead of their own `compose up`; their doc comments now describe the real convergence boundary (the reconcile pass brings a stranded running container to the new env via the pending-recreate marker) rather than the old "stays stale until the user retries".
- **Tests** — `store`: `TestSetInstancePendingRecreate` (default-false, Get + List round-trip, clear, `ErrNotFound`). `lifecycle` (`pending_recreate_test.go`): a failed `SetConfig` marks then a successful retry clears; a failed `RebindMail` marks; reconcile recreates + clears a pending *running* instance and does **not** re-create it on a second (converged) pass; a failed reconcile recreate leaves the marker set; the "no containers" branch clears the marker; a clean install + healthy reconcile never set it.

## How it maps to the specs

`APP_LIFECYCLE.md`'s reconciliation list now carries the pending-recreate drift case as a fourth bullet, alongside running-no-containers / stopped-with-containers / orphans. The mechanism is the spec's own "every state-changing op records 'I am about to apply this change' before issuing it" pattern (line 89) made concrete for env-restamping edits: the marker is set at the failed edit, and reconcile re-applies desired. It is the brain-commits-first / "host is reconstructible" posture from `CLAUDE.md` # Load-bearing decisions — the marker persists in SQLite so the running container converges on the next startup pass.

## Known gaps & deviations

- **Startup-pass cadence.** `Reconcile` runs once at brain startup (`cmd/brain/main.go`), not on a timer, so a stranded *running* container converges on the next brain restart, not seconds after the failed edit. This matches the issue's framing ("low-severity, self-healing-on-restart edge") and the spec's imperative, no-reconciler-loop stance; a periodic reconcile is out of scope and unspecced.
- **No store-write fault coverage.** The three `slog.Warn` branches that fire only when `SetInstancePendingRecreate` itself errors (a SQLite write failure) are uncovered — the lifecycle fakes back onto a real SQLite store with no fault injection, the same reason the adjacent `restore()`-error and `SetServiceIdentity` Exec-error logs are untested. All new *logic* (mark-on-failure, clear-on-success, reconcile retry, no-churn, no-containers clear, store round-trip + `ErrNotFound`) is covered.
- **`architecture.md` unchanged.** The issue suggested recording the policy in `docs/architecture.md` too, but its only reconcile mention is the one-line `lifecycle` responsibility cell, which already reads "reconcile pass" at the right altitude; the convergence policy lives in `APP_LIFECYCLE.md` (which owns the drift list) and here.

## What's next

- Nothing required for #268. If a future change adds a periodic reconcile, the marker mechanism already drives convergence on every pass, not just startup.
- Any future env-restamping op (beyond config/mail) gets convergence for free by routing its running-instance recreate through `recreateRunning`.
