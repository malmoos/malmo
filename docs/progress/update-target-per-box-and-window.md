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

**Tests.** In `internal/hostagent/updatetarget`: the id is sent as `box_id=…`; a source with no id asks a bare path with **no query at all**; the id joins an existing query without dropping it; the window is read from the wire when present and left empty when absent; an answer's window opens an apply that the box's own window would have held; an answer with no window leaves the box's own window in force (held at 12:30, applied at 12:50); an unreadable window warns **once** and still applies, with the box's own window chosen so an apply proves the fall-back went there and not to the default; a window that moves later within the same night does not start a second attempt for the same version (this one reproduces the retry-suppression bug above and fails without the fix); the same version still gets a fresh attempt the next night even though the window moved again in between. In `cmd/host-agent-real`: the box id comes from the seed, survives an env-var target, and is empty when there is no seed.

**Boot proof.** `write_target` takes an optional fourth argument and writes the `window` field only when it is given, so the refusal step still exercises the absent case. Two new assertions in the `update` boot: the journal shows the box resolving a `box_id` to send, and it shows `from=answer` before the apply. The drop-in's `MALMO_UPDATE_WINDOW` changed from `00:00-23:59` to `04:00-04:01` and the answer now carries `00:00-23:59`. That inversion is the real proof: the apply can only happen if the answer's window outranked the variable. A whole-day variable would have passed whether the box read the answer's window or ignored it.

**A self-review caught a second bug in the same tick, before merge: a moving window could double-apply in one night.** `Tick`'s one-attempt-per-night guard compared the exact start instant of the window's current occurrence against the instant it last attempted at. Making the window mutable per tick (the change above) meant that instant could now move between polls even when the target version did not, and a same-night change to the window's start (the control plane nudging the hour, or — since the window rides the same unauthenticated channel as the target — a forged answer) could make `Tick` read its own earlier attempt as belonging to an earlier occurrence and start a second one the same night. The spec (`UPDATES.md` # 8.4) is explicit that "the next attempt is the next night"; this broke that for a window that moves intraday. **Fixed by comparing the occurrence's calendar date instead of its exact start instant** (`occurrenceNight`, `internal/hostagent/updatetarget/loop.go`): it still uses `Window.Occurrence`'s own wrap-past-midnight rollback, so a window spanning local midnight is still one night, not two, but a same-night change to the window's start no longer looks like a new occurrence. Two considered alternatives: storing the attempt's raw wall-clock time (does not fully close the gap — if the window's start moves later and enough real time passes for the new, later start to still be before "now", the new occurrence's start is still after the old attempt's timestamp, and the guard still opens); and the naive calendar day of `now` (breaks a window that spans local midnight, since the code after midnight would see a different day than the code before it for what is really one continuous occurrence). Comparing dates derived from `Window.Occurrence` avoids both problems.

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

- **The boot-proof change is unexercised locally.** The hosted cloud lane needs root, `/dev/kvm` and about ten minutes, and `CLAUDE.md` says not to build that image locally. Both edits were made by reading. Three things a red run would catch: the `from=answer` and `box_id=` greps depend on the default `slog` text format, so a handler change breaks them; the assertion that the answer's window was taken (`window_taken`, before the apply-wait) catches most window-path breaks with its own message, but a bug *downstream* of that — the resolved window being read correctly but not honoured correctly when `Tick` actually applies — would still only show up as the generic "nothing applied in 420s" from the apply-wait loop, with no assertion of its own to point at the window; and the file server ignores the query string, so the ask carrying `box_id` is proven from the box's log rather than from the server's.
- **The drop-in's one-minute window (`04:00-04:01`) has a small, accepted flake risk.** It exists to prove the answer's `00:00-23:59` window outranks it — a whole-day drop-in would apply whether the box read the answer's window or ignored it. The residual risk is that the guest's wall clock happens to sit inside `04:00-04:01` during the run (about 1 in 1440), in which case the apply would succeed even if the code fell back to the drop-in instead of the answer. This is left as-is on purpose: the `window_taken` assertion (`from=answer`, checked before the apply-wait) still proves the answer won regardless of the clock, so the rare coincidence would only cost that one assertion's power to discriminate, not the whole proof — the apply-wait passing for the wrong reason on its own, without `window_taken` also passing, was never possible.
- **Nothing on the control-plane side reads the parameter or sends a window yet.** This is the box half of a two-side capability, and the other half is being written against the same contract. It ships inert: an answer with no `window` changes nothing, and an endpoint that ignores `box_id` answers exactly as it does today.
- **The box id is not validated.** Whatever the seed says is sent. A junk id gets a junk answer or none, and `Validate` still refuses anything unpinned, so the failure mode is "no update", not "wrong update".
- **The window is trusted from the same weak channel as the target.** Someone who could forge the answer could hold a box's updates to a one-minute window. That is a smaller version of the risk the target already carries, and it closes with the same credential.
- **A box that is already on its target never logs its window.** `windowFor` runs where the window is used, which is after the "already on the target version" check. The window only matters when there is something to apply.

## What's next

- The control plane starts answering per box, and setting a window for a box that needs a different hour.
- The report-back (# 8.4 step 5), which needs the credential in `NEXT.md` first — it is the first thing on this channel that writes.
