# Folderless app `data/` writability: pin `user:` to the brain identity

- **Status:** done
- **Date:** 2026-06-09
- **Specs touched:** `APP_ISOLATION.md`

Open WebUI installed but its container crash-looped with `sqlite3.OperationalError: unable to open database file`. The migration could not create its SQLite DB under the `/app/backend/data` bind. Root cause: a Tier-3 container runs with `cap_drop: [ALL]`, which strips `CAP_DAC_OVERRIDE`, so it can only write its private `data/` dir when it runs as that dir's **owner** — root-ness alone is not enough. The brain pinned `user:` only for apps that declare `folders`; a folderless app (Open WebUI) emitted no `user:` and ran as the image default (root). That happens to work only when the brain itself is root and thus owns the `data/` dir it created (production) — it breaks in the native inner-loop brain, which runs as the unprivileged dev user (UID 1000), so the dir is 1000-owned and the root-UID container cannot write it.

This was a real divergence from `APP_ISOLATION.md` # Owner-scoped instances / # User content, which says every instance runs as a resolved `user:` with file ownership lined up natively — with no folderless carve-out.

## What was done

### Brain — `internal/lifecycle`

- **Every instance now resolves a runtime identity** (`lifecycle.go`, install step 6). `iso` is always built: folder apps keep the owner UID/GID (personal) or molma-app identity (household) and their folder binds; a **folderless app runs as the brain's own effective UID/GID** (`os.Geteuid()/os.Getegid()`) — the creator and owner of the `data/` dir `writeInstanceDir` just made. Production brain → root (behavior-identical to before); native dev brain → the dev user, so the bind stays writable.
- **`writeOverride` stamps `user:` on every instance**, not just folder apps (the `iso` it receives is now always non-nil; the empty-`mounts` path makes folder binds / `group_add` no-ops). The `isolation` struct doc was updated — it is no longer nil for folderless apps.
- **`data/` is chowned to the resolved identity** before `compose up`. No-op for folderless apps (already euid-owned); a real chown for folder apps under the production brain — which also closes a latent prod gap where a folder app writing private state into root-owned `data/` could not write it. The chown is **privilege-aware**: under the production brain (euid 0) a failure is a hard install error; under the unprivileged native dev brain it cannot chown to a host-agent-assigned UID it does not own, so that case is downgraded to a `slog.Warn` and the install proceeds (folder-apps-in-dev were already not faithfully runnable — no regression; folderless apps, the common dev path, are unaffected).

### Catalog + docs

- `catalog/open-webui/compose.yml` header comment corrected — it no longer claims "runs as root, no `user:` override needed"; it now documents that molma pins `user:` to the `data/`-owning identity and that Open WebUI tolerates a non-root runtime user given a writable `data/` + injected `WEBUI_SECRET_KEY`.
- `APP_ISOLATION.md` # Volumes gained the explicit rule: every instance runs as a resolved `user:` and its `data/` dir is owned by that UID, with the folderless-app identity spelled out and the `cap_drop: [ALL]` / `CAP_DAC_OVERRIDE` reasoning.

### Tests

- `TestInstallFolders_FolderlessSkipsIdentity` → `TestInstallFolders_FolderlessRunsAsBrainIdentity`: asserts a folderless override now carries `user: <euid>:<egid>`, binds no folders (`volumes:`/`group_add:` absent), and still makes no host identity calls. Full Go suite (`make test-nopam`) green.

## What's next

- **Dev-fake UID fidelity for *folder* apps.** The fake host-agent's `resolveHome`/`wellKnownIdentity` return UIDs in the [3000,3999] range, which the unprivileged native brain can neither chown to nor match — so folder apps are still not faithfully runnable in the inner loop (the chown is warned-and-skipped). Making the fake report the brain's own euid/egid would close this; deferred here to keep the change focused and avoid test-determinism churn.
- **Production identity for folderless apps is root.** Pinning a folderless app to the brain euid means root in production. A non-root default sandbox identity for folderless apps (a dedicated low-privilege service UID) is a larger security decision, intentionally out of scope.
- **Existing broken instances are not retroactively healed.** This fix is install-time; an already-installed instance whose `data/` is owner-mismatched (e.g. the Open WebUI that triggered this) needs a reinstall or a one-shot `chown`.
