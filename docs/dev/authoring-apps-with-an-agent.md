# Authoring catalog apps with an agent

A reusable agent prompt that turns an upstream `docker-compose.yml` (or a GitHub repo) into a molma **Door-1** catalog app: it rewrites the compose to pass [admission](../specs/APP_LIFECYCLE.md), rewires env vars to molma's injected values, resolves image digests, and writes the `catalog/<id>/{manifest.yml, compose.yml}` pair. This is the author-side adaptation the app's developer would do to run on molma — the tool we use to grow the catalog.

## How to use it

1. Open a fresh agent session **inside the molma repo** (the prompt reads on-disk sources — it does not rely on its own text being correct).
2. Paste the prompt block below, then append the **inputs**: the app name, a pasted compose **or** a GitHub repo URL, and optionally a docs URL.
3. Let it work, then **read its report and re-run the two validators yourself** before committing. The agent's job ends at a passing `manifest lint` plus a clean admission re-check; the PR (`Closes #<N>`, progress entry if it's a slice) is yours.

One app per run. For a batch, run it once per app and review each `catalog/<id>/` pair on its own.

## What the validators actually cover (read before trusting "it passed")

Two **separate** checks, and the agent must satisfy both — they do not overlap:

- **`go run ./cmd/molma manifest lint <path>`** — schema only. Required fields, `manifest_version == 1`, kebab-case slugs, the `permissions` block (folder names / `mode` / `scope` / `default` / `target`), `health_probe` shape, that `compose_file` resolves + parses, and that `main_service` is one of the compose's services. That's the whole list (`cmd/molma/main.go`, `internal/manifest/manifest.go`).
- **Admission** (`internal/admission/admission.go`) — the structural compose rejections (ports, named volumes, absolute binds, `privileged`, `cap_add`, `build:`, `extends:`, host namespaces). **`manifest lint` does NOT run admission.** There is no admission CLI yet, so the agent re-checks it by hand: `docker compose -f catalog/<id>/compose.yml config -q` for syntax, then eyeballing the compose against the rules in `admission.go`.

Two consequences worth internalizing:

- **Unknown manifest fields are silently accepted.** `manifest.Parse` uses non-strict YAML, so `storage:`, `services:`, `resources:`, `description:`, `categories:`, `author:`, `license:` are not in the Go struct yet and are ignored by lint — they neither fail nor get validated. Write them anyway: the manifest is the durable, author-grade artifact and `APP_MANIFEST.md` is the source of truth. Following the full spec schema is forward-compatible; skipping it loses information the catalog will eventually consume.
- **A green `manifest lint` is necessary, not sufficient.** It can pass on a compose that admission would reject. Always run the admission re-check too.

The on-disk examples (`catalog/whoami/`, `catalog/files-demo/`) are intentionally minimal skeletons. For the full author-grade schema (storage split, managed services, resources, description), the worked reference is the **"Complete sample manifest"** (PhotoPrism) in `APP_MANIFEST.md`.

## The prompt

