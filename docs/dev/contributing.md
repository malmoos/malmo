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

The **boot / storage / LUKS+TPM track** is actively in flight ([`../progress/luks-tpm-enrollment.md`](../progress/luks-tpm-enrollment.md)). Stay out of: `dev/test-qemu/**`, `cmd/malmo-storage-verify/`, `internal/storageverify/`, `dist/systemd/**`, and the `BOOT.md` / `STORAGE.md` specs. Coordinate with the maintainer before starting anything in that area.

## Step 2 — Branch off `main`

Always work on a branch; **never commit to `main`**. Every change lands via a PR into `main`.

```bash
git checkout main && git pull            # always start from a fresh main
git checkout -b <area>/<N>-<short-slug>  # e.g. feat/12-health-banners, fix/8-login-lockout
```

Branch-name shape: `feat/…`, `fix/…`, `test/…`, `docs/…` + the issue number + a short kebab slug (≤4 words, no filler like "add" or "implement"). Including the issue number makes in-flight work visible in `git branch -a` without opening GitHub. One issue per branch — always.

**One PR per issue.** If a task feels too big, split the issue first (file child issues, link them with `Depends on #<N>`, add the `blocked` label to dependents) — then each issue gets its own focused PR. Don't split a single issue across multiple PRs.

## Step 3 — Build it

The inner dev loop is all native, no VM — see [`running-locally.md`](running-locally.md) (`make dev` runs agent + brain + UI together). Hold to the conventions already in the tree:

- **Go discipline** — CLAUDE.md # Go code discipline (consumer-side interfaces, layer boundaries, `log/slog` only, standard structured field names, typed errors only at boundaries, no premature abstraction). Match the surrounding code.
- **Don't build host-integrated subsystems without the VM outer loop** the specs assume (CLAUDE.md # Repo state). The fake host-agent (`cmd/host-agent`) is the inner-loop stand-in.
- **Keep specs and reality in sync** — if your implementation realizes or *diverges* from a spec, update the matching `docs/specs/` doc in the same change, and add a `DECISIONS.md` entry if a locked decision flips.
- **Scope is exactly the issue, nothing more.** If you encounter something broken or improvable outside the issue scope: search for an existing issue (open or closed) first; if none exists, open one. Never implement it in the current PR. Maintainers decide what gets built and when — that is a product decision, not a developer one.
- **New dependencies must be justified.** Any new `go.mod`/`go.sum` or `package.json`/`package-lock.json` entry must be called out in the PR body with the reason. When in doubt, don't add the dependency.
- **DB schema changes are additive-only.** Never `DROP COLUMN`, `RENAME COLUMN`, or write a destructive `ALTER TABLE`. New columns on existing tables must have a `DEFAULT` or be nullable so the migration runs safely against a live database.
- **Never swallow errors.** `_ = err` or a missing `if err != nil` is almost always wrong. Errors must be returned, logged, or explicitly handled — never silently dropped.
- **User-facing errors must be plain English.** Raw Go error strings must not reach API responses or the dashboard UI. Translate at the API boundary; internal detail goes to `slog` only.

## Step 4 — Test it

Every behavioral change ships with tests. Which layer depends on what you touched — see [`testing-brain.md`](testing-brain.md) for the brain pyramid (unit → store → lifecycle-with-fakes → API → integration → e2e) and [`../specs/TESTING.md`](../specs/TESTING.md) for the boot-level lanes (nspawn fast / QEMU medium / soak).

Before you push, run the gate:

```bash
make check          # the pre-PR gate: gofmt-clean + go vet + full test suite
make check-web      # additionally, for any frontend change: web-ui typecheck + build
```

`make check` is the one command that mirrors CI's Go job and the [definition of done](#definition-of-done--checklist) below — formatting, vet, and the full test suite in one pass (fails fast on the cheap checks first). Run `make fmt` to auto-fix formatting if `check` flags it. The full suite needs `libpam0g-dev` (see [`running-locally.md`](running-locally.md)); without the headers, fall back to the narrower targets:

```bash
make fmt-check && make vet    # the non-test gates, no PAM headers needed
make test-nopam               # skip the PAM-cgo target
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

## Step 6 — Review it

Before opening the PR, run a self-contained code review using a fresh agent. The agent receives only the diff and your progress document — no conversation history — so it has no attachment to the implementation choices you made.

In Claude Code, run:

```
/code-review low Read docs/progress/<your-slug>.md first for context, then review the diff per docs/dev/code-review.md.
```

`/code-review low` spawns a fresh subagent with no access to your conversation history, which is what makes it impartial. The `low` effort level limits output to high-confidence findings — fast and cheap. Use `high` or `max` for a particularly complex or risky slice.

Address every **Block** finding before opening the PR. If you disagree with a finding, note it in the progress entry's "Known gaps" section — never silently ignore it.

## Step 7 — Open the PR

```bash
git push -u origin <your-branch>
gh pr create --base main --fill        # then flesh out the body
```

PR body must include **`Closes #<N>`** — do not delete this line. It is the only thing that tells GitHub to auto-close the linked issue when the PR merges; without it the issue stays open and the board goes stale. Replace `<N>` with the issue number. Also include: the spec(s) touched, what you tested (and against what — real Docker? a VM boot?), and any known gaps. If your work unblocks a dependent issue, drop its `blocked` label (`gh issue edit <N> --remove-label blocked`).

**Don't merge your own PR** unless the maintainer has said to. PRs into `main` get a review pass first; merging closes the linked issue automatically.

## Definition of done — checklist

- [ ] Behavior works in the inner loop (`make dev`), and integration-tested against the real system if it touches one.
- [ ] Tests added at the right layer; `make check` green (frontend changes: `make check-web` too).
- [ ] Progress entry written (`docs/progress/NNNN-…`); indexes in both READMEs updated; Up-next re-ordered if needed.
- [ ] Spec doc updated if behavior realized/diverged; `DECISIONS.md` entry if a locked decision flipped.
- [ ] No `§`, no hard-wrapped markdown, `log/slog` only, conventions per CLAUDE.md.
- [ ] No out-of-scope changes; any out-of-scope finding has an existing or new issue filed, not a fix in this PR.
- [ ] No new dependencies added silently; any new `go.mod`/`package.json` entry called out in the PR body with justification.
- [ ] Schema changes additive-only; no dropped or renamed columns.
- [ ] No swallowed errors (`_ = err`); user-facing error messages are plain English, not raw Go error strings.
- [ ] Commits are logical units with "why" messages; no micro-commit or WIP history.
- [ ] Self-review run (`/code-review low` with progress doc as context per [`code-review.md`](code-review.md)); all Block findings addressed, disagreements noted in Known gaps.
- [ ] Branched off a fresh `main` (`git checkout main && git pull` before branching); branch named `<area>/<N>-<short-slug>`.
- [ ] One PR closes exactly one issue; if scope grew, the issue was split first.
- [ ] PR into `main` with `Closes #<N>` (do not delete — GitHub won't auto-close the issue otherwise); any dependent issue un-`blocked`.

## When you're stuck or unsure

- **A decision isn't in any spec** → it's an open question. Add/flag it in [`../specs/NEXT.md`](../specs/NEXT.md); don't guess.
- **The spec seems wrong or contradictory** → say so in the PR and tag the maintainer; don't silently work around it.
- **Precision matters** — this is still a spec-driven project. When proposing a change, name the doc and section.
</content>
</invoke>
