# Working on malmo

The end-to-end loop for contributing an implementation slice: get oriented, pick a task, branch, build, test, document, open a PR. Read this once; it links out to the docs that own each step rather than repeating them.

This guide is written so a contributor **and their coding agent** can both follow it. If you're driving Claude Code, point it here first — most of what an agent needs to not go off the rails (conventions, where docs live, the definition of done) is one hop from this page.

## Step 0 — Get oriented (~30 min)

Read in this order. Don't skip [`../../CLAUDE.md`](../../CLAUDE.md) — it holds the load-bearing conventions and overrides default agent behavior.

1. **[`../../CLAUDE.md`](../../CLAUDE.md)** — what malmo is, the audience, the locked decisions, and the code/doc discipline you're held to. The **Documents** section is the annotated map of every spec.
2. **[`../specs/SPEC.md`](../specs/SPEC.md)** and **[`../specs/CONTROL_PLANE.md`](../specs/CONTROL_PLANE.md)** — the vision and the control-plane architecture (brain + host-agent + Caddy).
3. **[`../README.md`](../README.md)** — the doc map. You don't read every spec now; you read the one(s) your task touches, end-to-end, when you pick it up.
4. **[`running-locally.md`](running-locally.md)** — get the stack running natively (no VM) before you write a line.
5. **[`testing-brain.md`](testing-brain.md)** + **[`../specs/TESTING.md`](../specs/TESTING.md)** — the test model (brain test pyramid + boot-level lanes).

The spec docs are the source of truth and cross-reference each other heavily. When your task names a spec, read that spec **end-to-end** before changing behavior — a decision in one doc usually constrains others.

## Step 1 — Pick a task

