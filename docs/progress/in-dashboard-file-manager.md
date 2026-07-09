# In-dashboard file manager (Files destination)

- **Status:** done
- **Date:** 2026-07-09
- **Issue:** closes #49
- **Specs touched:** `FILES.md`, `BRAIN_UI_PROTOCOL.md`, `BRAIN_HOST_PROTOCOL.md` (all pre-existing — this implements the already-written wire contract), `docs/architecture.md`

Implements the **Files** dock destination specced in `FILES.md` — the last Tier-1 product-surface gap from `NEXT.md`. "Files are first-class" was true on disk from day one but had no in-product browse surface: the only specced path to a user's own content was SMB + a desktop file manager, invisible to the Plex/Synology audience who will never mount a share. This is the zero-setup answer: open the dashboard, see your folders, upload and download from any device with a browser.

The whole vertical slice landed in one PR (protocol → host-agent fake + real → hostclient → brain → web-ui), per the scoping call recorded below.

## What was done

A single thread, `browser → web-ui → brain → host-agent (as the user's UID) → filesystem`, for the v1 op set: list/browse, download, upload, new folder, rename, move, copy, delete over the two roots **home** (`/home/<user>/`) and **shared** (`/srv/malmo/shared/`).

### Shared filesystem primitives — `internal/hostagent/fileops`