```text
Adapt an upstream app into a molma Door-1 catalog app (molma = home-server OS, Umbrel/CasaOS category). Produce a `catalog/<id>/{manifest.yml, compose.yml}` pair that installs cleanly — the author-side adaptation the app's developer would do to run on molma.

You run inside the molma repo. Read these on-disk sources; don't rely on this prompt alone — if it disagrees with the code, the code wins:
- `docs/specs/APP_MANIFEST.md` — manifest schema (fields, injection conventions, folder model). The "Complete sample manifest" section is the full author-grade shape.
- `internal/admission/admission.go` — the compose rejection rules; ground truth for what installs.
- `internal/manifest/manifest.go` — the schema validator your manifest.yml must pass (note: non-strict — it ignores fields it doesn't know, so a clean lint does NOT prove the storage/services blocks are right; it only proves the fields it knows are right).
- `catalog/whoami/`, `catalog/files-demo/` — worked examples of the target output (minimal skeletons; the full schema lives in the spec).

INPUTS (appended below): app name; a pasted compose OR a GitHub repo URL; optionally a docs URL.

STEPS

1. GATHER. Given a repo URL, do NOT clone — read just the files you need via `gh`:
   - Locate them in one call: `gh api repos/<owner>/<repo>/git/trees/HEAD?recursive=1 --jq '.tree[].path'`. Find the canonical compose (`docker-compose.yml`/`compose.yaml`, often at root or under `docs/`/`examples/`), `.env.example`, and README/install docs.
   - Read each raw: `gh api repos/<owner>/<repo>/contents/<path> -H "Accept: application/vnd.github.raw"`.
   Extract: image + version tag; the port the app listens on *inside* the container; the env-var names for its data dir, content/library path, and DB/cache credentials; which volumes are real data vs regenerable cache; whether the app supports pointing its library at an external path (if not, note it for step 6's app_managed_user_content fallback). Given only a pasted compose, use it (+ docs URL if provided).
   - **Icon**: Search the repo tree for common icon filenames (`icon.svg`, `icon.png`, `logo.svg`, `logo.png`, `app-icon.svg`, `app-icon.png`) anywhere under `assets/`, `docs/`, `public/`, or root. Download the first match: `gh api repos/<owner>/<repo>/contents/<path> -H "Accept: application/vnd.github.raw" --output catalog/<id>/icon.<ext>`. If nothing found, fall back to the GitHub org avatar: get the URL via `gh api /orgs/<owner> --jq '.avatar_url'` then `curl -sL "<url>" -o catalog/<id>/icon.png`. Note in the report which source was used.
   - **Screenshots**: Parse the README for embedded images (`![...](...)` markdown). For repo-relative paths, download each via `gh api repos/<owner>/<repo>/contents/<path> -H "Accept: application/vnd.github.raw" --output catalog/<id>/screenshots/NN.<ext>`, numbered `01`, `02`, etc., preserving the source extension (png/jpg/gif/webp). Skip external URLs (CDN, GitHub issue attachments, Notion, etc.) — list them in the report but do not attempt to download them.

2. IDENTITY & RUNTIME. Set `id`, `name`, `version` (the app's real version — never `custom`, which is the Door-2 marker), `main_service` (the service that is "the app"), `main_port` (its *internal* listen port, NOT a host-side mapping), `preferred_slugs`. Set `description.short` to a one-line tagline synthesized from the README's opening pitch — strip markdown, badges, and install noise, aim for ≤ 100 chars. Optionally add `description.long` as a markdown string for the detail page, synthesized from the README's "what is this" sections — omit install instructions, contributing guidelines, badge rows, and Docker-specific setup that doesn't apply on molma. Do NOT add a `multi_user` field — household-vs-personal is the installer's runtime choice, not a manifest property (APP_MANIFEST.md # G).

3. REWRITE THE COMPOSE TO PASS ADMISSION (verify each against `admission.go`):
   - Drop every `ports:` mapping. From a mapping like `8080:80`, mine the container side (`80`) for `main_port`.
   - Convert named volumes to relative binds under `./data/` (e.g. `db_data:/var/lib/postgresql/data` -> `./data/db:/var/lib/postgresql/data`).
   - Drop absolute host bind paths. If one was *user content* (a media library), don't bind it — grant it via `permissions.folders` (step 5) and point the app at `${MOLMA_FOLDER_<NAME>}`.
   - Drop `privileged`, `cap_add`, `build:`, `extends:`, `network_mode`, `pid/ipc/userns_mode: host`. If the app genuinely needs any of these, STOP — it's not Tier-3 (such apps go through Tier 2); report it and don't fabricate a passing compose.
   - Keep `image:` as readable version tags; digests go in the manifest `images:` map, not the compose.
   - Prefer the app listen on a non-privileged port (>=1024). Tier-3 apps run as a non-root elected UID under `cap_drop: ALL`, so an app that only listens on :80 may need a flag/env to move (see files-demo's `--port=8080`).

4. REWIRE ENV VARS — molma injects values; the compose maps them to the app's own names. The injected names use the MOLMA_ prefix (the project was renamed from "malmo"; never emit MALMO_):
   - Content/library path -> `${MOLMA_FOLDER_<NAME>}` (+ a `permissions.folders` entry). E.g. `PHOTOPRISM_ORIGINALS_PATH: ${MOLMA_FOLDER_PHOTOS}`. The brain bind-mounts each declared folder at `/molma/<folder>` and injects its absolute path as `MOLMA_FOLDER_<NAME>`.
   - App state dir -> a `./data/...` bind (use `${MOLMA_DATA_DIR}` if the app needs an absolute path).
   - DB/cache credentials -> `${MOLMA_SERVICE_<NAME>_*}` + a `services:` block — only when the app maps cleanly to managed Postgres/Redis. If it ships its own DB, keep that (adapted to binds) instead.

5. PERMISSIONS (least privilege). `internet`/`lan` only if actually used; `folders` with `mode` (default `read`; `write` is deliberate) and `scope` (`whole` | `pick-subfolder` + optional `default`); `devices` for `/dev/...`; `gpu: true` for hardware accel. Do NOT set a Door-2 `target:` on a folder — that field is for synthetic Door-2 manifests only.

6. STORAGE. Split binds into `storage.data_volumes` (back up: indexes, configs, app DB) and `storage.cache_volumes` (regenerable: thumbnails, transcodes). Add `estimated_size`/`tier` if justified. If the app has NO external-library support (it insists on owning its content internally), set `storage.app_managed_user_content: true` instead of a `folders` grant, and say so in the report — it forfeits the "files first-class" badge.

7. HEALTH PROBE (recommended for real web apps). If the app exposes a cheap HTTP health/ready path, declare `health_probe` (e.g. `health_probe: /healthz`, or the full mapping with `healthy_status`/`start_period`). Omit it for apps with no such endpoint — omitting just means the app is never probed.

8. IMAGE DIGESTS. For each `image:tag`, resolve its `sha256:` via `docker manifest inspect <ref>` or `skopeo inspect docker://<ref>` and key it in the `images:` map by the EXACT compose string. Never guess a digest; if the registry is unreachable, omit `images:` (valid — brain does trust-on-first-use) and say so.

