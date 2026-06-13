# Prepare elected shared folder sources (the shared half of bind-dir prep)

- **Status:** done
- **Date:** 2026-06-12
- **Specs touched:** `APP_ISOLATION.md`, `STORAGE.md`

Closes #156, the shared-tree half of the bind-source preparation that [prepare-every-bind-dir.md](prepare-every-bind-dir.md) (#147) deliberately scoped out. #147 made the brain create + chown elected **personal** use-case folder sources before `compose up`, but left **shared** sources (`/srv/malmo/shared/…`) untouched because that tree has a different ownership model — so a household shared-folder app whose elected `<Folder>[/<subfolder>]` didn't pre-exist still got a docker-created **root:root** directory the `malmo-app` container couldn't write. This carves off that half plus the production ownership decision.

## The decision (spec-grounded, not guessed)

The issue flagged the ownership model as a spec call. `STORAGE.md` # Permissions settles it: the household shared tree is **`root:malmo-shared`, mode `02770`** (setgid), *not* chowned to a runtime UID — the issue's "presumably `malmo-app:malmo-shared`" guess is superseded by the spec. The `malmo-app` household identity (`APP_ISOLATION.md`: well-known 2000/2001) reaches the tree through its `malmo-shared` `group_add`, which `writeOverride` already emits; ownership is by group, not by UID. So a brain-created shared subfolder must set the **group + setgid + group-rwx**, never the owner.

Three sub-decisions:

- **Where it runs — the brain, not a new host-agent op.** Consistent with #147's personal loop and the private-bind-dir loop, both done in-brain: the production brain is root, already reaches `/srv/malmo`, and already fetches `MalmoSharedGID` via `WellKnownIdentity`. No new protocol surface.
- **Never re-own a parent.** `os.MkdirAll` can't report which levels it created, so the prep walks the elected path one component at a time from the shared root down, applying the group + mode only to **newly created** levels. A pre-existing `/srv/malmo/shared/Documents` is left exactly as the storage setup made it; the shared root's *absence* is a hard fault (not ours to create).
- **Dev seam — out of the inner loop (documented).** Writing under `/srv/malmo` needs root, and the unprivileged native dev brain can't. The prep runs only under euid 0; under the dev brain it warns-and-skips, leaving `make dev` behavior exactly as before (household shared-folder apps were already unsupported there). A dev-rooted shared base via the host seam — the symmetric mirror of #147's dev-rooted personal home — is the follow-up if dev parity is ever wanted.

## What was done (`internal/lifecycle`)

- **New `prepareSharedSource(root, src, sharedGID)`** creates each missing level beneath the shared root, owning it `:malmo-shared` (group only, owner left as the root brain) with mode `02770`. The mode is expressed `os.ModeSetgid | 0o770`, **not** a raw `0o2000` — `os.Mkdir`/`os.Chmod` take an `os.FileMode` where the setgid bit is a named flag, so a literal `0o2770` would silently *not* set setgid. `Mkdir`'s mode is umask-masked, so the group + mode are reasserted with explicit `Chown`/`Chmod`.
- **A second loop in `install`** (after the personal-source loop) runs it for each `sourceShared` mount, gated on euid 0 with rollback on failure (prod posture); euid ≠ 0 warns and skips (dev posture). Same shape as #147's personal loop.
- **The shared tree root became a `Manager.sharedRoot` field** (`defaultSharedRoot = /srv/malmo/shared`), threaded into `isolation.sharedBase` and used by both `hostSource` (the bind path) and the prep. This is what makes the tests hermetic — they root it under a temp dir, so even a root-run suite never touches the real `/srv` (the personal side gets this for free via the fake host's temp `homeRoot`).

## Verification

- **`TestPrepareSharedSource_*`** (4 tests, 95.2% coverage of the helper, no root needed): creation sets `02770` + setgid + the malmo-shared group on every new level; idempotent re-run is a no-op; a pre-existing parent is never re-owned while the missing leaf still gets the shared mode; and the reject paths (source outside the root, absent/​non-dir root, a file as a path component, mkdir under a read-only parent, chgrp to a non-member gid). The non-root chgrp/​mkdir-failure cases self-skip under euid 0.
- **`TestInstallFolders_SharedSkippedUnderUnprivilegedBrain`** pins the dev-seam decision: a household shared install under the unprivileged brain doesn't hard-fail and creates nothing under the shared root, while the override still binds the source the root brain would have prepared.
- Existing `TestInstallFolders_HouseholdSharedWrite` / `TestInstallCustomFolders_TargetDestination` updated to assert the bind under the (temp) `sharedRoot`.
- `make check` green (gofmt, vet, OpenAPI freshness — no brain API surface changed — full Go suite).

## Known gaps & deviations

- **The production euid-0 create branch needs root to exercise end-to-end.** Like #147's prod chown branch, the install loop's euid-0 path is left to the prod posture; the creation *logic* is covered directly by `TestPrepareSharedSource_*` against a temp root with the test process's own GID. The real "boots clean + `stat` shows `root:malmo-shared` 02770" check is an on-hardware / rooted-VM step, not a hermetic unit test.
- **Household shared-folder apps remain out of the `make dev` inner loop** (the dev brain is unprivileged). Documented above and in `APP_ISOLATION.md`; the dev-rooted shared base is the follow-up.
- **No catalog app elects a shared `pick-subfolder` source yet**, so the path is exercised by tests and hand-written manifests only.
- **Existing broken instances are not retroactively healed** — install-time fix; a household shared-folder app installed before this with a root-owned source needs a reinstall (same caveat #147 carried).

## What's next

- A dev-rooted shared base via the host-agent seam (mirror of #147's `devIdentity` home) if household shared-folder apps should work under `make dev`.
- The first catalog app that elects a shared `pick-subfolder` source, to exercise the path end-to-end on hardware.