A new leaf package of pure, identity-agnostic FS primitives (`Resolve`, `List`, `Mkdir`, `Move`, `Copy`, `Delete`, `Open`, `Save`) plus lexical path containment. Two consumers justify the shared package (`CLAUDE.md` # no premature abstraction): the fake host-agent runs them in-process; the real host-agent's worker child runs the *same* code as the user's UID. Errors are the plain `fs.ErrNotExist`/`fs.ErrExist`/`fs.ErrPermission` + `syscall.ENOSPC` sentinels, so every layer maps them with `errors.Is` and no bespoke taxonomy. `Move` falls back to copy-then-delete across filesystems (home and shared can be different mounts); move/copy are non-clobbering; delete is permanent (no trash in v1).

### host-agent seam + handlers — `internal/hostagent`

- A consumer-side `FileManager` interface and the `/v1/files/*` handlers (`files.go`): metadata ops (Pattern A) plus streamed `GET`/`PUT /v1/files/content` (`io.Copy`, no whole-file buffering). `writeFileErr` maps the fs sentinels to the wire codes (`not-found`/`exists`/`permission-denied`/`no-space`/`invalid-path`/`is-a-directory`).
- `FakeFileManager` (in `fake.go`): runs `fileops` in-process as the dev operator (no UID drop — the dev brain and agent are the same operator, mirroring `resolve-home`), mapping home/shared to two base dirs. Wired by `cmd/host-agent` with the operator's home + a dev shared dir under `MALMO_STATE_DIR`.

### Real UID-drop file manager — `internal/hostagent/filemgr` (Linux-only)

The load-bearing security decision (`FILES.md` # Execution, `DECISIONS.md` 2026-05-31). `LinuxFileManager` runs every op in a **child process re-exec'd as the requesting user's UID/GID** via `exec.Cmd.SysProcAttr.Credential` — *not* in-process `setresuid`, which is per-OS-thread and unsafe under Go's M:N scheduler. Running as the user makes POSIX `0750`/`02770` the kernel-enforced backstop (a brain-side bug degrades to "denied," not "leaked"), gives created files correct ownership natively, and contains symlink attacks for free. The supplementary group set is carried (`Credential.Groups`) so the `02770 malmo-shared` tree is writable. The op spec passes to the child via env (`/proc/<pid>/environ` is owner+root-only, unlike world-readable cmdline); download uses an `OK`/`ERR` header line so a pre-stream failure surfaces as a typed error before bytes flow. A `classify`/`reconstruct` pair round-trips the error class across the process boundary so the handler's `errors.Is` mapping works identically for the real and fake agents. Isolated in its own package (imported only by `cmd/host-agent-real`, wired in both build profiles) with a `//go:build !linux` stub, mirroring `pamverifier`.

### Brain API — `internal/api` + `internal/hostclient`

- `hostclient` (`files.go`): Pattern A metadata methods plus streaming `FilesOpen` (returns the response body) and the first request-body-streaming `FilesSave` (pipes the incoming reader through). A typed `FileOpError{Code, Message, Status}` bypasses `do`'s error flattening so the brain can discriminate 404/409/507/403/400, mirroring `ResolveHome`.
- brain (`files.go`): huma metadata handlers + raw streamed content handlers. Resolves the session to a username (every op runs as the session owner — no cross-user browse, for any role), accepts only `home`/`shared`, rejects `..`/absolute before forwarding, and gates writes on the `data-drive-missing` health issue (`409 blocked-by-health-issue`, carrying `issue_id`). A `fileError` StatusError carries the spec's `{code, message, issue_id}` shape. File ops are **not** audited and do **not** trigger the elevation re-prompt (`FILES.md` # Audit & elevation) — deliberately unlike every mutation in `users.go`. The content endpoints are registered raw (outside OpenAPI), already exempt from the request-rate bucket (`ratelimit.go`).

### web-ui — `web-ui/`

Replaced the `FilesView.vue` stub with the full view: root switch (My files / Shared), breadcrumb navigation, a folder-first listing with per-row Download/Rename/Move/Copy/Delete, a New-folder inline input, upload with browser-native progress, a "show hidden" toggle, and inline delete confirmation. A `useFiles.ts` composable holds the API layer; download is a same-origin `<a download>` (cookie rides along) and upload is an `XMLHttpRequest` PUT (the only transport that reports upload progress) — both outside the JSON `api.ts` wrapper, which can carry neither a streamed File body nor a binary download. A `FileDestinationDialog.vue` folder picker backs move/copy across roots. Types generate from the brain's OpenAPI (`FileEntry`, `FileLocation`).

## Testing

- `fileops` — 88.7% (residual: fault-injection-only I/O error branches — mid-copy read faults, cross-device `EXDEV`, close errors).
- host-agent handlers, `hostclient` file methods, and brain handlers — covered end-to-end over a real UNIX socket (the brain test harness mounts a real `hostagent.Agent` + `FakeFileManager`), plus direct unit tests for the error-mapping arms and validation.
- `filemgr` — 51.7%: the worker op logic, error round-trip, `resolve`/`credential`, and command construction are unit-tested; **the privileged fork itself is uncovered by unit tests** because dropping to another UID (and `setgroups`) requires root. See Known gaps.
- `make check` and `make check-web` green.

## Scope decisions

- **One full-stack PR** (including the real `filemgr`) rather than a fake-only slice + follow-up, so `Closes #49` means the appliance actually enforces per-user UID isolation — the issue's defining security property, not just a dev-loop demo.
- **No web-ui unit tests.** The project has no web-ui test tooling; adding vitest is a new dependency + CI change beyond this issue's scope. The view is covered by `vue-tsc` typecheck + the production build (the existing `make check-web` gate). Rigorous testing is concentrated on the Go layers.

## Known gaps / what's next

- **Real UID-drop path is outer-loop-verified only.** The `filemgr` fork/credential-drop cannot run in CI (needs root + multiple real Linux users + a `/srv/malmo/shared` tree); a booted VM smoke of upload/download + a cross-user denial + a shared-group write is the acceptance step. The cross-platform surface (`fileops`, the worker op logic, error round-trip) *is* CI-tested.
- **`disk-full` health issue is not registered** in `builtinDefinitions()`. The brain maps a host `no-space` to `507` standalone; `FILES.md`'s "linked to the disk-full issue" is aspirational until that issue lands (`HEALTH.md`-owned, out of scope here).
- **OpenAPI advertises `ErrorModel` for the file routes**, not the custom `{code, message, issue_id}` shape huma actually emits for a returned `StatusError` (huma doesn't auto-register the custom type). The web-ui client tolerates both; a shared error-code registry is the `BRAIN_UI_PROTOCOL.md` follow-up.
- **v1 cuts** stand as specced (`FILES.md` # deferred, `NEXT.md`): thumbnails/preview, search, zip download, trash+undo, in-place editing, resumable uploads, sharing links, bulk USB/network import, and the audited admin break-glass into another user's files.
- **Degraded-state surfacing** relies on the global `HealthBanner` (app-wide) plus write-op `409` toasts; a Files-specific empty-tree treatment for `data-drive-missing` was not added.