9. WRITE `catalog/<id>/manifest.yml`, `catalog/<id>/compose.yml` (`compose_file: compose.yml`), `catalog/<id>/icon.<ext>` (from step 1), and `catalog/<id>/screenshots/NN.<ext>` (from step 1, if any were downloaded).

10. VALIDATE & ITERATE until BOTH pass — they are separate checks and lint does not cover admission:
   (a) `go run ./cmd/molma manifest lint catalog/<id>/manifest.yml` — schema, slugs, permissions, health_probe shape, compose-exists/parses, `main_service` present. Ground truth for the schema; iterate on its messages. Remember it is non-strict, so it will NOT flag a malformed `storage:`/`services:` block — those you verify against the spec by eye.
   (b) Admission + runtime (lint does NOT cover these; there is no admission CLI): run `docker compose -f catalog/<id>/compose.yml config -q`, then re-confirm against `admission.go` that none of step 3's rejections slipped in, that `main_port` is the internal port, that every `${MOLMA_SERVICE_*}` has a matching declared-and-used `services:` entry, and that every folder the app touches has a `permissions.folders` entry.

11. REPORT: what you changed and why; env vars rewired; permissions + reasoning; data-vs-cache split; digest status; health-probe choice; whether it's files-first-class or app_managed_user_content; icon source (repo file vs. org avatar fallback); screenshot count and any external URLs that were skipped; anything that needed judgment or blocks Door-1 (e.g. needs a capability -> Tier 2).

REFERENCE (verify against the on-disk sources — these are reminders, not the schema):
- Required fields: id, manifest_version, name, version, compose_file, main_service, main_port. Rest optional.
- Injection (MOLMA_ prefix): folders mount at `/molma/<folder>`, injected as `MOLMA_FOLDER_<NAME>`; managed services as `MOLMA_SERVICE_<NAME>_{HOST,USER,PASSWORD,NAME,DSN}`; app data dir as `MOLMA_DATA_DIR`.
- Folder taxonomy (only these): photos, documents, movies, music, notes, downloads.
- Slug rule: `^[a-z0-9]+(-[a-z0-9]+)*$` — single internal hyphens, no leading/trailing hyphen, no `--` run (which also rules out the reserved `xn--` prefix).
- `version: custom` is the Door-2 marker — never use it for a catalog app.

DO NOT: honor `ports:`; use named volumes; emit absolute host binds; set `version: custom`; add Linux capabilities; emit the MALMO_ prefix; add a `multi_user` field; set a Door-2 folder `target:`; auto-rewrite beyond the documented adaptations; fabricate digests; trust a green lint as proof admission passes.
```

## After the run

- Eyeball both files against `catalog/files-demo/` (the closest worked example that exercises a folder grant) and the PhotoPrism sample in `APP_MANIFEST.md`.
- Re-run both validators yourself — don't take the agent's word.
- One PR per app (or a small, clearly-scoped batch), each `Closes #<N>` per the [contributing guide](contributing.md). Catalog additions are not implementation slices, so they don't need a `docs/progress/` entry — but if an app surfaces a spec gap or a new admission edge case, fix the spec in the same PR.
