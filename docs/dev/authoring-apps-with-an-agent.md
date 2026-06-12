# Authoring catalog apps with an agent

A reusable agent prompt that turns an upstream `docker-compose.yml` (or a GitHub repo) into a molma **Door-1** catalog app: it rewrites the compose to pass [admission](../specs/APP_LIFECYCLE.md), rewires env vars to molma's injected values, resolves image digests, and writes the `catalog/<id>/{manifest.yml, compose.yml}` pair. This is the author-side adaptation the app's developer would do to run on molma — the tool we use to grow the catalog.

## How to use it

1. Open a fresh agent session **inside the molma repo** (the prompt reads on-disk sources — it does not rely on its own text being correct).
2. Paste the prompt block below, then append the **inputs**: the app name, a pasted compose **or** a GitHub repo URL, and optionally a docs URL.
3. Let it work, then **read its report and re-run `manifest check` yourself** before committing. The agent's job ends at a passing `manifest check` (schema + admission in one); the PR (`Closes #<N>`, progress entry if it's a slice) is yours.

One app per run. For a batch, run it once per app and review each `catalog/<id>/` pair on its own.

## What the validator actually covers (read before trusting "it passed")

**`go run ./cmd/molma manifest check <path>`** runs both checks in one pass — schema lint AND the compose admission policy — so a single green `check` is the bar:

- **Schema** (`internal/manifest/manifest.go`): required fields, `manifest_version == 1`, kebab-case slugs, the `permissions` block (folder names / `mode` / `scope` / `default` / `target`), `health_probe` shape, that `compose_file` resolves + parses, and that `main_service` is one of the compose's services.
- **Admission** (`internal/admission/admission.go`): syntax via `docker compose config -q`, then the structural compose rejections (ports, named volumes, absolute binds, `privileged`, `cap_add`, `build:`, `extends:`, host namespaces). The agent no longer hand-eyeballs these or reads `admission.go` — `check` enforces them and names the offending service + field.

(`manifest lint` still exists for the catalog CI schema-only step, but authors should run `check` — `lint` does NOT run admission.)

One consequence worth internalizing:

- **Unknown manifest fields are silently accepted.** `manifest.Parse` uses non-strict YAML, so `storage:` (except `estimated_size`, now read into the app footprint), `services:`, `resources:`, `categories:`, `author:`, `license:` are not in the Go struct yet and are ignored by `check` — they neither fail nor get validated. Write them anyway: the manifest is the durable, author-grade artifact and `APP_MANIFEST.md` is the source of truth. Following the full spec schema is forward-compatible; skipping it loses information the catalog will eventually consume. A green `check` therefore proves the fields it knows, not the storage/services blocks — those you still verify against the spec by eye.

The on-disk examples (`catalog/whoami/`, `catalog/files-demo/`) are intentionally minimal skeletons. For the full author-grade schema (storage split, managed services, resources, description), the worked reference is the **"Complete sample manifest"** (PhotoPrism) in `APP_MANIFEST.md`.

## The prompt

