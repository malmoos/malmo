# A provisioned box can be pointed at its own update target

- **Status:** done - unit-tested on the untagged build that CI actually runs; not yet exercised on a booted box (see Known gaps)
- **Date:** 2026-08-12
- **Specs touched:** `docs/specs/UPDATES.md` (# 8.4, three new as-built bullets), `docs/specs/ENVIRONMENT.md` (# Provisioning & first-boot, # Updates), `CLAUDE.md` (two new slog fields)

Closes #404. [update-target-source.md](update-target-source.md) shipped the loop that learns which control plane a box should run and applies it. This entry makes the two things that steer that loop settable **per box, at provision time**.

## The problem

A hosted box took its update target from a URL compiled into `host-agent`, and its update window from a value only an on-box unit drop-in could change. Neither could be set when the box was created. There is no SSH on the hosted image (`ENVIRONMENT.md` # Access & files), so there was also no way in after boot. The practical effect: **a provisioned box could not be pointed at `?channel=candidate`**, which makes proving a release on one box before the fleet gets it impossible for exactly the boxes that matter, the real ones.

## What was done

**Two optional systemd credentials on `host-agent.service`**, the same delivery path `malmo.seed` already uses:

- `malmo.update_target_url` sets the update-target endpoint the box reads.
- `malmo.update_window` sets the local window an update may start in, as `HH:MM-HH:MM`.

**Precedence is credential, then environment variable, then the built-in default.** The credential is the per-box fact the provisioner injected; `MALMO_UPDATE_TARGET_URL` and `MALMO_UPDATE_WINDOW` stay underneath it as the local hand-edit an operator drop-in or the CI boot proof uses. A box provisioned with neither credential behaves exactly as it did before, which is every box in the fleet today.

**An unusable target URL is refused, not resolved away.** If the credential is present but is not an absolute `http` or `https` URL with a host, `startUpdateTarget` logs at error level and **does not start the loop at all**. It does not fall back to the fleet endpoint. The asymmetry with the window is deliberate and is the whole point of the issue: a box pinned to a candidate must not quietly join `stable` because someone left a trailing character in a provisioning template. An unusable *window* still falls back to 03:00-04:00 with a warning, because a wrong hour can only apply an update at the wrong time, while a wrong target sends the box to the wrong version. Refusing the loop leaves the box serving whatever it is already running, which is the safe end state; refusing to start `host-agent` would take the brain down with it.

**Which source won is logged once at startup**, the way `resolved brain image` logs `from_ledger`. A box on a non-default target logs at **warn**: "this box is not following the fleet update target". That has to be visible without knowing to go looking for it, because "why is this one box different" is the first question a divergent box raises.

**None of the logic is build-tagged.** The target URL only matters on the hosted build, but `go vet ./...` and `go test ./...` both run untagged in CI, so anything behind `//go:build hosted` is neither vetted nor tested there. The resolution lives in an untagged `cmd/host-agent-real/updateconfig.go`; only the call site (`updatetarget_hosted.go`) keeps the tag. `updateTargetSource` grew an `error` return on both profiles so the hosted half has somewhere to put a refusal; the appliance half always returns nil, because there is no per-box configuration on that path to get wrong.

**The downgrade property is now written down** (`UPDATES.md` # 8.4). The loop compares "does the running pair differ from the target pair" with no notion of "forward", so a box running something newer than its target rolls back to the target. That is correct, since per-box pinning wants deliberate downgrades, and it is surprising. It nearly bit us the day the loop shipped: a box provisioned from the v0.7.0 image would have rolled itself back to v0.6.0 overnight had `stable` not been promoted the same hour. Now that a target is settable per box, the number of people who can trip it goes up, so it is documented in the spec an operator reads and cross-referenced from `ENVIRONMENT.md`.

**Tests** (`cmd/host-agent-real/updateconfig_test.go`, 21 subtests, all running untagged in CI): credential present, absent, unparseable, and present-but-unreadable for both settings; credential beating the env var for both; a refused URL resolving to nothing rather than to the env var or the default; and the case that guards every existing box, neither source set leaving `HTTPSource` on its default URL and the window on 03:00-04:00. Every function in `updateconfig.go` is at 100% statement coverage, which is how the read-error branch is held honest: the unreadable credential is a **directory**, not a file at mode `0o000`, because root ignores the mode bits and the test would then assert nothing whenever the suite runs as root.

## How it maps to the specs

- `UPDATES.md` # 8.4 gains three as-built bullets: the per-box credentials and their precedence, the refusal behaviour and why the window differs, and the downgrade property with the instruction to check which way a target moves a box before setting it.
- `ENVIRONMENT.md` # Provisioning & first-boot gains the two credentials alongside `malmo.seed`, framed by the reason they exist (no SSH). # Updates (hosted) gains a one-line pointer to both.
- `CLAUDE.md` # Go code discipline gains `url` and `from` as standard slog fields, with `from` defined as one of `credential`, `env`, `default`.

## Known gaps & deviations

- **Not proven on a booted box.** The credentials are unit-tested against a `CREDENTIALS_DIRECTORY` laid out the way systemd lays one out, and the `ImportCredential=` lines are in the shipped unit, but nothing yet delivers either credential over SMBIOS or user data in the cloud boot proof. The boot proof drives the loop through `Environment=` drop-ins, which this change leaves working unchanged. A lane that delivers a real `malmo.update_target_url` credential and asserts the box read it is the natural follow-up.
- **Nothing on the cloud side sets these yet.** The credentials are the box-side half of a two-side capability: the box can now be pointed somewhere, and the provisioner has to actually point it. That work is not in this repo.
- **Only the credential is validated; the environment variable is not.** `MALMO_UPDATE_TARGET_URL` has never been checked and still is not. It is the operator's own hand-edit on a box they can already reach, so it does not carry the "pinned box silently joins stable" risk the credential does. Tightening it would also change the boot proof's contract, which is a separate change.
- **A credential file that exists but cannot be read is treated differently by the two settings**, on purpose and consistently with the parse failures: the URL refuses, the window warns and falls back. Both halves are tested. The self-review caught this claim sitting in this section with no assertion behind it, which is the failure mode this section exists to prevent, so the tests were added rather than the claim softened.
- **The `ImportCredential=` lines are in the shared unit, so an appliance imports them too.** Harmless: the window applies on both profiles, and the URL is only read by the hosted build. Scoping them to a hosted drop-in was rejected because the issue asks for a production capability in the shipped unit, not a profile-specific one.

## What's next

- Deliver one of these credentials in the cloud boot proof and assert the box read it, closing the gap above.
- The cloud-side control for setting a box's target, so the capability is reachable by an operator rather than only by a provisioning template.
