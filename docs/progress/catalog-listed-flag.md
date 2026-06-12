# Catalog app status roster + `listed: false` (pull from store without deleting)

- **Status:** done
- **Date:** 2026-06-11
- **Specs touched:** `APP_MANIFEST.md` (# A — new optional `listed` field), `APP_STORE.md` (# Listed apps — the enforcement contract), plus `docs/dev/catalog-status.md` (new per-app roster), `docs/dev/authoring-apps-with-an-agent.md` (status-recording step), `docs/dev/README` map.

We started adding catalog apps that are degraded or completely non-functional (the trigger: `calibre-web` crash-loops on every boot — its linuxserver.io image runs s6-overlay, which must start as uid 0 to set up `/run` before dropping to `PUID:PGID`, but malmo forces a non-root `user:` and strips `CAP_SETUID`/`CAP_SETGID`, so preinit aborts; gap-class `nonroot-data-ownership`). Two needs fell out: a **centralized record** of every app's state, and a way to **pull a broken app from the store without throwing away its adaptation work**. This slice delivers both — the roster doc, and the `listed: false` mechanism that enforces a `Blocked`/`Rejected` verdict.

## What was done

### Manifest + catalog (the mechanism)

- `internal/manifest`: new optional `listed *bool` field. A pointer so absent is distinguishable from explicit `false`; read through the new `IsListed()` accessor, which defaults absent/`true` to listed. Back-compatible field add — every existing manifest stays listed with no change, `manifest_version` unchanged.
- `internal/catalog`: the read surface is split by intent, asymmetrically.
  - **Store-facing, filtered:** `List()` (browse grid) skips unlisted apps; `Detail()` (detail page) returns `ErrNotFound` for an unlisted app — so the store treats it as nonexistent.
  - **By-id, honest:** new `Entry(id)` returns the grid summary for one app *without* the visibility filter; `Load()` stays raw. This is the path used for installed-instance enrichment, reconciliation, icon/screenshot serving, and `malmo manifest lint` — an app unlisted *after* install keeps its dashboard card and stays manageable.
- `internal/api`: both install paths gate on `IsListed()` — `installApp` (`POST /api/v1/apps`) and `installPlan` (`GET …/install-plan`) return the same 404 as a missing manifest, so a stale store link or scripted call can't install a withdrawn app. `listApps`/`getApp` enrichment was moved off `List()`/`Detail()` onto the honest `Entry()` lookup, so the filter never strips an installed app's card.

### Calibre-web pulled

- `catalog/calibre-web/manifest.yml`: `listed: false` with a comment pointing at the s6-overlay blocker, the two possible unblocks (user-namespace remap, or a verified `janeczku/calibre-web` swap), and the roster/ledger. Manifest, compose, and resolved digests are preserved in-catalog — it's withdrawn, not deleted.

### Docs

- New `docs/dev/catalog-status.md`: the per-app roster, keyed by app, with four states — **Full** (no limitation recorded), **Degraded** (runs, a feature broken), **Blocked** (temporarily withdrawn, ships when a named gap closes), **Rejected** (permanently won't ship). The limitation cell is a one-line summary + a link into `catalog-import-gaps.md`; the roster says *what/which app*, the ledger says *why/where-it-stands*. `Blocked`/`Rejected` rows carry `listed: false`.
- `APP_MANIFEST.md` # A and `APP_STORE.md` (new # Listed apps): the schema and the enforcement contract (store-facing filter vs. honest by-id resolution; curation control, not access control — box-wide, no per-user/role "show unlisted" path in v1).
- `authoring-apps-with-an-agent.md`: new step 13 (RECORD STATUS) and an "After the run" check, so every import updates the roster and a `Blocked`/`Rejected` app actually gets `listed: false`.

## How it maps to the specs

`listed` joins the identity/metadata block (`APP_MANIFEST.md` # A) the brain mostly doesn't act on — but unlike the others it *is* enforced, in the catalog read surface and the install gate (`APP_STORE.md` # Listed apps). It's the enforcement arm of the roster's curation states; the roster is documentation, the flag is what makes the verdict real.

## Known gaps & deviations

- **`Full` is "no limitation recorded," not "audited clean."** The roster marks apps with no recorded gap as `Full`, including ones not re-verified this slice (mealie, memos, files-demo, hermes-agent). The doc says so explicitly; a smoke-test should link its progress entry in the row's Detail column to back the claim. (`calibre-web` is the cautionary tale — it had looked `Full` from the catalog listing alone.)
- **calibre-web's `janeczku` swap is unverified.** Marked `Blocked` on the OS-side userns-remap gap (a `NEXT.md` topic, no implementation issue yet — a feasibility spike comes first, since Docker's userns-remap is daemon-global), with the upstream-image swap noted as a second possible unblock that still needs a boot test under the sandbox.
- **No "Rejected" app exists yet.** The state is defined for completeness; nothing in the catalog is permanently unshippable today.
- **Asset routes don't gate on `listed`.** `…/icon` and `…/screenshots/{n}` still serve for an unlisted app (they go through raw `Load`). Harmless — the grid never links them and the detail page 404s — left honest deliberately so an installed app's card icon keeps resolving.
