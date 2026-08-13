# The update target moves to the seed, the one channel a real box has

- **Status:** done - unit-tested on the untagged build CI runs; the boot-proof change is unexercised locally (see Known gaps)
- **Date:** 2026-08-13
- **Specs touched:** `docs/specs/ENVIRONMENT.md` (# Provisioning & first-boot, the per-box update bullet and the seed-file shape), `docs/specs/UPDATES.md` (# 8.4), `CLAUDE.md` (the `from` field's allowed values)

Closes #407. [update-target-per-box.md](update-target-per-box.md) made the update target settable per box and delivered it as a systemd credential. That entry is a frozen snapshot and stays as written; this one records why the delivery channel was wrong and what replaced it.

## The problem

**The #404 mechanism never reached a real box.** `host-agent.service` carried `ImportCredential=malmo.update_target_url`, which reads systemd's credential store. A hosted box receives its per-box facts as **metadata user-data** from the provider, and user-data does not feed that store. `malmo.seed` looks like a counter-example but is not: it is a credential only in the QEMU lane, which is exactly why `malmo-seed-materialize.sh` has an HTTP fallback for production.

So the feature worked in CI and did nothing everywhere else. It passed every test it had, because every test it had was on the QEMU side of that line.

## What was done

**One optional field on the seed: `update_target_url`** (`internal/profile/seed.go`). The seed is the only production channel for a per-box fact. It is also **write-once** — user-data is taken when the VM is created and cannot be rewritten — so it fits a fact that is fixed for the life of the box. "Which control plane does this box belong to" is such a fact: a box never moves between control planes.

**The window did not move with it, and will not.** A window has to be changeable while the box runs, and a write-once channel cannot do that. It stays on `MALMO_UPDATE_WINDOW`, then the built-in default, until it moves into the control plane's answer. That is a separate change and this one does not wait for it.

**host-agent reads the seed itself, not through `profile.ReadSeed`.** That reader is the brain's and hard-errors on a seed with no `box_id` or `assertion_verification_key`, which is right for the brain and wrong here. host-agent wants one optional field. It unmarshals into `profile.Seed`, so there is still one definition of the wire shape, and it honours `MALMO_SEED_PATH` the way the brain does so tests and the boot proof can point both sides at one file.

**Three seed states, decided on purpose and each visible in the journal.**

- **Absent.** The appliance case, and a hosted box provisioned without a seed. Not an error: it falls through to the environment variable and then the default, and logs one line saying so.
- **Present and readable.** The field is used when it is set. An empty field carries no instruction, so it falls through like an absent one, and logs that too.
- **Present and malformed** (or unreadable). Refused: the update loop does not start. Bytes we cannot parse might have carried a target and we cannot tell, so reading the fleet endpoint instead could move a box that was meant to be pinned onto `stable`. This is the same call #404 already made for an unusable URL, for the same reason.

**Precedence is seed, then `MALMO_UPDATE_TARGET_URL`, then the compiled default** — the same shape as before, with the top source swapped. The `from` value in the startup log becomes `seed` where it was `credential`.

**The credential path is deleted, not kept alongside.** Both `ImportCredential=` lines, `readCredential`, `credUpdateTargetURL`, `credUpdateWindow`, `credentialsDirEnv` and `fromCredential` are gone. Two mechanisms for one setting, one of which silently does nothing in production, is worse than either alone. `windowSetting` keeps environment variable, then default.

**Everything else from #404 stands:** an unusable URL is refused and the loop does not start, an unusable window warns and falls back, and the winning source is logged once at startup — at **warn** when it is not the fleet default, because "this box is not following the fleet" has to be visible without going looking for it.

**The boot proof now exercises the channel.** The `update` boot in the hosted cloud lane already delivers a seed over SMBIOS. `seed_cred_keyed` takes an optional third argument and that boot passes its target URL there (`dev/cloud/run-cloud-tests.sh`), so the URL arrives the way production delivers it. The `30-update-target.conf` drop-in in `dev/cloud/cloud-assertions.sh` no longer sets `MALMO_UPDATE_TARGET_URL`; it keeps only the window and the repository overrides, which are genuinely local test knobs. A new assertion greps the journal for `from=seed` **before** the refusal and apply assertions. Without it, the apply would still pass on a box that read its target from anywhere at all — which is precisely how the credential mechanism stayed green while doing nothing.

**Tests** (`cmd/host-agent-real/updateconfig_test.go`, 19 subtests, all untagged so CI runs them): the seed field present, absent, empty, and with whitespace trimmed; a malformed seed and an unreadable one, both errors; **no seed file at all**, which is the appliance path and must not error; seed beating `MALMO_UPDATE_TARGET_URL`; the environment variable winning when the seed names no target; neither source set leaving `HTTPSource` on its default URL; four unusable URLs each refused to nothing rather than to the environment variable; and a seeded target leaving the window untouched. The unreadable case is a **directory**, not a file at mode `0o000`, because root ignores the mode bits and the assertion would evaporate whenever the suite runs as root.

## How it maps to the specs

- `ENVIRONMENT.md` # Provisioning & first-boot: the "per-box update credentials" bullet is replaced by one describing the seed field — where it lives, why it lives there, the three states, and the fact that the window is deliberately not in the seed. The seed-file bullet gains a pointer saying host-agent reads the field and the brain ignores it. The write-once channel bullet above it needed no change; it already named #404 as the mistake and #407 as the fix.
- `UPDATES.md` # 8.4: the #404 credential bullet is replaced by the seed mechanism, with the credential story kept as a short sub-bullet so the reason the channel changed is not lost. The refusal bullet gains the malformed-seed case. The window bullet no longer names `malmo.update_window`.
- `CLAUDE.md` # Go code discipline: `from` is now one of `seed`, `env`, `default`.

## Known gaps & deviations

- **The boot-proof change is unexercised locally.** The hosted cloud lane needs root, `/dev/kvm` and about ten minutes, and CLAUDE.md says not to build that image locally. Both script edits were made by reading. Two things a red run would catch: the seeded URL and the address `cloud-assertions.sh` serves `target.json` on must agree, and they are set in two different files with nothing but a failing boot to cross-check them; and the `from=seed` grep depends on the default `slog` text format, so a handler change would break it. The lane is the gate that has to run before this merges.
- **Nothing on the cloud side sets the field yet.** This is the box half of a two-side capability. The box can be pointed somewhere; the provisioner has to actually point it. That work is not in this repo, so this ships inert and safe: a seed without the field behaves exactly as a box does today.
- **The environment variable is still not validated.** `MALMO_UPDATE_TARGET_URL` has never been checked and still is not. It is the operator's own hand-edit on a box they can already reach, so it does not carry the "pinned box silently joins stable" risk. Tightening it would also change the boot proof's contract.
- **An empty `update_target_url` is treated as absent, not as unusable.** #404 refused an empty credential. An empty JSON field is what a template that filled in nothing produces, and it carries no instruction, so it falls through like a missing field — the same call `windowSetting` already made for an empty variable. The cost is that a provisioner who meant to pin a box and wrote an empty string gets fleet behaviour rather than a refusal. `omitempty` means the field round-trips as absent anyway, so the two cases are hard to keep apart on the wire.
- **The seed is read once, at host-agent startup.** A box whose seed changes while it runs keeps its old target until the next restart. Nothing rewrites user-data, so this is only reachable by editing the on-box file by hand.
- **`profile.Seed` now has a field the brain never reads.** The struct is the shared definition of the wire shape, and splitting it so each reader gets its own would give the two repos two formats to agree on instead of one.

## What's next

- Move the window and the per-box release into the control plane's answer, which `UPDATES.md` # 8.1 specified all along. That is the home for per-box policy that has to change while a box runs.
- The cloud side sets `update_target_url` at provision, which is what makes any of this do something on a real box.
