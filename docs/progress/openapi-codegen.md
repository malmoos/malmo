# OpenAPI codegen pipeline

- **Status:** done
- **Date:** 2026-06-03
- **Specs touched:** `docs/specs/BRAIN_UI_PROTOCOL.md` (# API discipline → # Codegen + the locked-decisions bullet: past-tensed the client-side split now that TS-type generation landed; recorded the reproducible server-less emission + freshness gate). No `DECISIONS.md`/`NEXT.md` change — the codegen direction was already locked (`DECISIONS.md` 2026-05-15) and the stale NEXT timing item was already removed.

Closes issue #52. The brain↔UI API is malmo's public surface, but the Go server types and the browser client types were maintained by hand on both sides — `web-ui/src/api.ts` carried ~15 hand-rolled wire interfaces, drifting silently on every brain DTO change. This lands the codegen pipeline the schema was always meant to feed: a reproducible, server-less OpenAPI emitter; a committed spec artifact with a CI freshness gate; and a generated TypeScript client that replaces the hand-rolled interfaces.

## What was done

**Backend — reproducible, server-less spec emission.**
- `internal/api`: extracted `registerAll(api huma.API)` as the single source of the REST surface, shared by the live `Handler()` and a new exported **`OpenAPIDocument() *huma.OpenAPI`** that registers every route on a throwaway mux against a zero-value `Server` and returns the document. No server, no port, no live dependencies — `huma.Register` only reflects the typed request/response structs, so handlers never run. The title/version are shared constants so the live server and the emitted spec describe the same API.
- `cmd/openapi-gen` (new): a tiny entrypoint (`-o <dir>`) that serializes the document to `api/openapi.json` (indented, trailing newline) and `api/openapi.yaml`. huma marshals with stable field ordering and Go sorts map keys, so the output is **byte-stable across runs** — which is what makes the freshness check meaningful (verified: two runs diff clean).
- `make openapi` now runs the emitter (was: `curl` a running brain into an untracked root `openapi.json`). `api/openapi.{json,yaml}` are committed (25 paths, 38 schemas).

**Backend — freshness gate.**
- `make openapi-check` re-emits to a scratch dir and diffs against the committed copy, failing if stale (mirrors `fmt-check`). Wired into `make check` and a new **OpenAPI spec freshness** step in `ci-go.yml`; `api/**` added to the Go-CI path filter so a hand-edit to the spec also trips it. Verified both directions (passes fresh, fails on a tampered title).

**Frontend — generated TS client.**
- Added **`openapi-typescript@7.13.0`** (dev dep, pinned exact). `npm run gen:api` generates `web-ui/src/generated/openapi.ts` from `../api/openapi.json` (committed).
- `web-ui/src/api.ts`: the hand-rolled wire interfaces are gone — `User`, `Instance`, `Notification`, `Job`, `InstallPlan`, … are now aliases of `components["schemas"][…]` under the **same export names**, so all 34 `api.*` call sites and ~11 importing files are unchanged. The hand-written `fetch` wrapper (`ApiError` mapping + `/api/v1` prefix + 401 handler + `waitForJob`) **stays**.
- `make check-web` and `ci-web.yml` regenerate the client and fail if the committed copy is stale (the frontend analogue of `openapi-check`); `api/**` added to the web-CI path filter.

**Supply-chain handling (per the May 2026 npm-compromise caution).** `openapi-typescript@7.13.0` (Feb 2026) is clean, but its transitive ranges floated to three packages published **2026-05-25** (inside the compromise window): `@redocly/openapi-core@1.34.15`, `@babel/code-frame@7.29.7`, `@babel/helper-validator-identifier@7.29.7`. npm `overrides` pin them to the latest **pre-May** releases — `@redocly/openapi-core@1.34.14` (exact-pinned subtree), `@babel/code-frame@7.29.0`, `@babel/helper-validator-identifier@7.28.5` — so the entire added closure predates the window (a pre-May release can't be affected by a May compromise). Verified: a full sweep of every added lockfile package shows **zero** May-2026-or-later publishes; `npm audit signatures` passes (137 signatures, 45 attestations). All installs ran `--ignore-scripts`. The `overrides` rationale is captured in a `comment:overrides` key in `package.json`.

## How it maps to the specs

- `BRAIN_UI_PROTOCOL.md` # Codegen — "Split. Server-side from day one; client-side deferred." Server-side was already realized via huma; this lands the **client-side** half (TS *types*) and makes server-side emission reproducible + committed. Honors the locked nuance that the wrapper was "designed to be swapped for `openapi-fetch`" by deferring the *client* swap while adopting generated *types*.
- `BRAIN_UI_PROTOCOL.md` # CI enforcement — step 1 of the future `oasdiff breaking` check is "build the brain and write `openapi.json` (a `make openapi` target or a tiny Go binary that calls `api.OpenAPI()`)." That binary now exists (`cmd/openapi-gen` → `api.OpenAPIDocument()`); the committed artifact + freshness gate are its prerequisites. `oasdiff` itself stays future work (out of scope here).
- `DECISIONS.md` 2026-05-15 (huma + generated OpenAPI) — no change; this realizes it.
- `CLAUDE.md` # Go discipline — `internal/` only; export-on-second-consumer (`OpenAPIDocument` joins `Handler` as the second consumer of the route registrations, which is why `registerAll` was extracted); a CLI's output is `fmt`, not `slog`.

## Known gaps & deviations

- **`openapi-fetch` deferred (deliberate).** The issue lists `openapi-fetch` as a new dep, but it also mandates "keep the existing `ApiError` mapping" and "types + tooling only, no behavior changes." `openapi-fetch`'s `{data, error}` (no-throw) client would require rewriting all 34 call sites' error handling — forbidden by "types + tooling only" — and adding it unused is dead supply-chain surface. So only `openapi-typescript` (the type generator the wrapper was designed around) was added; the typed-client swap is a clean follow-up.
- **Filed DTO mismatches (don't-fix-here, per the issue).** huma emits several semantically-enum fields as plain `string` (`scope`, `severity`, `status`, `state`, `mode`, folder `scope`) because the Go structs don't declare huma `enum` tags, and nil-able Go slices as `T[] | null` (`permissions.folders/devices`, `SourceMenu.options`, the list-wrapper arrays). The generated types are therefore *more accurate* than the hand-rolled ones were. Adopted as-is: `Scope` is kept as a UI-side literal union (the dashboard controls it and indexes `FolderSources` by it), and `InstallDialog.vue` normalizes the three nullable arrays it consumes via a `computed`/helper. **Follow-up for the Go side:** declare huma `enum`s on those fields (so the schema — and `oasdiff` — capture the closed sets) and consider non-nil slice initialization; not done here to avoid changing the wire contract in a types-only PR.
- **SSE types stay hand-maintained.** `/api/v1/events` and `/api/v1/system/live` are registered raw (they bypass huma for curl-debuggability), so their event payloads aren't in the spec and aren't generated. A typed-SSE story is a separate follow-up.
- **`oasdiff` breaking-change CI not wired.** This PR lands the spec artifact + a *freshness* gate (committed spec matches the code); the *breaking-change* diff (`oasdiff breaking`) described in # CI enforcement is still future work.

## Tests

- Backend: emitter determinism (two runs byte-identical, JSON + YAML); `make openapi-check` verified to pass on a fresh tree and fail on a tampered spec; `go vet` + `go test ./internal/api/` green (the `registerAll` refactor rippled cleanly); `gofmt` clean.
- Frontend: `npm run gen:api` output matches the committed client; `npm run build` (`vue-tsc --noEmit` typecheck + `vite build`) green — the type swap + `InstallDialog` null-guards compile against every consumer; `npm ci` confirms lockfile sync.
- Supply-chain: full lockfile sweep (zero May-2026+ packages after overrides); `npm audit signatures` passes.

## What's next

- **`oasdiff breaking` CI** — the additive-minor breaking-change gate (`BRAIN_UI_PROTOCOL.md` # CI enforcement) now has its prerequisite (committed spec + emitter); wire `oasdiff breaking origin/main HEAD` on PRs.
- **Typed `openapi-fetch` client** — swap the hand-written wrapper for the generated typed client, migrating call sites off `api.get<T>()` to path-typed calls (the deferred half of this issue).
- **Go-side enum + non-nil-slice tags** — declare huma `enum`s on the string-widened fields and initialize slices, so the schema captures the closed sets and arrays stop being nullable.
- **Typed SSE** — bring the event payloads into a typed surface (separate from the huma REST spec).
