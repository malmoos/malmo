# malmo manifest check CLI

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** `docs/specs/NEXT.md` (noted `manifest check` shipped under # Developer / app-author surface, and pinned the deferred `malmo catalog scaffold` item with its build trigger). `docs/dev/authoring-apps-with-an-agent.md` (the agent-authoring prompt now calls `check` instead of hand-eyeballing `admission.go`). No schema change.

Follow-up to the lint CLI (`manifest-lint-cli.md`) and the resolve CLI (`catalog-image-footprint.md`). `manifest lint` validates the schema but deliberately does NOT run admission, so the agent-authoring guide had the author re-check admission by hand — read all of `internal/admission/admission.go` and eyeball the compose against its rules. That manual step is the error-prone one (it produces a manifest that lints green then fails at install) and it re-derives in the model, every run, a check that already exists as a callable Go function. This adds `malmo manifest check`: lint + the compose admission policy in one pass, so a single green `check` is the author's "would this actually install?" bar.

## What was done

- **`cmd/malmo/check.go`** (new) — `check(ctx, admit, manifestPath)`: runs `lint` (schema + sibling-compose validation), then re-reads the verbatim compose bytes and runs them through the admission policy. Either failure aborts with the underlying error, which already names the field/slug (lint) or the offending service + field (admission).
- **`cmd/malmo/main.go`** — new `manifest check <path>` dispatch case wiring the real `admission.Check`; usage line and the package doc comment now list all three subcommands (lint = schema only, check = schema + admission, resolve = mutates).
- **The admission seam.** `check` takes the admission step as a `composeChecker` func param, mirroring `resolve`'s `imageSizer`. Production passes `admission.Check` (syntax via `docker compose config -q` + structure); tests pass `admission.CheckStructure` (the daemon-free path admission already exposes) so the unit tests stay hermetic.
- **Agent-authoring guide rewritten to use `check`** (`docs/dev/authoring-apps-with-an-agent.md`) — the prompt no longer tells the agent to pre-read `admission.go`/`manifest.go` to learn the rules (drops ~550 lines of source from each run's context); step 10's two-validator split collapses to one `check` command, leaving only the semantic checks `check` can't make (main_port is internal, every `${MALMO_SERVICE_*}` has a `services:` entry, every touched folder has a `permissions.folders` grant) as a manual checklist.

## How it maps to the specs

- `APP_LIFECYCLE.md` # admission policy — `check`'s admission half is exactly `admission.Check`, the same policy the brain enforces at install and catalog CI enforces at publish. No second implementation to drift.
- `APP_MANIFEST.md` (schema) — `check`'s lint half is `manifest.Parse`, unchanged. `check` is non-strict on unknown fields for the same reason lint is — the storage/services blocks still need an eyeball against the spec.
- `NEXT.md` # Developer / app-author surface — `check` joins lint/resolve as shipped; the heavier `malmo install --local` surface stays deferred, and the new `malmo catalog scaffold` item is pinned with its trigger (revisit after ~10 hand-authored apps; build only against observed rewrite patterns).
- `CLAUDE.md` # Go discipline — the admission seam follows the export-on-second-consumer / consumer-side-interface pattern resolve already established; no new abstraction beyond the one func type the test needs.

## Known gaps & deviations

- **`check` needs the Docker daemon** (its admission half shells to `docker compose config -q`), same as `resolve`. The hermetic unit tests use `admission.CheckStructure` via the seam; the daemon-backed path is covered by running `check` on a real sample, not in the unit suite. Consequently there is no "valid check" success row in the dispatch test — the same reason there's no "valid resolve" row.
- **No CI wiring here.** Like lint/resolve, the binary is the reusable unit; the catalog-repo CI step that runs `check` over every app on PR lands with the catalog-repo CI (`APP_STORE.md`), not in this repo.
- **`lint` stays.** It remains the schema-only subcommand for the catalog CI schema-lint step; `check` is the author-facing superset. Both share the same `lint()` function, so there is no schema drift between them.

## Tests

`go test ./cmd/malmo/...` green. `cmd/malmo/check_test.go` (all via the daemon-free `admission.CheckStructure` seam):

- **Real samples:** `check` of both `catalog/whoami` and `catalog/files-demo` returns clean.
- **Runs admission, not just schema:** a schema-valid manifest whose compose declares host `ports:` / a named volume / `privileged: true` fails, and the error names the admission problem.
- **Runs lint first:** a bad slug fails with the schema error (kebab-case) before admission runs.
- **Dispatch:** `manifest check` without a path returns the usage sentinel (the success path needs the daemon, so it's exercised by running `check` on a sample, not in the dispatch table).

## What's next

- **Catalog-repo CI step** that runs `malmo manifest check` (admission + schema) over every app directory on PR (`APP_STORE.md` # CI on the repo).
- **`malmo catalog scaffold --compose <path>`** — the deterministic Phase-2 rewrite, deferred until ~10 apps are hand-authored against the new `check`-based loop so it's built on observed rewrite patterns (`NEXT.md` # Developer / app-author surface).
