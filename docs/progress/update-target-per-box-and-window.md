# The box says which box is asking, and takes its window from the answer

- **Status:** done - unit-tested on the untagged build CI runs; the boot-proof change is unexercised locally (see Known gaps)
- **Date:** 2026-08-13
- **Specs touched:** `docs/specs/UPDATES.md` (# 8.1 and # 8.4), `NEXT.md` (the box-to-cloud credential), `CLAUDE.md` (the `from` field's allowed values), `docs/architecture.md`

Closes #408. [update-target-from-seed.md](update-target-from-seed.md) gave host-agent its seed reader and said the next step was to move the window into the control plane's answer. This is that step, plus the identity that makes a per-box answer possible at all. That entry is a frozen snapshot and stays as written.

## The problem

A hosted box asked its update-target source "what should I be running?" and never said who was asking. So the answer was the same for every box, and there was no way to move one box without moving all of them. `UPDATES.md` # 8.1 has always specified a target held **per `box_id`**, and recorded the fleet-wide read as a first step, not the destination.

The box already knows its `box_id`. It is in `seed.json`, which #407 taught host-agent to read.

## What was done

**The ask carries the box id: `GET <target-url>?box_id=<id>`.** `updatetarget.HTTPSource` gains a `BoxID` field and merges the parameter into whatever query the configured URL already has, so a box pointed at `…/target?channel=candidate` keeps its channel.

**A box with no identity sends no parameter at all.** Not an empty `box_id=`, which is a different statement — it names a box called nothing. An appliance box and a hosted box with no seed both take that path, and their request is byte-for-byte what it was before boxes said who they are. The appliance profile is untouched in every other way too: its source is the signed release manifest, not an HTTP endpoint.

**The box id comes off the same seed read as the target URL.** `seedTargetURL` became `seedUpdateFacts`, which returns both from one `os.ReadFile`. Two reads could see two different files and pair a box id with a target that was never meant for it. `updateTargetURL` became `updateTarget` and returns the id alongside the URL and the `from` value.

**The identity does not depend on where the URL came from.** A box whose target URL is a local `MALMO_UPDATE_TARGET_URL` hand-edit is still that box, so it still says so.

**The answer may name the window, and it wins.** One new optional field on the wire, `window`, in `HH:MM-HH:MM` form. Precedence becomes **answer > `MALMO_UPDATE_WINDOW` > the built-in default**. This is the home the #407 entry said the window was waiting for: a window has to be changeable while the box runs, and the seed can never be rewritten, but an answer can change on any poll.

**An answer with no window field is "no opinion", never "use the default".** Reading an absent field as the default would silently outrank an operator's `MALMO_UPDATE_WINDOW`. It is also what lets this land before the control plane sends a window: today every answer omits the field, and every box keeps behaving exactly as it does now.

**An unusable window in the answer warns and falls back to the setting below it.** It is not fatal, and it does not drop to the built-in default either — it falls back to what the box is configured with. The asymmetry with a bad target is the same one #404 made: a wrong hour can only apply an update at the wrong time, while a wrong target sends the box to the wrong version.

**The window is resolved per tick, not at startup.** It has to be, because the answer arrives on every poll. `Loop.windowFor` does it, and the loop carries `WindowFrom` only so a log line can say what an answer replaced. The window lines are deduped with **separate memory** from the target lines (`lastWindow`, not `lastQuiet`): the two move independently, and a target that changes nightly must not keep re-announcing a window that never moved.

**Logs.** The startup line still names the box's local window and its `from`. The hosted "update target resolved" line now carries `box_id`, so the journal says which identity went out. When an answer's window takes over, the loop logs it once with `from=answer` — a fourth allowed value for that field, written into `CLAUDE.md`.

**Tests.** In `internal/hostagent/updatetarget`: the id is sent as `box_id=…`; a source with no id asks a bare path with **no query at all**; the id joins an existing query without dropping it; the window is read from the wire when present and left empty when absent; an answer's window opens an apply that the box's own window would have held; an answer with no window leaves the box's own window in force (held at 12:30, applied at 12:50); an unreadable window warns **once** and still applies, with the box's own window chosen so an apply proves the fall-back went there and not to the default. In `cmd/host-agent-real`: the box id comes from the seed, survives an env-var target, and is empty when there is no seed.

**Boot proof.** `write_target` takes an optional fourth argument and writes the `window` field only when it is given, so the refusal step still exercises the absent case. Two new assertions in the `update` boot: the journal shows the box resolving a `box_id` to send, and it shows `from=answer` before the apply. The drop-in's `MALMO_UPDATE_WINDOW` changed from `00:00-23:59` to `04:00-04:01` and the answer now carries `00:00-23:59`. That inversion is the real proof: the apply can only happen if the answer's window outranked the variable. A whole-day variable would have passed whether the box read the answer's window or ignored it.

## The identity is weak, on purpose, and written down

A bare `box_id` on an unauthenticated endpoint means anyone who learns a box-id can read what that box is told to run, and can claim to be that box when asking. It is accepted for now: the ask is a **read**, nothing is mutated by it, the answer is version information rather than tenant data, and the operator path that sets a target sits behind the control plane's own auth. The costs are that a third party could watch one box's rollout, and that the control plane cannot trust the id it is handed.

**Nothing that mutates state may be built on it.** The report-back (`UPDATES.md` # 8.4 step 5) and the fleet-side auto-halt (# 8.5) both write, so they wait for real authentication. That is recorded in `UPDATES.md` # 8.1 and tracked in `NEXT.md` as a new item. No authentication was built here.

## How it maps to the specs

- `UPDATES.md` # 8.1: the "as built, the target is fleet-wide" caveat is gone, replaced by the `box_id` ask and the trade-off it carries. The undesigned-credential paragraph stays and is now the thing that closes the trade-off.
- `UPDATES.md` # 8.4: a new bullet gives the window its new home and states the three-way precedence, the "no opinion" rule, and the warn-and-fall-back rule. The #407 sub-bullet keeps its reason for the window not being in the seed and loses its "until it moves" clause, which has now happened.
- `NEXT.md`: a new item, "Authenticate the box to the control plane".
- `CLAUDE.md`: `from` gains `answer`.
- `docs/architecture.md`: the hosted source is now asked with the box's `box_id`, in the window the answer names.

## Known gaps & deviations

- **The boot-proof change is unexercised locally.** The hosted cloud lane needs root, `/dev/kvm` and about ten minutes, and `CLAUDE.md` says not to build that image locally. Both edits were made by reading. Three things a red run would catch: the `from=answer` and `box_id=` greps depend on the default `slog` text format, so a handler change breaks them; the narrowed `MALMO_UPDATE_WINDOW` means a bug in the window path now shows up as "nothing applied in 420s" rather than as a window message; and the file server ignores the query string, so the ask carrying `box_id` is proven from the box's log rather than from the server's.
- **Nothing on the control-plane side reads the parameter or sends a window yet.** This is the box half of a two-side capability, and the other half is being written against the same contract. It ships inert: an answer with no `window` changes nothing, and an endpoint that ignores `box_id` answers exactly as it does today.
- **The box id is not validated.** Whatever the seed says is sent. A junk id gets a junk answer or none, and `Validate` still refuses anything unpinned, so the failure mode is "no update", not "wrong update".
- **The window is trusted from the same weak channel as the target.** Someone who could forge the answer could hold a box's updates to a one-minute window. That is a smaller version of the risk the target already carries, and it closes with the same credential.
- **A box that is already on its target never logs its window.** `windowFor` runs where the window is used, which is after the "already on the target version" check. The window only matters when there is something to apply.

## What's next

- The control plane starts answering per box, and setting a window for a box that needs a different hour.
- The report-back (# 8.4 step 5), which needs the credential in `NEXT.md` first — it is the first thing on this channel that writes.
