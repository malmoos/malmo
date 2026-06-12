# install-permissions-folders-schema — parse the permission fields + collapse folder source

- **Status:** done
- **Date:** 2026-05-30
- **Specs touched:** `APP_MANIFEST.md`, `APP_ISOLATION.md`, `DASHBOARD.md`, `DECISIONS.md`, `NEXT.md`, `APP_STORE.md`, `THREAT_MODEL.md`, `UPDATES.md`

Slice 1 of the app install consent + config flow. Two things: (1) `internal/manifest` now parses the permission fields the override generator will consume (`folders`, `devices`, `gpu`) — data only, no enforcement yet; (2) a spec flip — the old `user_folders` / `shared_folders` split collapses into a single `permissions.folders` declaration, with the personal-vs-shared **source** moved out of the manifest and into the installer's per-folder choice at install time. No `writeOverride`/UI changes in this slice; those are slices 2–5.

## What was done

### Schema (`internal/manifest`)

- `Permissions` gains `Folders []Folder`, `Devices []string`, `GPU bool` (it previously parsed only `Internet`/`LAN`). `Folder` is `{Folder, Mode, Scope, Default}`.
- `validatePermissions` (called from `validate`) normalizes defaults in place — `mode` → `read`, `scope` → `whole` — and rejects: unknown folder names (taxonomy `photos|documents|movies|music|notes|downloads`), bad `mode`/`scope`, a `default` set without `scope: pick-subfolder`, and a `default` that is absolute or contains `..`.
- Source (personal `~/<Folder>/` vs shared `/srv/malmo/shared/<Folder>/`) is **not** parsed or validated — it is deliberately not a manifest concern.
- Tests in `manifest_test.go`: defaults applied, devices/gpu parsed, unknown-folder reject, bad mode/scope reject, default-misuse reject.

### Spec flip — `folders` replaces `user_folders` / `shared_folders`

- `APP_MANIFEST.md` # E: permissions block, the new # `folders` subsection (source-is-installer's-choice prose), both sample manifests, and the Locked-decisions bullets.
- `APP_ISOLATION.md`: # User content rewritten around the personal-vs-shared **source election**; this **resolves** the former MVP carve-out that forbade Tier-3 per-user apps from reading household-shared content (a personal instance reading the shared tree is now a supported election, via `group_add` to `malmo-shared`). High-level toggles table updated to two `folders` rows (personal / shared source).
- `DASHBOARD.md` # install authorization: documents the per-folder source choice; the deferred "cross-user shared-folder access" bullet is retired (replaced by a note pointing at the source election) and reframed as the still-deferred granular **post-install revocation**.
- `DECISIONS.md`: new top entry (2026-05-30) recording the flip above the same-day schema-reconcile entry it amends. The older entry is left intact as written.
- `NEXT.md`: the resolved carve-out item is replaced by a deferred "author-declared default/hint for folder source" item; two `permissions.user_folders` context pointers updated to `permissions.folders`.
- `APP_STORE.md` (`files_first_class` badge), `THREAT_MODEL.md` (blast-radius row), `UPDATES.md` (permission-widening prompt) updated to the `folders` key.

## How it maps to the specs

- Realizes the parse half of `APP_MANIFEST.md` # E / # `folders` and the `DECISIONS.md` 2026-05-30 folder-source flip.
- The validation taxonomy matches `APP_ISOLATION.md` # User content (fixed v1 folder set).

## Known gaps & deviations

- **No enforcement yet.** `writeOverride` still emits only networks/cap_drop/restart/labels/image; `folders`/`devices`/`gpu`/`user:` are parsed but not acted on. That is slice 4, which depends on the host home/UID resolution call (slice 2).
- **No host-side source resolution.** The brain can't read `/etc/passwd`; resolving the owner's home/UID and the `malmo-app` service identity + `malmo-shared` GID is slice 2.
- **`catalog/whoami` unchanged** — it declares no folders; the verification app gets an extended `folders` declaration in slice 4.
- `internal/hostagent/pamverifier` still fails to build here (CGO `C.RTLD_NEXT`); pre-existing, unrelated.

## What's next

- **Slice 2 — host resolves owner home + UID/GID.** New `GET /v1/users/{username}/home` across `protocol`, `hostagent` (real + fake), `hostclient`, and the `HostDriver` interface; surface the `malmo-app` service UID + `malmo-shared` GID. Documented in `BRAIN_HOST_PROTOCOL.md`. Gates slice 4.
- **Slice 3 — install-plan endpoint** (`GET /api/v1/catalog/{id}/install-plan`): permission lines, role-derived scope options, per-folder source options (personal+shared for personal scope, shared-only for household), pick-subfolder prompts, warnings.
- **Slice 4 — enforce in `writeOverride`/`writeEnv`** (all services): `user:`, folder bind mounts from the elected source, `group_add` for shared sources, `devices`, GPU, `MALMO_FOLDER_*`. Verify a real dev-loop install per `feedback_verify_before_commit`.
- **Slice 5 — consent + config UI.**
