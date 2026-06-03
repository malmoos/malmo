# GET /api/v1/catalog/{id}/install-plan (slice 3 of install consent flow)

- **Status:** done
- **Date:** 2026-05-30
- **Specs touched:** docs/specs/BRAIN_UI_PROTOCOL.md, docs/specs/DASHBOARD.md

## What was done

Added a read-only `GET /api/v1/catalog/{id}/install-plan` endpoint (Pattern A sync) that returns everything the install-consent screen needs before the user confirms an install: declared permissions, role-derived scope options, and per-folder per-scope source menus.

Code changes:

- `internal/api/install_plan.go` — new file. DTO types: `InstallPlanDTO`, `InstallPlanPermissions`, `InstallPlanFolder`, `FolderSources`, `SourceMenu` (snake_case JSON tags; `subfolder_default` is omitempty). Pure builder `buildInstallPlan(man *manifest.Manifest, isAdmin bool) InstallPlanDTO` does all mapping with no I/O. Handler `installPlan` guards with `auth.FromContext` → 401, then maps `catalog.Load` errors: `catalog.ErrNotFound` → 404 (no log — a normal client outcome, like `getApp`); any other Load error (malformed manifest, missing compose) → 500 with a loud `slog.Error` (a curated-catalog integrity problem, not a missing app). Small unexported consts `sourceShared`/`sourcePersonal` for the source value strings. Helper `scopeMenu(isAdmin bool)` returns scope options and default: admin → (`["household","personal"]`, `"household"`), member → (`["personal"]`, `"personal"`).
- `internal/api/api.go` — registered `GET /api/v1/catalog/{id}/install-plan` (OperationID `get-install-plan`) in `register()`. Added a comment on `resolveOwnerScope` pointing at `scopeMenu` as the shared scope-authorization source so the read (menu) and write (enforcement) paths stay in sync.
- `internal/catalog/catalog.go` — new sentinel `catalog.ErrNotFound`, returned from `Load` only when the manifest dir/file is absent (`errors.Is(err, fs.ErrNotExist)` on the manifest read); parse failures and a missing compose file stay as plain wrapped errors. Lets the API discriminate 404 (missing) from 500 (malformed) instead of collapsing both. Same shape/rationale as `store.ErrNotFound`.
- `internal/api/install_plan_test.go` — new file. 8 table cases covering: unauthenticated → 401; unknown id → 404; **malformed manifest → 500** (guards the ErrNotFound discrimination); admin scope_options + default; member scope_options + default; no-folders manifest (flags false, empty devices/folders); folders-bearing manifest (mode/scope/subfolder_default + household/personal source menus); member still receives household source menu in folder rows (uniform shape). Fixtures are written into the harness's own catalog dir (`h.catalogDir`, exposed by `newHarness`) and read lazily by the live server — no post-listen mutation of the running server's catalog field.
- `web-ui/src/api.ts` — added `SourceMenu`, `FolderSources`, `InstallPlanFolder`, `InstallPlanPermissions`, `InstallPlan` TS types alongside existing `CatalogEntry`/`Instance`/`Job`. No view changes — the consent screen is slice 5.
- `docs/specs/BRAIN_UI_PROTOCOL.md` — added `GET /api/v1/catalog/:id/install-plan` to the Pattern A sync list and a full subsection documenting the response shape and its key properties.

## How it maps to the specs

Realizes the "read-only endpoint the consent screen calls before showing itself" from the slice 3 plan. The endpoint is Pattern A sync (BRAIN_UI_PROTOCOL.md): short read, no host call, no mutation. Auth guard and 404 shape follow the same conventions as `getApp`/`listApps`. The `scopeMenu` helper is the single source of truth for the install authorization table (DASHBOARD.md # install authorization) on the read path; `resolveOwnerScope` is the write-path mirror — a comment ties them together.

## Known gaps & deviations

**Option A per-scope source map chosen.** Each folder carries `sources.{household, personal}` — both menus always populated regardless of caller role (household unreachable for members but uniform shape simplifies UI). Alternatives considered: Option B (single flat `source_options` array, UI filters by elected scope) was rejected because it requires UI-side policy derivation. Option A was the approved choice per the plan.

**Structured fields only (no copy).** The brain returns `mode`/`scope`/`subfolder_default` and source fields; the UI owns all wording. This matches the rest of the brain's data-not-sentences convention.

**No audit.** Pure read — CLAUDE.md: "Pure reads … don't audit."

**`go build ./...` fails on the PAM cgo module** (unrelated to this slice — a pre-existing CGo/RTLD_NEXT issue in the dev environment). `go build ./internal/api/...` succeeds cleanly; `go test ./internal/api/... ./internal/catalog/... -race` passes (8 install-plan cases + the rest of the api suite).

**Code-review follow-up.** A post-implementation review raised four points; resolved as: (1) Load errors no longer collapse to 404 — `catalog.ErrNotFound` splits 404 (missing) from 500 (malformed/compose-missing); (2) the failure log moved from `slog.Info` to `slog.Error` and only fires on the 500 path (a 404 is a normal client outcome, logged nowhere, like `getApp`); (3) **declined** — the TS `scope_options: Scope[]` was flagged as looser than the Go `[]string`, but the narrower TS type is the better choice (gives the UI exhaustiveness checking; TS types never validate JSON at runtime regardless), and typing the Go side would ripple into `store`'s untyped scope consts for no contract gain; (4) the test no longer mutates the live server's catalog field — fixtures write into `h.catalogDir` and are read lazily (`go test -race` was already clean here because the HTTP round-trip synchronizes, but the pattern was fragile).

## What's next

- **Slice 4** — `writeOverride` + `writeEnv` enforce permissions: `user:`, folder bind mounts from elected source, `group_add` for shared, devices, GPU, `MOLMA_FOLDER_*` injection. Calls `ResolveHome` (slice 2). Receives the user's elections from `POST /api/v1/apps` `config`. Decide whether `GET /v1/identity/well-known` belongs here or can use hard-coded defaults.
- **Slice 5** — consent + config UI in `web-ui/src/views/StoreView.vue` consuming `InstallPlan` from this slice.
