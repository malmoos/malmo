# Install consent + config UI in the Store view (slice 5 of install consent flow)

- **Status:** done
- **Date:** 2026-05-30
- **Specs touched:** docs/specs/DASHBOARD.md (# install authorization, # warn-don't-block), docs/specs/BRAIN_UI_PROTOCOL.md (Pattern B — Jobs), docs/specs/APP_MANIFEST.md (# folders), docs/specs/APP_ISOLATION.md (# User content)

## What was done

The final slice of the install consent flow: the Vue 3 frontend now fetches an `InstallPlan`, presents a consent + configuration dialog, and submits the user's elections to `POST /api/v1/apps` with `config.folders[]`.

### `web-ui/src/api.ts`

Added two types alongside the existing `InstallPlan*` interfaces:

- `FolderElection { folder, source?, subfolder? }` — one entry in the install request's `config.folders` array.
- `InstallRequest { manifest_id, scope?, confirm?, config: { folders: FolderElection[] } }` — the full `POST /api/v1/apps` body for Door-1 catalog installs.

### `web-ui/src/components/InstallDialog.vue` (new file)

A modal dialog component. Props: `plan: InstallPlan`, `submitError?: string | null`. Emits: `submit(InstallRequest)`, `cancel`. (Scope-picker visibility keys off `scope_options.length`, not the caller's role — the role is already baked into the plan, so the dialog needs no role prop.)

Features:
- **Scope picker** — shown only when `scope_options.length > 1` (i.e. the caller is an admin). Radio buttons "For the whole household" / "Just for me"; default is `scope_default`. Members see a fixed "Installing as a personal app." label instead.
- **Permissions display** (advisory, UI-authored sentences): internet → "Connect to the internet"; lan → "Reach other devices on your network"; gpu → "Use the graphics card"; each device path → "Access device <path>"; `mode: read` folder → "Read files in your <Folder> folder"; `mode: write` folder → "Add, change, and delete files in your <Folder> folder" — write rows are styled in `text-destructive` with bold weight (visually distinct per APP_MANIFEST.md:218 and APP_ISOLATION.md # User content).
- **Per-folder source pickers** — reactive to the elected scope: reads `f.sources[electedScope]` (`SourceMenu`). A single-option menu (e.g. household forces "shared") renders as a fixed/disabled text label. A multi-option menu renders radio buttons with human labels: "Your <Folder>" (personal) / "The household's shared <Folder>" (shared). Source defaults are re-derived when the scope radio changes; subfolder user input is preserved across scope flips.
- **Subfolder input** — only when `f.scope === "pick-subfolder"`: a text input pre-filled with `subfolder_default`, labelled "Which subfolder should this app manage?".
- **422 inline error** — when `submitError` is set, displayed as a styled error block inside the dialog, keeping it open for correction.
- **Footer** — Cancel and Install buttons. On Install, emits an `InstallRequest` assembled from elected scope, per-folder sources, and per-folder subfolders.

All wording is UI-owned; no raw enum values are shown to the user.

### `web-ui/src/views/StoreView.vue` (rewritten catalog section)

- Imports `useAuth()` to get `currentUser` (needed for `owner_user_id` comparison and role).
- **Per-row button logic** (replaces the old `installedManifest()` hiding hack):
  - Household instance exists → "Open shared app" link to `instance.url` + a secondary "Install" button (allows installing a personal copy alongside the shared one).
  - Caller's own personal instance exists (`scope === "personal" && owner_user_id === currentUser.id`) → "Open" link only.
  - Otherwise → "Install" button.
- **Install flow**: clicking Install sets `planFor` ref → `useQuery(["install-plan", id], enabled: planFor !== null)` fetches the `InstallPlan` → once loaded, `<InstallDialog>` renders. Dialog emits `submit(InstallRequest)` → `install` mutation posts to `/apps`. As soon as the `POST` is accepted (202 + `job_id`), the dialog closes and the catalog row's button switches to a disabled "Installing…" (driven by an `installingId` ref, not the dialog state). `waitForJob` then polls the job to a terminal state; `onSettled` awaits the `["apps"]` invalidation before clearing `installingId`, so the row flips straight from "Installing…" to "Open" with no intermediate "Install" flicker. Because 409/422 are returned at `POST` time (before the job starts), they still surface while the dialog is open; a failure *during* the job (after the dialog closed) shows in a standalone dismissable `installError` banner.
- **409 duplicate-install**: on `ApiError.code === "duplicate-install"`, `duplicateInfo` is set with `error.message`; dialog hides and a warning banner appears with the summary and an "Install my own copy" button that re-submits the same `InstallRequest` with `confirm: true`.
- **All other failures (422 election rejection, job failure, host 5xx)**: `dialogError` is set with the error message and passed as the `submitError` prop into the still-open dialog, displayed inline. The install `mutationFn` throws when `waitForJob` returns a non-`completed` terminal job (carrying `job.error.message`), so an install that fails *after* the job starts is surfaced rather than silently closing the dialog.
- `onSettled` invalidates `["apps"]` in both success and error paths.
- The old `installedManifest()` function and single-button hiding are removed. The header comment block is updated to describe the new flow.

## How it maps to the specs

Realizes the consent screen described in DASHBOARD.md # install authorization and # warn-don't-block. The scope picker follows the install authorization table (admin: household/personal choice; member: personal only). Per-folder source pickers follow APP_MANIFEST.md # folders ("source is the installer's choice") and the Option A source menus from the install-plan endpoint (slice 3). Write-mode warning styling matches the APP_MANIFEST.md:218 requirement ("shows up on the install screen as 'this app can ADD, CHANGE, AND DELETE files'"). The 409 duplicate-install pattern matches BRAIN_UI_PROTOCOL.md Pattern B and DASHBOARD.md # warn-don't-block exactly. The install mutation follows Pattern B (POST → 202 + job_id → poll to terminal state).

## Verified

- `make check-web` passes (vue-tsc --noEmit + vite build, no TypeScript errors, build output 14.66 kB for StoreView chunk).
- Runtime verification (install files-demo from the Store, admin scope picker, member forced-personal path, duplicate-install flow, 422 inline error display) is **pending human verification** — the implementation was not run against a live brain.

## Known gaps

- **Plan loading state UX**: while the install plan is fetching (after clicking Install, before the dialog appears), a brief "Loading install plan…" text is shown below the catalog list rather than in-place on the row. A per-row spinner is a follow-up UX polish item.
- **GPU/devices capacity errors** deferred from slice 4 (see `install-permissions-enforcement.md` Known gaps) — not a UI gap, but when the brain gains capacity-check enforcement those 422s will surface correctly via the existing `dialogError` path.

## What's next

- **Mute settings UI + retention** — the top item in the progress queue (`docs/progress/README.md` # Up next).
