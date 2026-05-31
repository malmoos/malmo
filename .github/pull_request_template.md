<!-- See docs/dev/contributing.md # Step 7 + Definition of done. Fill every section; delete the comments. -->
<!-- Branch should be named <area>/<N>-<short-slug> (e.g. feat/12-health-banners). One PR closes exactly one issue. -->

<!-- ⚠️ REQUIRED: keep the Closes line below — do not delete it. Without it GitHub will NOT auto-close the issue on merge and it will remain open. Replace <N> with the issue number. -->
Closes #<N>

## What & why

<!-- One or two lines: what this slice does and the behavior change. -->

## Spec(s) touched

<!-- Which docs/specs/*.md this realizes or diverges from. "None" if pure tooling/infra. Add a DECISIONS.md entry if a locked decision flipped. -->

## What was tested

<!-- How, and against what. Be concrete: `make check` green? real Docker? a VM boot (nspawn / QEMU+swtpm)? Unit tests alone are not enough for slices that integrate with a real external system. -->

## Known gaps & deviations

<!-- Be honest. Every "handled" claim must be verifiable in the diff — don't assume symmetry between similar code paths. "None" if truly none. -->

## Definition of done

- [ ] Behavior works in the inner loop (`make dev`), and integration-tested against the real system if it touches one.
- [ ] Tests added at the right layer; `make check` green (frontend changes: `make check-web` too).
- [ ] Progress entry written (`docs/progress/NNNN-…`); indexes in both READMEs updated; Up-next re-ordered if needed.
- [ ] Spec doc updated if behavior realized/diverged; `DECISIONS.md` entry if a locked decision flipped.
- [ ] No section-sign symbol (write `#` instead), no hard-wrapped markdown, `log/slog` only, conventions per `CLAUDE.md`.
- [ ] Branch off `main`, PR into `main` with `Closes #<N>` (do not delete this line — GitHub will not auto-close the issue otherwise); any dependent issue un-`blocked`.
