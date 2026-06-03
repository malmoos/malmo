# Door-2 permission block authoring

- **Status:** done
- **Date:** 2026-06-03
- **Specs touched:** none changed. Realizes `DASHBOARD.md` # Permissions, # Form is a projection of the synthetic manifest, and # Folder grants carry an explicit destination path (all locked, `DECISIONS.md` 2026-06-02). No `DECISIONS.md`/`NEXT.md` change.

Closes issue #57. Follows [door2-custom-install-flow.md](door2-custom-install-flow.md) (#56), which shipped the Door-2 spine — paste/upload, service/port inference from `expose:`, and the single `internet` election — and explicitly carved permission authoring out as this follow-up. #57 layers the rest of the permission block onto that form: **LAN** and **GPU** toggles, **folder grants** with an explicit in-container destination, the **Edit-as-YAML** escape hatch, the `ports:`-container-side port signal, and the `Synthesize` rework that makes the synthetic manifest carry an admin-elected permission set instead of a hardcoded `internet: true`.

## What was done

**Backend — `Synthesize` carries elected permissions** (`internal/manifest/synthesize.go`): `Synthesize` gained a `perms Permissions` parameter and sets `Permissions: perms` (was a hardcoded `Permissions{Internet: true}`). The internet-on default moved up to the API layer, where the form's election lives. The folder grants the manifest now declares drive the same isolation/bind machinery a store app uses — no Door-2 special-casing in `install()`.

**Backend — `ports:`-container-side port inference** (`internal/manifest/synthesize.go`): `InferMainPort` now falls back from `expose:` to the container side of a single published `ports:` mapping — `8080:80` ⇒ `80`, `127.0.0.1:8080:80` ⇒ `80`, `8080:80/tcp` ⇒ `80`, and the long-syntax `{ target: 80, … }`. The mapping itself is still an admission rejection (Caddy fronts every app); we mine its container side for the prefill only, never honoring the host binding (`DASHBOARD.md` # Main port). `expose:` wins when both are present; several ports (or a range) stay ambiguous ⇒ `0` ⇒ the form asks.

**Backend — Edit-as-YAML overlay (brain owns all YAML)** (`internal/manifest/synthesize.go`, `internal/api/custom_app.go`): `RenderPermissionsOverlay`/`ParsePermissionsOverlay` marshal a `Permissions` to/from the overlay YAML, with parse running the same `ValidatePermissions` gate the form path uses (a hand-typed folder target, unknown folder, or bad mode is rejected identically) and rejecting unknown keys so a typo (`interent:`) surfaces instead of silently reading as `false`. Two admin-only endpoints — `POST /api/v1/apps/custom/overlay/render` (structured perms → overlay text) and `/parse` (overlay text → structured perms) — let the frontend flip modes without shipping a YAML dependency. The install endpoint accepts **either** a structured `permissions` object **or** a raw `overlay` string (overlay wins); `resolveCustomPerms` collapses the two.

**Backend — Door-2 folder target** (`internal/manifest/manifest.go`, `internal/lifecycle/lifecycle.go`): `Folder.Target` (the explicit in-container destination, validated absolute + no-`..`) and `FolderMount.Target` carry the admin-typed path through to the override generator. `customMounts` resolves a Door-2 manifest's folder grants into mounts — the source is **scope-derived** (personal install ⇒ owner's `~/<Folder>/`, household ⇒ shared tree), since Door-2 has no per-folder source-election UI. `writeOverride`/`writeEnv` bind to `Target` when set (else the store `/molma/<folder>` convention) via a shared `containerDest` helper, and `MOLMA_FOLDER_<NAME>` reflects the real in-container path.

