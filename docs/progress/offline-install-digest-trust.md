# Offline (air-gapped) install: trust the catalog-promised digest

- **Status:** done
- **Date:** 2026-06-16
- **Specs touched:** `APP_LIFECYCLE.md`, `DECISIONS.md`

The first brain slice of #167 (M2 — full-stack integration assertions in the QEMU lane), carved off ahead of the QEMU harness work because it is the blocker the air-gapped app-install assertion sits on. The M2 lane runs the guest with no Docker Hub (QEMU netdev `restrict=on`) to prove the offline image bundle is complete; but `lifecycle.install` resolves every image digest by an unconditional `docker pull` + registry `RepoDigest` inspect (`internal/lifecycle/pinning.go`). Air-gapped, the pull hard-fails and a `docker save`/`load`ed image carries no `RepoDigest` to resolve against — so *any* install on a baked box fails before M2's assertions can run. This slice gives the brain an offline install path; the QEMU harness, the bundled app image, the brain-container catalog mount, and the five assertions are the follow-up slices of #167.

## What was done

- **`resolveImages` / `pullAndResolve` gain an `offline` parameter** (`internal/lifecycle/pinning.go`). The promised digest (from `manifest.Images`, "" for Door-2) is threaded in. On a pull failure in offline mode, the new `resolveOffline` helper falls back to the locally-present image: `ImageInspect` is the presence probe (it succeeds with an empty `RepoDigest` list for a loaded image and errors only when the image is absent), and the trusted digest — the catalog promise, or the `@sha256:` ref for a digest-pinned image — becomes the pin. Two cases stay fatal and are distinguished from a transient pull error: no trusted digest to fall back on (a Door-2 install can't be pinned offline), and a genuinely-absent image (the bundle is incomplete — the missing-image hard-fail the air-gapped lane exists to catch). The online path is byte-for-byte unchanged: a successful pull still resolves and verifies the registry digest.
- **`Manager.offlineInstall` + `SetOfflineInstall`** (`internal/lifecycle/lifecycle.go`). A box-level mode, not a per-install option, set as a struct field in the same style as `sharedRoot` / `healthWait`. `install` passes `m.offlineInstall` to `resolveImages`.
- **`cmd/brain` wires `MALMO_OFFLINE_INSTALL`** (`cmd/brain/main.go`): a new `envBool` helper parses the flag (off by default), and `main` calls `life.SetOfflineInstall(cfg.offlineInstall)`. A baked, registry-less box sets it; a box with a registry leaves it off and pulls + verifies as before.

## Why gated behind an explicit mode, not a silent fallback

A silent "pull failed → use whatever image is local" fallback would let an *online* box mask a transient registry outage: on an update, the registry being briefly unreachable while the previous version's image is still present locally would silently accept the stale image and pin the new (un-verified) promised digest — an integrity hole. The explicit mode confines the trust-the-bundle behavior to boxes that genuinely have no registry. For a *fresh* install of a never-seen image the fallback can't fire even in offline mode (the image isn't local → presence probe fails → hard-fail), so the mode only ever reuses bytes the offline bundle deliberately loaded.

## Verification

- **`internal/lifecycle/pinning_offline_test.go`** (5 cases, full install through `Manager.Install`):
  - offline + locally-loaded image + catalog promise → installs, override pins the **promised** digest, driver order `Pull → ImageInspect → ComposeUp`;
  - offline + image absent → hard-fails, no `ComposeUp`, SQLite row rolled back;
  - offline + no catalog promise (empty `Images`) → hard-fails;
  - **online** (mode off) + pull fails while the image is present locally → still fatal (the gating guard);
  - offline mode + pull *succeeds* → still resolves the registry digest (happy path unchanged).
- The fake docker driver grew a `loaded` set so `ImageInspect` can model "present, no `RepoDigest`" (a docker-loaded image) vs "absent" (error), mirroring the real CLI.
- `make check` green (gofmt, vet, OpenAPI freshness — no API surface changed — full Go suite).

## Known gaps & deviations

- **Not exercised on a real air-gapped box yet.** The logic is covered hermetically against the fake; the end-to-end "guest with no internet docker-loads the bundle and installs whoami" proof is the QEMU full-stack lane — the next slice of #167. This slice only unblocks it.
- **The digest-pinned (`@sha256:`) offline branch is not covered through `Install`.** M2's whoami compose references images by tag, so the tag path is what the lane needs; the digest-form fallback is symmetric and low-risk but tested only by construction, not a test.
- **Offline mode trusts the catalog promise without re-deriving the loaded image's manifest digest.** A `docker save`/`load` image exposes no manifest digest (only a config `Id`), so there is nothing to compare offline — the signed catalog is the trust anchor by design (`APP_STORE.md` # Trust model). Recorded in `DECISIONS.md` 2026-06-16.

## What's next

The remaining #167 slices, on top of this one:
- bundle `traefik/whoami` (the app image) into the offline image set + `docker load` it at first boot;
- bundle a **test-only catalog** (whoami + a `documents:write` folder grant) and mount it into the brain container (`MALMO_CATALOG_DIR` + a `brainlaunch` mount — the brain container has no catalog today);
- air-gap the guest (`restrict=on` on the QEMU netdevs) and set `MALMO_OFFLINE_INSTALL` on the VM brain;
- extend `medium-assertions.sh` with the five M2 assertions (dashboard, real PAM login, app install end-to-end, content survives uninstall, socket-proxy boundary) and bump `CANARY_VERSION`.