```text
Adapt an upstream app into a molma Door-1 catalog app (molma = home-server OS, Umbrel/CasaOS category). Produce a `catalog/<id>/{manifest.yml, compose.yml}` pair that installs cleanly — the author-side adaptation the app's developer would do to run on molma.

You run inside the molma repo. Read these on-disk sources; don't rely on this prompt alone — if it disagrees with the code, the code wins:
- `docs/specs/APP_MANIFEST.md` — manifest schema (fields, injection conventions, folder model). The "Complete sample manifest" section is the full author-grade shape.
- `catalog/whoami/`, `catalog/files-demo/` — worked examples of the target output (minimal skeletons; the full schema lives in the spec).
Do NOT pre-read `internal/admission/admission.go` or `internal/manifest/manifest.go` to learn the rules — `go run ./cmd/molma manifest check` enforces both the schema and the admission policy and names the exact field at fault. Draft, then run `check` and iterate on its messages (note: `check` is non-strict on unknown manifest fields — a green run does NOT prove the storage/services blocks are right, only the fields it knows). Consult `manifest.go` only if a `check` message is unclear.

INPUTS (appended below): app name; a pasted compose OR a GitHub repo URL; optionally a docs URL.

ADAPT, DON'T FORCE (read first — this governs every step below). Your job is to adapt apps that *can* run as a molma Tier-3 Door-1 app, not to make every app pass at any cost. The admission rules in `admission.go` exist because molma's architecture genuinely cannot run what they reject — stripping a directive the app actually depends on produces a manifest that lints green and then fails or misbehaves at install. That is worse than no manifest. So:
- The legal adaptations are ONLY the ones documented in steps 3-6 (drop host port mappings, named volumes -> binds, content paths -> folder grants, credentials -> managed services). Anything beyond that is forcing.
- If making the app pass would require removing or faking something the app genuinely needs to function, STOP. Do not invent flags, do not strip a required capability and hope, do not downgrade the app to a broken subset. Write no files.
- When you bail, say so plainly in the report: name the exact blocker (the directive, capability, or assumption), point to the rule in `admission.go` or the spec that forbids it, explain why molma's architecture can't satisfy it, and state whether it's a Tier-2 candidate (needs capabilities/host access) or simply unsupportable in v1. A clear "can't, because X" is a successful run — a fabricated pass is a failed one.

Concrete bail triggers (non-exhaustive — when unsure whether an adaptation is legal or forcing, treat it as forcing and bail):
- Needs `privileged`, `cap_add`, `network_mode: host`, `pid/ipc/userns_mode: host`, or device/kernel access beyond a declarable `devices:`/`gpu:` grant -> Tier-2 candidate, not Door-1.
- Needs a fixed *host* port (not just an internal listen port molma can route to) — e.g. a VPN/DHCP/mDNS app that must own a specific host port or the host network.
- Ships only a `build:` with no published image, or requires `extends:` of a file you don't have.
- Requires multiple replicas, swarm/k8s constructs, or an orchestrator molma's single-node `docker compose` driver doesn't run.
- Insists on owning content in a way incompatible with the folder model AND can't fall back to `app_managed_user_content` cleanly.
- Depends on a host-level service molma doesn't provide and won't add for one app.

STEPS

1. GATHER. Given a repo URL, do NOT clone — read just the files you need via `gh`:
   - Locate them in one call: `gh api repos/<owner>/<repo>/git/trees/HEAD?recursive=1 --jq '.tree[].path'`. Find the canonical compose (`docker-compose.yml`/`compose.yaml`, often at root or under `docs/`/`examples/`), `.env.example`, and README/install docs.
   - Read each raw: `gh api repos/<owner>/<repo>/contents/<path> -H "Accept: application/vnd.github.raw"`.
   Extract: image + version tag; the port the app listens on *inside* the container; the env-var names for its data dir, content/library path, and DB/cache credentials; which volumes are real data vs regenerable cache; whether the app supports pointing its library at an external path (if not, note it for step 6's app_managed_user_content fallback). Given only a pasted compose, use it (+ docs URL if provided).
   - **Icon**: Search the repo tree for common icon filenames (`icon.svg`, `icon.png`, `logo.svg`, `logo.png`, `app-icon.svg`, `app-icon.png`) anywhere under `assets/`, `docs/`, `public/`, or root. Download the first match: `gh api repos/<owner>/<repo>/contents/<path> -H "Accept: application/vnd.github.raw" --output catalog/<id>/icon.<ext>`. If nothing found in the repo, skip — do not fall back to external sources.
   - **Screenshots**: Parse the README for embedded images (`![...](...)` markdown). Download any image whose URL ends in a recognized extension (`.png`, `.jpg`, `.jpeg`, `.gif`, `.webp`): repo-relative paths via `gh api repos/<owner>/<repo>/contents/<path> -H "Accept: application/vnd.github.raw" --output ...`, external direct-image URLs (CDN, GitHub issue attachments, raw.githubusercontent.com, etc.) via `curl -sL`. Skip anything that requires crawling — page URLs, YouTube/Loom embeds, badge URLs, or URLs with no image extension. Number the downloaded files `01`, `02`, etc., preserving the source extension.
   - **GO / NO-GO GATE (do this before any rewriting).** With the compose and docs in hand, run the bail triggers from ADAPT, DON'T FORCE against what you found. If any apply, STOP NOW — do not start step 2, do not write files. Skip straight to step 11 and report the blocker. This gate exists so you discover a dealbreaker before investing in the rewrite, not after.

2. IDENTITY & RUNTIME. Set `id`, `name`, `version` (the app's real version — never `custom`, which is the Door-2 marker), `main_service` (the service that is "the app"), `main_port` (its *internal* listen port, NOT a host-side mapping), `preferred_slugs`. Write `description.short`: a single punchy sentence that captures what the app *does for the user* — written fresh, not lifted from the README. Optionally write `description.long`: a short markdown paragraph (3–5 sentences) that expands on the value proposition — what problems it solves, what makes it worth running. Read the README for facts, but write both fields in your own words; do not copy README prose, badges, install steps, or Docker-specific context. Do NOT add a `multi_user` field — household-vs-personal is the installer's runtime choice, not a manifest property (APP_MANIFEST.md # G).

3. REWRITE THE COMPOSE TO PASS ADMISSION (verify the result with `manifest check`, step 10):
   - Drop every `ports:` mapping. From a mapping like `8080:80`, mine the container side (`80`) for `main_port`.
   - Convert named volumes to relative binds under `./data/` (e.g. `db_data:/var/lib/postgresql/data` -> `./data/db:/var/lib/postgresql/data`).
   - Drop absolute host bind paths. If one was *user content* (a media library), don't bind it — grant it via `permissions.folders` (step 5) and point the app at `${MOLMA_FOLDER_<NAME>}`.
   - Drop `privileged`, `cap_add`, `build:`, `extends:`, `network_mode`, `pid/ipc/userns_mode: host`. These are not adaptable: if the app genuinely needs any of them it is not a Tier-3 Door-1 app, so STOP per ADAPT, DON'T FORCE — report the blocker and write no files; do not fabricate a passing compose by stripping a directive the app depends on.
   - Keep `image:` as readable version tags; digests go in the manifest `images:` map, not the compose.
   - Prefer the app listen on a non-privileged port (>=1024). Tier-3 apps run as a non-root elected UID under `cap_drop: ALL`, so an app that only listens on :80 may need a flag/env to move (see files-demo's `--port=8080`).

4. REWIRE ENV VARS — molma injects values; the compose maps them to the app's own names. The injected names use the MOLMA_ prefix (the project was renamed from "malmo"; never emit MALMO_):
   - Content/library path -> `${MOLMA_FOLDER_<NAME>}` (+ a `permissions.folders` entry). E.g. `PHOTOPRISM_ORIGINALS_PATH: ${MOLMA_FOLDER_PHOTOS}`. The brain bind-mounts each declared folder at `/molma/<folder>` and injects its absolute path as `MOLMA_FOLDER_<NAME>`.
   - App state dir -> a `./data/...` bind (use `${MOLMA_DATA_DIR}` if the app needs an absolute path).
   - DB/cache credentials -> `${MOLMA_SERVICE_<NAME>_*}` + a `services:` block — only when the app maps cleanly to managed Postgres/Redis. If it ships its own DB, keep that (adapted to binds) instead.

5. PERMISSIONS (least privilege). `internet`/`lan` only if actually used; `folders` with `mode` (default `read`; `write` is deliberate) and `scope` (`whole` | `pick-subfolder` + optional `default`); `devices` for `/dev/...`; `gpu: true` for hardware accel. Do NOT set a Door-2 `target:` on a folder — that field is for synthetic Door-2 manifests only.

6. STORAGE. Split binds into `storage.data_volumes` (back up: indexes, configs, app DB) and `storage.cache_volumes` (regenerable: thumbnails, transcodes). `estimated_size` is **measured, not guessed** — it's the app-state baseline at install, not a usage projection (`APP_MANIFEST.md` # Storage, `DECISIONS.md` 2026-06-09). You already boot the app for the smoke test (step 10c / verification); at the moment the health probe first passes, `du -sb` the data bind and record that as `estimated_size`. Round up modestly; aim close to the real figure, and undercounting (a first-boot download still in flight) is acceptable. Omit it if you don't boot the app. Add `tier` if justified. If the app has NO external-library support (it insists on owning its content internally), set `storage.app_managed_user_content: true` instead of a `folders` grant, and say so in the report — it forfeits the "files first-class" badge.

7. HEALTH PROBE (recommended for real web apps). If the app exposes a cheap HTTP health/ready path, declare `health_probe` (e.g. `health_probe: /healthz`, or the full mapping with `healthy_status`/`start_period`). Omit it for apps with no such endpoint — omitting just means the app is never probed.

8. IMAGE DIGESTS + SIZES. The `images:` map is object form — each `image:tag` maps to `{digest, download_bytes, disk_bytes}` (`APP_STORE.md` # Catalog schema). Don't hand-write these: once the files exist (step 9), run `go run ./cmd/molma manifest resolve catalog/<id>/manifest.yml`, which pulls each compose image via the Docker daemon and fills the map from the registry — pinned digest plus compressed/uncompressed sizes — keyed by the exact compose string, then prints the per-app footprint. It rewrites the `images:` block in place (preserving your comments) and fails loudly rather than writing a bogus zero. Never guess a digest; if the registry is unreachable the command errors — omit `images:` (valid — brain does trust-on-first-use) and say so.

9. WRITE `catalog/<id>/manifest.yml`, `catalog/<id>/compose.yml` (`compose_file: compose.yml`), `catalog/<id>/icon.<ext>` (from step 1), and `catalog/<id>/screenshots/NN.<ext>` (from step 1, if any were downloaded).

10. VALIDATE & ITERATE:
   (a) `go run ./cmd/molma manifest check catalog/<id>/manifest.yml` — runs the schema lint AND the compose admission policy in one pass (slugs, permissions, health_probe shape, compose-exists/parses, `main_service` present, and the structural rejections: ports, named volumes, absolute binds, privileged, cap_add, build, extends, host namespaces). Iterate until it passes. It is non-strict on unknown fields, so it will NOT flag a malformed `storage:`/`services:` block — those you verify against the spec by eye.
   (b) Semantic checks `check` can't make — confirm by hand: that `main_port` is the internal listen port (not a host mapping), that every `${MOLMA_SERVICE_*}` has a matching declared-and-used `services:` entry, and that every folder the app touches has a `permissions.folders` entry.

11. REPORT: what you changed and why; env vars rewired; permissions + reasoning; data-vs-cache split; digest status; health-probe choice; whether it's files-first-class or app_managed_user_content; icon found or skipped; screenshot count or skipped; anything that needed judgment or blocks Door-1 (e.g. needs a capability -> Tier 2). If you bailed under ADAPT, DON'T FORCE, this report (naming the blocker, the forbidding rule, and the tier verdict) IS the deliverable — there are no files.

12. CAPTURE PLATFORM GAPS. If the app *was* adaptable but something didn't fully translate because molma lacks a mechanism — an env var it can't inject, a public URL it can't supply, an auth secret it can't generate — add a **Platform gaps** section to the PR description, one entry per distinct gap. Each entry: gap-class tag, severity (`degrades` / `blocks-start`), trigger (the specific field/image/behavior), what breaks for the user, and why molma can't satisfy it. Reuse an existing gap-class tag when it's the same mechanism. Do NOT edit `NEXT.md` — that's the human's triage step, not yours. This is for platform gaps only; per-app judgment calls stay in the step-11 report.

13. RECORD STATUS. Add or update the app's row in `docs/dev/catalog-status.md`: `Full` / `None known` if it shipped clean, `Degraded` with a one-line user-visible limitation (linking the step-12 ledger entry) if a real feature is broken. If the app *runs but a feature degrades*, it still ships listed. If you wrote a manifest but discovered it can't run at all (crash-loops / never serves), mark it `Blocked` (re-shippable once a named gap closes) or `Rejected` (never), and set `listed: false` in its manifest so the store withdraws it — see `APP_STORE.md` # Listed apps. A fully bailed app (no files written, per step 11) gets no row.

REFERENCE (verify against the on-disk sources — these are reminders, not the schema):
- Required fields: id, manifest_version, name, version, compose_file, main_service, main_port. Rest optional.
- Injection (MOLMA_ prefix): folders mount at `/molma/<folder>`, injected as `MOLMA_FOLDER_<NAME>`; managed services as `MOLMA_SERVICE_<NAME>_{HOST,USER,PASSWORD,NAME,DSN}`; app data dir as `MOLMA_DATA_DIR`.
- Folder taxonomy (only these): photos, documents, movies, music, notes, downloads.
- Slug rule: `^[a-z0-9]+(-[a-z0-9]+)*$` — single internal hyphens, no leading/trailing hyphen, no `--` run (which also rules out the reserved `xn--` prefix).
- `version: custom` is the Door-2 marker — never use it for a catalog app.

DO NOT: honor `ports:`; use named volumes; emit absolute host binds; set `version: custom`; add Linux capabilities; emit the MALMO_ prefix; add a `multi_user` field; set a Door-2 folder `target:`; auto-rewrite beyond the documented adaptations; fabricate digests; treat a green `check` as proof the storage/services blocks are right (it's non-strict on those — verify by eye); force an app through by stripping or faking something it genuinely needs — bail and explain instead (see ADAPT, DON'T FORCE).
```

## After the run

- Eyeball both files against `catalog/files-demo/` (the closest worked example that exercises a folder grant) and the PhotoPrism sample in `APP_MANIFEST.md`.
- Re-run `manifest check` yourself — don't take the agent's word. (Then `manifest resolve` to fill digests, if it didn't error out.)
- One PR per app (or a small, clearly-scoped batch), each `Closes #<N>` per the [contributing guide](contributing.md). Catalog additions are not implementation slices, so they don't need a `docs/progress/` entry — but if an app surfaces a spec gap or a new admission edge case, fix the spec in the same PR.
- **Triage any new platform gaps.** If the PR description has a **Platform gaps** section, read it: a gap that now recurs across apps, or any `blocks-start` gap, is a candidate to promote into [`../specs/NEXT.md`](../specs/NEXT.md) (deduped and shaped) and the catalog-curation conversation. The PR is capture; NEXT.md is the decision — keep that boundary.
- **Confirm the status row.** Check the app's row in [`catalog-status.md`](catalog-status.md) matches reality — and that any `Blocked`/`Rejected` app actually carries `listed: false` (the roster's verdict and the store's behavior must agree).
