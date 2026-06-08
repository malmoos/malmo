# Install footprint UI: store-card size + consent-dialog Storage block

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** No design change — `DASHBOARD.md` # Install authorization already specs the store-card size + the consent-screen Storage block + the not-enough-space warning, and `APP_STORE.md` # Catalog schema / `BRAIN_UI_PROTOCOL.md` # install-plan own the wire shapes. This renders them.

Closes issue #71 — the last link of #68 ("on-disk footprint before install"). #70 (`install-footprint-brain.md`) made the brain serve the footprint on two surfaces; this consumes both in the dashboard so the user sees what an app costs their box *before* they commit. Frontend-only; the wire types were already generated in #70.

## What was done

### `formatSize` byte helper (`web-ui/src/utils.ts`)

A shared `formatSize(bytes)` rendering raw bytes as a **consumer-facing** size — `GB`/`MB`/`KB`, never the technical `GiB`/`MiB` the live-resources view uses (that surface is for the all-users analytics dropdown; this is for the non-technical install audience). Binary math (`1 GB = 1024³`) so it round-trips the binary units manifests author `estimated_size` in (`APP_MANIFEST.md` # Storage) and matches the Synology/Windows "GB label on 1024-based math" convention. Pure helper; call sites add the `~`/"about" hedge in their own wording, keeping the figure's advisory nature (`APP_STORE.md` # Trust model) in the copy, not the number.

### Store-card size (`web-ui/src/views/StoreView.vue`)

The catalog browse row now shows a small `~<size>` next to the name/version, from the coarse catalog `footprint.image_disk_bytes` (the on-disk cost — the glance-level "how big is this", before the dialog's sharper box-specific number). Skipped when the manifest carries no sized images (`image_disk_bytes == 0`, e.g. the `whoami` fixture) rather than rendering `~0 B`.

> Scope note: issue #71 named "`StoreView.vue` / `AppTile.vue`" for the card. `AppTile.vue` is actually the **home-screen launcher tile** for an *installed* `Instance` — it has no catalog footprint — so the size lives on the `StoreView` catalog row, which is the real "store card". `AppTile` was left untouched.

### Consent-dialog Storage block (`web-ui/src/components/InstallDialog.vue`)

A **Storage** section (after the folder-source pickers, before submit) driven by the install-plan `footprint`:

- **Download line** — `download_bytes` ("Download about ~620 MB"), which is already box-specific (the brain subtracted images this box has cached). Degrades to "Already downloaded — nothing new to fetch." when it's 0.
- **Space line** — leads with the immediate on-disk image size (`image_disk_bytes`, "Uses about ~1.5 GB on your box") and appends ", and grows as you use it" only when the manifest declared an `estimated_state`. An estimate-only manifest (no sized image) drops to the qualitative phrasing.
- **Exclusion reassurance** — "Your own files stay in your folders, not inside the app." (the footprint is the app only; user content is excluded — `DASHBOARD.md`).
- **Not-enough-space warning** — when the full projected need (`image_disk_bytes + estimated_state_bytes`) reaches ≥90% of live `free_bytes`, a surfaced (non-blocking) caution; the Install button stays enabled, matching the resource-pressure "surface, don't block" posture. Silent when `free_bytes == 0` (brain couldn't measure) rather than crying wolf.

The whole block is skipped for an unsized manifest (no images sized, no estimate) so the dialog never shows a bare `~0`.

## How it maps to the specs

- **`DASHBOARD.md` # Install authorization** — the store-card size, the Storage block (download + space + reassurance), and the not-enough-space warning are all realized as described; wording + unit rounding live in the UI, raw bytes come from the brain.
- **`APP_STORE.md` # Catalog schema** — the coarse `footprint.image_disk_bytes` is the card number.
- **`BRAIN_UI_PROTOCOL.md` # install-plan** — the box-specific `footprint` (download/disk/estimate/free) drives the dialog.

## Known gaps & deviations

- **90% is a UI judgement of "approaches".** The spec says the warning fires when `image_disk_bytes + estimated_state_bytes` *approaches* `free_bytes` without defining the threshold; I picked 90%. Easy to tune; no spec lock.
- **The state estimate isn't shown as a number.** The space line shows the image size and a qualitative "grows as you use it"; the concrete `estimated_state_bytes` feeds only the warning math, matching the spec's example wording ("Uses ~1.5 GB … grows as you use it"). If product wants the number surfaced, it's a one-line change.
- **Reassurance line is generic**, not per-folder. The spec's illustrative "your Photos stay in your Photos folder" names a specific folder; I show one folder-agnostic sentence (always correct, no per-folder string assembly). Naming the declared folders is a possible polish.
- **No frontend unit test.** web-ui has no test runner (verification is `vue-tsc --noEmit` type-check + `vite build`); `formatSize` is covered only by the type-check + manual reasoning. A vitest harness for the pure helpers is deferred (would be its own infra change, out of scope here).
- **Stacked on #70.** This branches off `feat/70-app-footprint` (PR #94) because the footprint wire types live there, not yet on `main`; rebase onto `main` once #94 merges.

## What's next

- #68 is complete with this. No immediate follow-up; the threshold/estimate-number/per-folder-reassurance items above are product-polish, not blockers.
