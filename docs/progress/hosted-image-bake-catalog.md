# Bake the Door-1 app-store catalog into the hosted cloud image

- **Status:** done
- **Date:** 2026-06-27
- **Specs touched:** none — `architecture.md` # catalog (loads from `MALMO_CATALOG_DIR`) already governs; this realizes it for the hosted image.

## What was done

A live hosted box couldn't load the store:

```
Couldn't load the catalog. read catalog: open /var/lib/malmo/catalog: no such file or directory
```

Root cause: the hosted production image (`dev/cloud/`) never baked a Door-1 catalog. host-agent-real defaults `CatalogDir` to `<DataDir>/catalog` = `/var/lib/malmo/catalog` (`cmd/host-agent-real/main.go`) and passes it to the brain as `MALMO_CATALOG_DIR` (`brainlaunch`), but the image's `stage_control_plane()` (`dev/cloud/stage-control-plane.sh`) staged the control-plane bundle, compose, caddy.json, and the seed materializer — and never created or populated `/var/lib/malmo/catalog`. The directory was simply absent, so `catalog.New(dir)` failed at first read and the store surface was dead. Only the appliance/test lanes (`dev/test-qemu/`) staged a catalog, and only a test-only `whoami` one.

Fix (`dev/cloud/stage-control-plane.sh`, in the shared `stage_control_plane()` both the production and cloud-test lanes source):

- Added `var/lib/malmo/catalog` to the wiring tree's `mkdir -p`.
- `cp -r "${REPO_ROOT}/catalog/." "$WIRING/var/lib/malmo/catalog/"` — bakes the real shipping catalog (manifests + icons + screenshots, ~15 MB, 22 apps). It rides the brain's existing `/var/lib/malmo` read-only bind mount, so no new mount or env is needed; the brain's existing default path resolves to it.

## How it maps to the specs

- `architecture.md` # catalog — the catalog package loads the Door-1 source from `MALMO_CATALOG_DIR`; the hosted image now provides that source where the brain's default points. ✓
- `ENVIRONMENT.md` # How the profile is realized — the catalog is data, not apt packages, so the lean package manifest (`expected-packages.txt`) is unchanged and the lean check still passes. ✓

## Known gaps & deviations

- **The catalog is baked, so a catalog update is an image rebuild + redeploy** — same delivery model as the control-plane image bundle (Door-1 is local by design; there is no remote-catalog fetch). A future hosted catalog-sync path, if wanted, is separate work.
- **Build/boot acceptance is the live deploy** — the image rebuild needs root + mkosi/QEMU (the #189 environment), so this verifies on the `make deploy-image` + redeploy in `malmoos/cloud`, consistent with the other `dev/cloud` build-script entries.

## How it was verified

- `bash -n dev/cloud/stage-control-plane.sh` — clean.
- Confirmed both `dev/cloud/bootstrap.sh` (production) and `dev/cloud/test/bootstrap.sh` (cloud-test lane) source the shared `stage_control_plane()`, so both bake the catalog.
- Catalog content checked: `catalog/` is 22 clean app dirs (`compose.yml`/`manifest.yml`/`icon.png`/`screenshots`), no `node_modules`/`.git`/build artifacts.

## What's next

- `make deploy-image` (in `malmoos/cloud`) to rebuild + reupload the hosted snapshot, then redeploy; re-check the store on a freshly provisioned box.
- If catalog churn outpaces image cadence, consider a hosted catalog-sync mechanism (out of scope here).