Actionable implementation work lives in **[GitHub Issues](https://github.com/onel/malmo/issues)** — the parallel-work board. Find work and claim it:

```bash
gh issue list --label P1                 # highest priority first; also P2, P3
gh issue list --label "area:frontend"    # filter by area: backend / frontend / tooling
gh issue view <N>                         # full task: spec refs, files to touch, "Done when"
gh issue edit <N> --add-assignee @me      # claim it — assignment, so no one double-grabs
```

Pick the highest-priority issue (`P1` > `P2` > `P3`) that is **unassigned** and **not labelled `blocked`**, then assign it to yourself before you start.

- Issues are written to be **self-contained** — each names its spec(s), the files it touches, and a crisp *Done when*.
- **Dependencies:** an issue labelled `blocked` has an unmet dependency named in its body (e.g. "Depends on #2"). Don't start it until that issue is closed; drop the `blocked` label when the dep lands.
- The maintainer's critical-path queue is separate — the **Up next** list in [`../progress/README.md`](../progress/README.md). The issue board is the work carved off for parallel contribution; the two are kept from overlapping on purpose.

If a task turns out to need a **design decision that isn't in a spec**, stop — that's a `NEXT.md` item, not implementation. Surface it (comment on the issue + flag the maintainer); don't invent the answer (see [`../specs/NEXT.md`](../specs/NEXT.md) and CLAUDE.md # Working style).

### ⛔ Owned / in-flight — don't touch

The **boot / storage / LUKS+TPM track** is actively in flight ([`../progress/0023-luks-tpm-enrollment.md`](../progress/0023-luks-tpm-enrollment.md)). Stay out of: `dev/test-qemu/**`, `cmd/malmo-storage-verify/`, `internal/storageverify/`, `dist/systemd/**`, and the `BOOT.md` / `STORAGE.md` specs. Coordinate with the maintainer before starting anything in that area.

## Step 2 — Branch off `main`

Always work on a branch; **never commit to `main`**. Every change lands via a PR into `main`.

```bash
git checkout main && git pull
git checkout -b <area>/<short-slug>      # e.g. feat/notifications-table, fix/health-audit-ids
```

Branch-name shape: `feat/…`, `fix/…`, `test/…`, `docs/…` + a short kebab slug. One task per branch.

## Step 3 — Build it

The inner dev loop is all native, no VM — see [`running-locally.md`](running-locally.md) (`make dev` runs agent + brain + UI together). Hold to the conventions already in the tree:

- **Go discipline** — CLAUDE.md # Go code discipline (consumer-side interfaces, layer boundaries, `log/slog` only, standard structured field names, typed errors only at boundaries, no premature abstraction). Match the surrounding code.
- **Don't build host-integrated subsystems without the VM outer loop** the specs assume (CLAUDE.md # Repo state). The fake host-agent (`cmd/host-agent`) is the inner-loop stand-in.
- **Keep specs and reality in sync** — if your implementation realizes or *diverges* from a spec, update the matching `docs/specs/` doc in the same change, and add a `DECISIONS.md` entry if a locked decision flips.

## Step 4 — Test it

Every behavioral change ships with tests. Which layer depends on what you touched — see [`testing-brain.md`](testing-brain.md) for the brain pyramid (unit → store → lifecycle-with-fakes → API → integration → e2e) and [`../specs/TESTING.md`](../specs/TESTING.md) for the boot-level lanes (nspawn fast / QEMU medium / soak).

Run the suite before you push:

```bash
make test           # full suite (needs libpam0g-dev — see running-locally.md)
make test-nopam     # skip the PAM-cgo target if you don't have the headers
```

Lane-specific targets exist too (`make test-health`, `make test-caddy`, `make test-usermgr-nspawn`, `make test-boot-chain-nspawn`, `make test-medium-qemu`, …); `make help` lists them. Put unit/store/API tests **in the same package** by default (CLAUDE.md # Go code discipline).

For slices that integrate with a real external system (Docker, PAM, systemd, Caddy, a real VM boot), **unit tests aren't enough** — exercise it against the real thing before you call it done, and say so in the progress entry. Don't rabbit-hole on test scaffolding: if verification keeps failing on the harness rather than the feature, step back and test the feature.

## Step 5 — Document it

A change is not complete until its docs are in the **same change** (CLAUDE.md # Documentation discipline). Concretely:

1. **Write a progress entry.** Add the next-numbered `docs/progress/NNNN-<slug>.md` following the template in [`../progress/README.md`](../progress/README.md) # Entry template (status, date, specs touched, what was done, how it maps to specs, known gaps, what's next). Progress entries are **append-only history** — never retro-edit an earlier one; link back to it instead.
   - Be honest in "Known gaps & deviations." Every "handled" claim must be verifiable in the diff — don't assume symmetry between similar code paths.
2. **Update the indexes** in the same change: add your entry to the table + "Latest" list in [`../progress/README.md`](../progress/README.md) and [`../README.md`](../README.md), and re-order the **Up next** queue if you consumed or added a follow-up. A doc not linked from the map is a bug.
3. **Update the spec** you touched if behavior realized or diverged from it. Update the root [`../../README.md`](../../README.md) quickstart if the dev workflow changed.
4. **Markdown style:** no line wrapping (continuous lines, not ~70-char breaks). Never use the `§` symbol — write `#`.

## Step 6 — Open the PR

```bash
git push -u origin <your-branch>
gh pr create --base main --fill        # then flesh out the body
```

PR body must include **`Closes #<N>`** (the issue it resolves — this auto-links and auto-closes the issue on merge), plus: the spec(s) touched, what you tested (and against what — real Docker? a VM boot?), and any known gaps. If your work unblocks a dependent issue, drop its `blocked` label (`gh issue edit <N> --remove-label blocked`).

**Don't merge your own PR** unless the maintainer has said to. PRs into `main` get a review pass first; merging closes the linked issue automatically.

## Definition of done — checklist

- [ ] Behavior works in the inner loop (`make dev`), and integration-tested against the real system if it touches one.
- [ ] Tests added at the right layer; `make test` (or `make test-nopam`) green.
- [ ] Progress entry written (`docs/progress/NNNN-…`); indexes in both READMEs updated; Up-next re-ordered if needed.
- [ ] Spec doc updated if behavior realized/diverged; `DECISIONS.md` entry if a locked decision flipped.
- [ ] No `§`, no hard-wrapped markdown, `log/slog` only, conventions per CLAUDE.md.
- [ ] Branch off `main`, PR into `main` with `Closes #<N>`; any dependent issue un-`blocked`.

## When you're stuck or unsure

- **A decision isn't in any spec** → it's an open question. Add/flag it in [`../specs/NEXT.md`](../specs/NEXT.md); don't guess.
- **The spec seems wrong or contradictory** → say so in the PR and tag the maintainer; don't silently work around it.
- **Precision matters** — this is still a spec-driven project. When proposing a change, name the doc and section.
</content>
</invoke>
