# The control-plane update transaction: apply, health-check, revert both

- **Status:** done
- **Date:** 2026-08-11
- **Specs touched:** docs/specs/UPDATES.md (# 3, no text change — this realizes it)

Fifth implementation slice of the update work designed in [#369](https://github.com/malmoos/malmo/pull/369), after [control-plane-image-ledger.md](control-plane-image-ledger.md) gave the box a declaration to write. Closes #380.

## What was done

New package `internal/hostagent/cpupdate` — the stream-B apply and rollback that `UPDATES.md` # 3 specifies and # 8 says is shared by both profiles ("the expensive half of the update machinery is shared, and we only build it once"). It is **trigger-free by design**: nothing in it knows whether the target came from a signed release manifest, from the cloud, or from an admin typing two refs. A caller hands it a pair; it applies it or puts the box back.

The transaction:

1. **Pull every moved ref.** Nothing on the box has changed yet, so a pull failure costs nothing.
2. **If the brain moved: stop it, then snapshot its SQLite.** In that order, because the brain opens the database with `journal_mode=WAL` and a copy taken under a live writer restores into a corrupt database — a safety net that only fails when used.
3. **Write the declaration — ledger and staged compose — before starting anything.** The # 8.3 handoff.
4. **Recreate only what moved**, the brain from `brainlaunch`'s own run spec.
5. **Health-check both** — the brain on `/healthz`, the UI on `/`, bounded at 60s.
6. **On any failure from step 2 on: revert both refs, restore the snapshot, put the containers back**, and report which step failed.

**Both refs revert even when only one moved.** A coordinated ship is tested as a pair, so a box left running half of one is in a combination nobody has ever run.

**The brain is recreated from `brainlaunch.RunSpecFor`, not from a second `docker run` builder.** An updated brain has to be identical to a first-boot one except for the image. A second builder would drift the first time a mount or env var is added, the box would boot fine, and the divergence would appear only after an update — the worst possible time to find out.

**Any HTTP response counts as "serving", including 500.** The question is "did the container come up and bind its port", not "is every route behaving". A brain answering 500 somewhere is still a running brain, and reverting the control plane over it would take a working box backwards. `/healthz` is 200-as-soon-as-serving by construction ([brain-healthz-probe.md](brain-healthz-probe.md)).

**A test caught a real ordering subtlety in this code.** The first version of the ordering assertion counted *any* container call, and it failed: the brain is removed (step 2) before the declaration is written (step 3). That turned out to be correct rather than a bug — the removal is needed for a consistent snapshot, and a crash in that window leaves the ledger naming the old pair, so the next boot brings the old brain back. The claim `UPDATES.md` makes is about **starting** a container on an image the declaration does not name, so the assertion now watches starts. The distinction is written down because it is exactly the kind of thing a later change could quietly break.

## How it maps to the specs

- `UPDATES.md` # 3 step 3 — pull, snapshot, recreate-only-what-changed, 60s health wait; step 4 — revert both on failure of either; the 7-day retention and GC.
- `UPDATES.md` # 8.3 / # 8.4 step 3 — one actor, declaration-before-recreate, and the same transaction under both triggers.
- `CLAUDE.md` # Go code discipline — consumer-side `Docker` and `Prober` interfaces, `slog` with `image`/`step`/`err`.

## Known gaps & deviations

- **Nothing calls `Apply` yet.** The trigger (#381) is the next slice: this ships the transaction and no way to start one.
- **Proven against a fake Docker only.** Eleven transaction tests plus three snapshot tests cover both-changed, UI-only, brain-only, declaration ordering, declaration restore, health-failure revert with a simulated schema migration, pull failure, no-op, GC in and out of the window, and the no-ledger derivation — but no real daemon, no real registry, no real brain restart. That is #382, and it is the gap that matters most here.
- **The health probe reaches containers by IP** (`docker inspect`), because the control-plane containers publish no ports. That works from the host, where host-agent runs, and is not a path anything else uses today.
- **`brainPort` and `uiPort` are constants.** The brain's port is `cmd/brain`'s `MALMO_LISTEN` default; a box that overrode it would be probed on the wrong port. Nothing overrides it today, and threading the value through would mean host-agent reading the brain's environment.
- **No retry, and no "three consecutive failures then pin"** (# 3 step 5). One attempt, one revert. The counter belongs with the trigger that decides when to try again.
- **GC deletes images and the snapshot, but never the ledger's own history**, since there is only ever one previous generation (# 3: "single-generation rollback").
- **A revert that itself fails is reported, not repaired.** `Result.RevertErr` is the operator's signal; there is no third generation to fall back to and inventing one would be guessing.

## What's next

1. **#381 — the trigger.** A minimal `system-update` job on the host socket plus the admin endpoint that starts it, taking explicit refs.
2. **#382 — the QEMU proof.** A real update and a real failed-update-then-revert on a booted box, against a registry inside the guest.
3. **The box → cloud target and report** (`UPDATES.md` # 8.1, # 8.4 step 5), still blocked on the box↔cloud credential (`NEXT.md` Tier 1).
