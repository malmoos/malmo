# Enforce folder/identity permissions in the override (slice 4 of install consent flow)

- **Status:** done
- **Date:** 2026-05-30
- **Specs touched:** docs/specs/BRAIN_HOST_PROTOCOL.md (well-known endpoint, in the companion slice [host-agent-well-known-identity.md](host-agent-well-known-identity.md))

## What was done

The enforcement slice: `writeOverride`/`writeEnv` now act on the permission fields parsed in slice 1 ([install-permissions-folders-schema.md](install-permissions-folders-schema.md)), and the install endpoint authoritatively validates the user's per-folder elections. This turns the advisory install-plan ([install-plan-endpoint.md](install-plan-endpoint.md)) into enforced container identity + bind mounts.

### API — authoritative election validation (`internal/api`)

- `POST /api/v1/apps` request body gains `config.folders[]` — per-folder `{folder, source?, subfolder?}` elections (BRAIN_UI_PROTOCOL.md Pattern B `config`).
- `resolveElections(man, scope, elections)` (new, `install_plan.go`) is the gate: it returns one fully-resolved `lifecycle.FolderMount` per declared folder (an omitted election takes the menu default), or a 422. Rejects: an election for a folder the app never declared, a duplicate election, a source not allowed for the scope (household forces shared; personal offers personal/shared), and a subfolder on a non-pick-subfolder folder or one that is absolute / escapes via `..`.
- `folderSourceMenu(scope)` extracted as the single source of truth shared by `buildInstallPlan` (advisory menus) and `resolveElections` (write-path validation) so the two never drift.
- The handler loads the manifest, validates, and on rejection emits an `app.install` `success=false` audit record (elevation-class mutation rule) before returning the 422 — synchronously, before the job starts.

### Lifecycle — stamp identity + mounts (`internal/lifecycle`)

- `Install`/`install` take `mounts []FolderMount`; `InstallCustom` passes `nil` (Door-2 compose owns its own `user:`).
- When the manifest declares folders, `install` resolves an `isolation`: `WellKnownIdentity` (slice 4a) for the `molma-shared` GID always, plus the `molma-app` UID/GID for a household instance; `ResolveHome` (slice 2) for a personal instance's owner UID/GID + home. `ErrUnknownUser` (owner deleted between plan and commit) rolls the install back as a terminal error, not a retry. Folderless apps skip this entirely and keep today's network/cap_drop-only override (verified: no host identity calls).
- `writeOverride` stamps, per service: `user: <uid>:<gid>`; one `volumes` entry per folder binding the elected host source (`<home>/<Folder>/` personal, `/srv/molma/shared/<Folder>/` shared, narrowed by subfolder) at `/molma/<folder>` with `:ro`/`:rw` from the manifest mode; `group_add: ["<molma-shared gid>"]` when any source is shared; and `devices` passthrough for `permissions.devices`.
- `writeEnv` injects `MOLMA_FOLDER_<NAME>=/molma/<folder>` per folder (stable regardless of source).

### Verification fixture

- New `catalog/files-demo/` (whoami image on port 8080, declaring `folders: documents read`). whoami binds a privileged port and would break under the forced non-root `user:`, so it stays the folderless smoke test; `files-demo` is the folders vehicle. (Deviates from the slice-2 handoff note that said "extend whoami" — extending whoami was unworkable for that reason.)

## How it maps to the specs

Realizes the enforcement half of `APP_ISOLATION.md` # User content (the toggle table's `folders` rows: personal source → home bind; shared source → shared bind + `group_add`) and `APP_MANIFEST.md` # `folders` (fixed `/molma/<folder>` mount + injected `MOLMA_FOLDER_<NAME>`). The household/personal `user:` split matches "personal instance runs as the owner; household runs as a shared service identity."

## Verified

Dev loop (real fake host-agent over the socket + real docker), per `feedback_verify_before_commit`:

- **Household, default elections** — `user: 2000:2000`, `group_add: ["2001"]`, `/srv/molma/shared/Documents:/molma/documents:ro`, `MOLMA_FOLDER_DOCUMENTS=/molma/documents`; container came up healthy as the non-root UID under `cap_drop: ALL`.
- **Personal, elect personal source** — `user: 3181:3181` (FNV-hashed owner UID in [3000,3999]), `/home/alex/Documents:/molma/documents:ro`, no `group_add`.
- **Illegal elections** — household electing `personal` → 422 ("source \"personal\" is not allowed for a household install"); undeclared folder → 422. Both auditable.

Unit tests: `internal/lifecycle/lifecycle_folders_test.go` (household-shared-write, personal-source-read-with-subfolder, deleted-owner rollback, folderless skip) and `internal/api/elections_test.go` (defaults, personal-may-elect-shared, subfolder override, seven rejection cases) + `instances_test.go` reject-and-audit HTTP test. All green under `-race`.

## Known gaps & deviations

- **GPU deferred.** `permissions.gpu` is still parsed-not-enforced. The spec's "refuse at capacity check if absent" (`APP_ISOLATION.md` # GPU) needs a host GPU-capability query that doesn't exist yet; emitting a GPU reservation blind would either silently under-grant or fail `compose up` instead of giving the specced capacity error. Folded into a follow-up with the host capability endpoint. No catalog app uses `gpu` today.
- **Device existence not validated.** `devices` are passed through (`/dev/x:/dev/x`) but the spec's "brain validates each exists before start" needs the same host hardware-introspection endpoint as GPU. Deferred together. An absent device currently fails at `compose up` rather than at a capacity check.
- **Double manifest load.** The install handler loads the manifest to validate, and `lifecycle.Install` loads it again. Acceptable (file read + parse); a single-load refactor would change `Install`'s signature to take a parsed manifest.

## What's next

- **Slice 5 — consent + config UI** in `web-ui/src/views/StoreView.vue`: render the install-plan permission lines + per-folder source/subfolder pickers, submit the elections as `config.folders[]`.
- **GPU + device capacity** — a host capability endpoint (`/v1/identity/well-known`'s neighbor) so `gpu`/`devices` honor the "refuse if absent" contract instead of failing at `compose up`.
