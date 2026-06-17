# Wire the VM brain to install an app offline (M2 prep)

- **Status:** done (the assertion phase that consumes this is the open follow-up — see What's next)
- **Date:** 2026-06-16
- **Specs touched:** none (realizes `TESTING.md` # Full-stack control-plane integration; no spec change)

The second and third brain/harness slices of #167 (M2), on top of [offline-install-digest-trust.md](offline-install-digest-trust.md) (which gave the brain an offline install path). Those closed the *logic* gap; these close the *plumbing* gaps that stop the air-gapped QEMU guest from installing a catalog app: the brain container shipped no catalog, and neither the app image nor a catalog was in the offline bundle.

## What was done

**Brain-container catalog + offline mode (`internal/hostagent/brainlaunch`, `cmd/host-agent-real`).** The brain run-spec mounted only the agent socket dir and `/var/lib/malmo` and set no `MALMO_CATALOG_DIR`, so `catalog.New` fell back to `./catalog` — empty in a container, so app install had nothing to install from. `brainlaunch.Config` gains:
- `CatalogDir` → emitted as `MALMO_CATALOG_DIR`. It must live under `DataDir`, so it rides the existing data-dir bind mount (the brain only *reads* the catalog — manifests, icons — so no separate mount is needed); emitted only when set, so an unset dir leaves the brain on its own default rather than pointing at `""`.
- `OfflineInstall` → emitted as `MALMO_OFFLINE_INSTALL` (only when set). `cmd/host-agent-real` reads both from its env (`MALMO_CATALOG_DIR` default `/var/lib/malmo/catalog`; `MALMO_OFFLINE_INSTALL` off by default via a new `envBool`).

**Offline bundle: app image + test catalog (`dev/test-qemu/`).** The full-stack lane installs `whoami` with no guest internet, so:
- `bootstrap.sh` `docker save`s `traefik/whoami:v1.10.3` into the first-boot loader's images dir (`load-control-plane-images.sh` globs `*.tar`, so it loads alongside the control-plane images — no loader change).
- A **test-only catalog** at `dev/test-qemu/catalog/whoami/` (staged to `/var/lib/malmo/catalog/`): a copy of `catalog/whoami` plus a `documents: {mode: write, scope: whole}` folder grant, so install exercises a real use-case-folder bind mount and the "content survives uninstall" assertion has a folder to write into. It is kept out of the shipping `catalog/` so the lane's needs don't bend the routing smoke test. Same image + catalog-promised digest as `catalog/whoami` — the offline path trusts that digest.
- The host-agent drop-in sets `MALMO_CATALOG_DIR=/var/lib/malmo/catalog` + `MALMO_OFFLINE_INSTALL=1`. `CANARY_VERSION` v19 → v20 (baked-image change).

## Verification

- `make check` green (brainlaunch unit tests assert the brain gets `MALMO_CATALOG_DIR` and, when offline, `MALMO_OFFLINE_INSTALL=true`; an unset catalog dir / offline-off omit them).
- `malmo manifest check dev/test-qemu/catalog/whoami/manifest.yml` → ok (schema + admission), so the folder-grant variant is valid.
- `bash -n dev/test-qemu/bootstrap.sh` clean.
- **No VM boot was run here.** The image-build, air-gap, and the assertions are the next slice and need `sudo make test-medium-qemu` on an mkosi/swtpm/QEMU host (the medium lane), which is not available in this environment.

## Known gaps & deviations

- **The catalog rides the `DataDir` mount rather than a dedicated read-only mount.** Putting it under `/var/lib/malmo` keeps it in the already-mounted tree (no overlapping nested bind); the brain never writes the catalog, so RW-by-inheritance is harmless. A dedicated RO mount is a future option if the catalog moves out of `DataDir`.
- **`whoami` never reads `/malmo/documents`.** The folder grant exists only to force a real bind mount; the "content survives uninstall" assertion will write into the *host* bind source and check persistence after uninstall (next slice).
- **No assertions consume any of this yet** — see below. This slice is inert until the assertion phase lands, which is why its acceptance is deferred to that run.

## What's next — the M2 capstone (air-gap + the five assertions)

The remaining, VM-gated work to close #167, to be developed against the real medium lane:

1. **Air-gap the guest** — add `restrict=on` to the `-netdev user` stanzas in `run-medium-tests.sh` (SSH `hostfwd` still works under it). Confirm SLIRP DHCP still serves the NM LAN NICs so the existing network-state assertions stay green (the open risk — verify on the lane, don't assume).
2. **Drive `/setup` + `/login` headlessly** (M1c left this undriven — `medium-assertions.sh:301`) and the **app-install** flow through Caddy: a session cookie, the install body's `config.folders` election for the `documents` grant, and job polling. The guest has no `curl`/`jq` today (M1b used bash `/dev/tcp` for GETs); the authenticated POST + cookie flow likely justifies adding `curl` to `mkosi.conf` packages — a small image change, decide on the lane.
3. **The five assertions** (`TESTING.md` table): dashboard reachable, real PAM login, whoami installs end-to-end (`docker compose up`, Caddy route, `whoami.local` via `avahi-resolve-host-name`, real bind mount), content survives uninstall, socket-proxy boundary (the last already asserted in the M1b block).
4. **Done when** `sudo make test-medium-qemu` boots the full stack with no guest internet and passes all five on a clean VM.
