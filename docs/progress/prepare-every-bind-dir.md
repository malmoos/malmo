# Prepare every private bind dir, not just `./data`

- **Status:** done
- **Date:** 2026-06-12
- **Specs touched:** `APP_ISOLATION.md`

Folder apps — and any app that binds more than the single `./data` dir — could not write their own data. This carries forward [folderless-app-data-dir-ownership.md](folderless-app-data-dir-ownership.md), whose "what's next" flagged exactly the dev-fake UID-fidelity gap closed here, and generalizes its single-`./data` chown to *every* declared private bind dir. Surfaced while testing Paperless-ngx (#142), which binds `./data/{data,media,export}` + `./data/redis`. Two independent causes, both fixed (#147).

**Root cause.** (1) *Production + dev:* the brain created + chowned only the top-level `<instance>/data` before `compose up`. The docker daemon then created every *other* declared bind source (`./data/media`, `./config`, …) as **root:root** — confirmed empirically that a missing bind source is created root-owned regardless of the parent's owner. A Tier-3 container (`cap_drop: ALL`, no `CAP_DAC_OVERRIDE`) running as the non-root runtime UID then can't write them and crash-loops. The catalog apps that worked (mealie, memos, kimai, open-webui) survive only because they bind exactly one `./data`. (2) *Dev only:* a folder app was pinned to the owner's resolved UID, which the fake host-agent invented as `fakeUID(username)` (≥ 3000) — no real account and not the UID the brain runs as — so even part (1)'s chown failed and was downgraded to a warning.

## What was done

### Part A — brain prepares every private bind dir (`internal/lifecycle`)

- **`composeService` gained `BindSources`** and `parseComposeServices` now decodes each service's `volumes:`, extracting the host-side source of each entry in both the short (`source:target[:mode]`) and long (`{type: bind, source: …}`) forms via a new `bindSource` helper (anonymous volumes and non-bind long-form mounts return "").
- **New `relativeBindDirs(composeBytes)`** returns the deduplicated, sorted set of `./`-relative bind sources as instance-relative slash paths. Absolute sources (the use-case folder binds the override injects — user-owned, election-managed) and named volumes are excluded by construction; any `../`-escaping source is dropped.
- **The single-`data/` chown (install step 6) became a per-dir loop**: for every relative bind dir, `os.MkdirAll` + `os.Chown` to `iso.uid/iso.gid` before `compose up`. The per-dir failure semantics are unchanged — production brain (euid 0) hard-errors + rolls back; the unprivileged dev brain downgrades a chown failure to a `slog.Warn`. `writeInstanceDir` still creates `data/`; the loop is idempotent over it.
- **Elected *personal* folder sources are prepared too** (a second loop over `iso.mounts`). A `pick-subfolder` election's subdir (e.g. `~/Documents/Notebooks` for Jupyter) is created + chowned to the owner UID before `compose up` — otherwise docker creates the absolute bind source root-owned and the owner-UID container can't write user content into it (`Permission denied: Untitled.ipynb`). Safe for personal because the runtime identity *is* the owner: a pre-existing `~/Documents` is already owner-owned (chown no-op) and only the new leaf is created. **Shared sources (`/srv/malmo/shared/…`) are deliberately skipped** — that tree is group-owned via `malmo-shared` and must not be chowned to a runtime UID; preparing shared subfolders (and the production owner-vs-malmo-app ownership story) is tracked as its own issue (#156). Same privilege posture as the bind-dir loop.

### Part B — dev seam resolves a chownable identity (`internal/hostagent`)

- **All three fake identity sources** (`UserMgr == nil`) now resolve to the operator's *own* uid/gid (`os.Getuid()/os.Getgid()`), the only identity the unprivileged dev brain can chown to:
  - **`resolveHome`** returns the operator uid/gid + home dir via a new `devIdentity` helper (instead of `fakeUID(username)` + `/home/<user>`); `devIdentity` ensures the home dir exists (operator-owned) so the use-case folder bind source is writable too.
  - **`wellKnownIdentity`** returns the operator uid/gid for `MalmoAppUID/GID` + `MalmoSharedGID` (instead of fixed 2000/2001), so household-scope folder apps resolve to a chownable identity.
  - **`allocateAppService`** returns the operator uid/gid (instead of a band UID ≥ 2100), so **`service_user: true` apps** (gitea, the first multi-bind service_user app tested in dev) get a chownable identity too — without this their bind dirs stayed operator-owned while the container ran as the un-chownable band UID and crash-looped (`/var/lib/gitea/git is not writable`). The fake no longer tracks a `svcIdents` reservation map; `releaseAppService` is an unconditional 200 no-op and the band guard moved into the real (`UserMgr != nil`) branch, where the userdel it protects actually lives.
- `fakeUID` (and its `hash/fnv` import) is removed — its only caller is gone.
- The prod/dev split stays entirely behind the host-agent seam; the brain is identical across prod and dev.

### Docs

- **`APP_ISOLATION.md`** # Volumes and # Runtime identity & data ownership: the invariant widened from "chowns `data/`" to "creates + chowns *every* declared relative bind dir", with the docker-creates-root-owned reasoning and the absolute-source exclusion spelled out.
- **`catalog-import-gaps.md`**: new `nonroot-data-ownership — paperless-ngx (multi-dir binds)` entry marked `implemented (#147)`, scoped to the brain-prepares-its-own-bind-dirs facet (the poznote/postiz image-internal-path gaps stay open — userns-remap).

### Tests (both hermetic — no docker, no root)

- **`TestInstallPreparesAllRelativeBindDirs`** (`lifecycle_folders_test.go`): a folderless multi-dir app (runs as the brain euid) — asserts every declared relative bind dir (`data`, `data/media`, `data/export`, `config`, `data/redis`) exists and is owned by the runtime UID, not just `data/`. Fails before the fix, passes after — the regression guard.
- **`TestRelativeBindDirs`**: the filter directly — relative sources kept (deduped, sorted), absolute + named + anonymous excluded.
- **`TestInstallFolders_PersonalSourceReadWithSubfolder`** extended: asserts the elected personal source (the `pick-subfolder` subdir) is created on disk before `compose up` (fails before the personal-folder loop). The lifecycle `fakeHost` now returns a writable temp `homeRoot/<user>` instead of `/home/<user>` so the install path can `MkdirAll` a personal source under it.
- **`TestResolveHome_FakeBranch_ReturnsOperatorIdentity`**, **`TestWellKnownIdentity_FakeBranch_ReturnsOperatorIdentity`**, and **`TestAllocateAppService_FakeBranch_ReturnsOperatorIdentity`** (`agent_test.go`): the three fake identity branches now return `os.Getuid()/os.Getgid()` (and the operator home), replacing the old fixed-constant / band assertions; the release-band test now wires a `UserMgr` (the guard's new home), and the fake release test asserts the unconditional no-op.
- `make check` green (gofmt, vet, OpenAPI freshness, full Go suite).

## What's next

- **The real production failure mode is not unit-testable.** The docker *daemon* creating root-owned bind dirs is invisible to the fake-driver lifecycle tests (which is why this went unnoticed) — it's covered by the `make dev` install-and-consume check on Paperless-ngx, or a future gated `dockerlive_test.go`.
- **The prod `euid == 0 → hard-error + rollback` chown branch needs root** to exercise directly; left to the prod posture (skipped off-root) rather than injecting a chown func + `privileged` predicate.
- **Shared folder source subdirs are not prepared.** The personal slice above creates + chowns elected *personal* folder sources, but *shared* sources (`/srv/malmo/shared/…`) are skipped — that tree is group-owned via `malmo-shared` and the correct creation/ownership (incl. the production owner-vs-malmo-app story) is a separate decision, tracked in #156. A shared `pick-subfolder` election whose subdir doesn't exist yet will still land root-owned until that lands.
- **Existing broken instances are not retroactively healed.** Install-time fix; an already-installed multi-dir app with root-owned bind dirs needs a reinstall.
