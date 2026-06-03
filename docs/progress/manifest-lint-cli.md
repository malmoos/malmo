# molma manifest lint CLI

- **Status:** done
- **Date:** 2026-06-03
- **Specs touched:** `docs/specs/NEXT.md` (past-tensed the lint reference under # Developer / app-author surface; the remaining "install --local" item stays open). No schema change — `APP_MANIFEST.md` and `APP_STORE.md` already describe the schema and the CI schema-lint step this backs.

Closes issue #7. App authors had no way to validate a `manifest.yml` short of opening a catalog PR and waiting on CI. This adds the author's inner-loop tool: a `molma` CLI with a single `manifest lint` subcommand that parses + validates a manifest against the schema and sanity-checks its sibling compose file, on a dev box with no running brain. The same checks are intended to back the catalog's CI schema-lint step (`APP_STORE.md` # CI on the repo), so one binary serves both the author loop and the CI gate.

## What was done

- **`cmd/molma`** (new) — the `molma` CLI skeleton, package `main`, single subcommand `molma manifest lint <path/to/manifest.yml>`. `lint(path)`:
  1. `os.ReadFile` the manifest and `manifest.Parse` it — schema validation (required fields, `manifest_version`, kebab-case slugs, permissions/folder rules) is delegated wholesale to the existing parser, so the CLI and the brain validate identically.
  2. Resolve `compose_file` relative to the manifest's directory, read it, and parse its service names; confirm `main_service` is one of them.
  Errors are author-actionable and name the problem; on success it prints `<path>: ok`. Usage errors (wrong/missing subcommand or args) print the usage line and exit **2** (Unix convention); a failed lint exits **1**; a clean lint exits **0**.
- **`internal/manifest`** — exported `ComposeServiceNames` (renamed from the unexported `composeServiceNames`, its one caller `Synthesize` updated). It now has a second consumer (the lint CLI), which is exactly when CLAUDE.md says exporting is warranted. The manifest package stays filesystem-free — `Parse` and `ComposeServiceNames` both take `[]byte`; the CLI owns all file I/O and path resolution.
- **Output discipline** — this is an interactive author CLI, so it prints to stdout/stderr (`fmt`) rather than emitting structured `slog`. The `log/slog`-only rule targets the brain daemon's diagnostics; a lint tool's output *is* its product. The package doc states this explicitly.

## How it maps to the specs

- `APP_MANIFEST.md` (schema) — lint is exactly `manifest.Parse`, the same validator the brain runs at install (`Parse` → `validate` → `validatePermissions`). No second schema implementation to drift.
- `APP_STORE.md` # CI on the repo — the issue's framing: "schema lint" is the first of the CI checks (admission, image-pullability, digest resolution, catalog regeneration follow). Building it as a binary gives the author loop and the CI gate from one tool. Admission + image-pullability stay out of scope here (catalog-CI concerns).
- `NEXT.md` # Developer / app-author surface — `cmd/molma` is the skeleton that item said #7 owns; the heavier `molma install --local` surface remains deferred there.
- `CLAUDE.md` # Go discipline — `internal/` only (no `pkg/`); export-on-second-consumer, not speculatively; tests in-package; no premature abstraction (no `manifest.Lint` library function — CI invokes the binary, so the orchestration lives in `cmd/molma`).

## Known gaps & deviations

- **Schema-only + compose sanity, by scope.** Lint does *not* run admission (ports/privileged/cap checks), image-pullability, or digest resolution — the issue puts those out of scope as catalog-CI concerns. Door-2's `internal/admission` is the runtime counterpart for pasted compose; this serves a different audience (authors) and artifact (a full `manifest.yml`).
- **No CI wiring in this repo.** The binary is the reusable unit; an actual GitHub Actions step that runs `molma manifest lint` over the catalog lands with the catalog-repo CI (`APP_STORE.md`), not here. `make build` is unchanged (it builds only the deployable host-agent + brain); the CLI builds via `go build ./cmd/molma` and is covered by `go vet ./...` / `go test ./...`.
- **Single subcommand only.** No `molma install --local`, no `--json` output, no globbing multiple manifests — all deferred (`NEXT.md`). The arg dispatch is a literal `manifest lint <path>` match, not a flag framework; that's enough for v1.

## Tests

`go test ./cmd/molma/ ./internal/manifest/` green (plus the full non-PAM suite — the rename rippled cleanly, `Synthesize` still passes). `cmd/molma/main_test.go`:

- **Real samples:** `lint` of both `catalog/whoami/manifest.yml` and `catalog/files-demo/manifest.yml` (the latter exercises the `permissions.folders` path) returns clean — the Done-when binaries.
- **Rejections** (each asserts the error *names* the problem): missing required field, bad slug (`Test_App` → "kebab-case"), unsupported `manifest_version`, missing compose file, compose with no services, and `main_service` absent from the compose.
- **Missing manifest file** → a "read manifest" error.
- **Dispatch:** valid `manifest lint <path>` succeeds; no-args / `manifest` only / `lint` without a path / unknown subcommand / extra trailing arg all return the usage sentinel.

## What's next

- **Catalog-repo CI step** that runs `molma manifest lint` over every app directory on PR (`APP_STORE.md` # CI on the repo) — the gate this binary was built to back.
- **`molma install --local`** and other dev subcommands (`NEXT.md` # Developer / app-author surface) — the heavier "run it on your own box" surface, still deferred.