**Frontend — permission authoring** (`web-ui/src/views/CustomInstallView.vue`, `web-ui/src/api.ts`): the form grew a Permissions section — Internet/LAN/GPU toggles and folder-grant rows (Source picker over the six use-case folders → typed Destination → read/write → remove). An **Edit as YAML** toggle flips that section to a raw overlay editor: flipping out calls `/overlay/render`, flipping back calls `/overlay/parse` (a bad overlay keeps the editor open with the error inline, so the edit isn't lost). `devices` has no form control (the long tail) but is preserved across flips and shown read-only when set via YAML. Submit sends the structured `permissions` in form mode or the raw `overlay` in YAML mode.

## How it maps to the specs

- `DASHBOARD.md` # Permissions — LAN/GPU default off, folder access optional/empty by default, two-input folder rows (Source picker + typed Destination + read/write), devices/`services` deliberately not given form controls.
- `DASHBOARD.md` # Main port — best-effort inference now mines `expose:` *and* the container side of a `ports:` mapping, asked only when the compose is silent.
- `DASHBOARD.md` # Form is a projection of the synthetic manifest — the Edit-as-YAML toggle edits the *overlay*, not the compose (two documents, never merged); admission gates every path identically (the escape hatch escapes the form, not the sandbox).
- `DASHBOARD.md` # Folder grants carry an explicit destination path — the Door-2-only `target` binds the elected source straight to the admin-typed path; the source stays a picker, not free text. Store grants keep the fixed-path + env-var convention.
- `APP_MANIFEST.md` # Custom container — the synthetic manifest now carries the full elected permission set rather than a hardcoded internet flag.
- `CLAUDE.md` # Go discipline — brain owns YAML (consumer-side, no new frontend dep); new Door-2 API surface lives in its own `custom_app.go`; the overlay endpoints are reads (no audit); `Folder.Target` validation reuses the exported `ValidatePermissions` gate.

## Known gaps & deviations

- **The overlay is the *permissions block*, not the whole synthetic manifest.** The spec calls the YAML view a "raw manifest editor"; in practice the editor is scoped to the `permissions:` overlay, while `name`/`main_service`/`main_port` stay dedicated form inputs in both modes and `id`/`version`/`compose_file`/`images` stay brain-minted (editing them would break synthesis invariants). This is the minimal faithful reading: the escape hatch exists for permission fields the form omits, and the only one that *exists today* is `devices`.
- **`services:` / `health_probe:` are not yet manifest fields.** The spec lists them as things the YAML escape hatch reaches; the `Manifest` struct has neither (both await their subsystems). So the Edit-as-YAML long tail today covers `devices` only — a `services:`/`health_probe:` block typed into the overlay is rejected as an unknown key. When those subsystems land, they extend `Permissions`/`Manifest` and the overlay picks them up for free.
- **Door-2 folder source is scope-derived, not per-folder elected.** A catalog install elects each folder's source individually (`internal/api` resolveElections); a Door-2 paste has no per-folder UI, so every grant follows the install scope. A household custom app reading a user's personal folder is not expressible — uninstall + reinstall personal, or use the shared tree.
- **Admin gate on the *install* endpoint still rides on #58.** As in #56, this branch gates the new overlay endpoints (`requireAdmin`) and the UI; the `requireAdmin` gate on `POST /api/v1/apps/custom` itself is PR #58 (#55). Merge #58 first; until then the install endpoint is UI-gated only.

## Tests

- `internal/manifest/manifest_test.go` — `InferMainPort` ports-side cases (host:container, bind-ip, container-only, `/tcp` suffix, long-syntax `target`, expose-preferred-over-ports, range → 0, several-ports → 0); `TestParseFoldersTargetValidation` (absolute ok, relative/traversal rejected); `TestPermissionsOverlayRoundTrip`; `TestParsePermissionsOverlayValidatesAndCoaches` (empty → zero perms, bad target → error, typo'd key → error, bad YAML → error).
- `internal/api/custom_overlay_test.go` — render produces overlay text with the expected fields; parse round-trips structured perms (including a YAML-only `devices`); parse rejects a bad target as 422; both endpoints are admin-only (member → 403).
- `internal/lifecycle/lifecycle_folders_test.go` — `TestInstallCustomFolders_TargetDestination`: a Door-2 grant binds the scope-derived source to the typed target (`/srv/molma/shared/Documents:/photoprism/originals:rw`) and `MOLMA_FOLDER_DOCUMENTS` reflects the target. `TestInstallHappyDoor2` updated for the new `CustomSpec.Permissions`.
- `web-ui`: `vue-tsc --noEmit` + `vite build` both clean (`CustomInstallView` is a lazy chunk).
- Backend sweep green excluding the pam-cgo packages (`pamverifier`, `host-agent-real`) that need `libpam0g-dev` headers absent on this dev box — unrelated; they build in CI.

## What's next

- **#58 (#55)** must merge to enforce the admin gate on the install endpoint server-side.
- **Graduate-in-place** (`NEXT.md`): editing a *live* instance's manifest (re-render, restart, reconcile, audit) remains deferred — Door 2's post-install path is still uninstall + re-paste.
- When the managed-`services` and `health_probe` subsystems land, extend the overlay's reach to those fields (today's gap above).
