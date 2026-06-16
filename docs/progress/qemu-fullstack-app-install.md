# QEMU full-stack app-install assertions, air-gapped (M2 capstone)

- **Status:** done (closes #167; VM-boot acceptance **PASSED** — `sudo make test-medium-qemu` green on a clean VM, all five assertions)
- **Date:** 2026-06-16
- **Specs touched:** none (realizes `TESTING.md` # Full-stack control-plane integration — the assertion table; no spec change)

The capstone of #167 / the #161 epic, on top of [vm-offline-app-install-prep.md](vm-offline-app-install-prep.md) (offline bundle + brain catalog wiring) and [offline-install-digest-trust.md](offline-install-digest-trust.md) (the offline install logic). Those made the air-gapped guest *able* to install a catalog app; this slice **air-gaps the guest and asserts it end-to-end**, completing the `TESTING.md` assertion table.

## What was done

**Air-gap the guest (`run-medium-tests.sh`).** All three QEMU `-netdev user` stanzas gain `restrict=on`: SLIRP routes no guest packet to the host or the outside, so a missing bundled image *hard-fails* instead of silently pulling from Docker Hub — proving the offline bundle is complete (the whole point of the air-gapped lane). `restrict=on` leaves explicit forwarding (the SSH `hostfwd` control channel) and SLIRP's own DHCP (the NM LAN NICs' leases) working, so the SSH-driven assertions and the network-state slice are unaffected.

**The five assertions (`medium-assertions.sh`, new `assert_app_install`, second-boot).** Driven over Caddy `:80` with the same bash `/dev/tcp` idiom as the M1b/M1c blocks (no curl/jq in the image):
1. **Authenticate** — reuse M1c's admin; `/login` and keep the `malmo_session` cookie (install authorizes on the session — no elevation needed for install).
2. **Install** — `POST /api/v1/apps {"manifest_id":"whoami","scope":"personal"}` → 202. `scope=personal` deliberately: a level-0 VM has no data drive, so the shared tree (`/srv/malmo/shared`) that `household` would force doesn't exist; personal binds the admin's own `~/Documents`, which the brain creates at install. Air-gapped, the brain's `docker pull` fails and it trusts the catalog-promised digest of the docker-loaded image.
3. **App reachable** — poll the `whoami.local` route through Caddy for `200` **and** the whoami echo body (`Hostname:`), so we know we hit whoami and not a stale splash/catch-all. A 200 here means the whole transaction converged (offline-trusted image → `compose up` → health-wait → route flip; whoami has no healthcheck, so the brain's `"none"`-counts-as-healthy path applies).
4. **`whoami.local` resolves** — `avahi-resolve-host-name -4` (avahi-utils; no `libnss-mdns`/getent dependency).
5. **Real bind mount** — whoami is a FROM-scratch image (no shell → no `docker exec`), so assert host-side: the running container's `Mounts` carry `/malmo/documents` bound from `~/Documents`. Container resolved by its `malmo.instance_id` label.
6. **Content survives uninstall** — write a marker into the bound host folder, `DELETE /api/v1/apps/{id}`, wait for the container to be gone, assert the marker outlives it (`STORAGE.md` # Files are first-class). The socket-proxy boundary (the fifth table row) is already asserted in the M1b block.

Runs **before** `assert_network_state` in the second-boot phase so the network test's interface disconnect / IP renumber can't disturb whoami's mDNS resolution mid-assertion.

## Verification

- `make check` green; `bash -n` clean on both scripts; the test manifest passes `malmo manifest check`.
- **VM-boot acceptance PASSED** on the user's host (`sudo make test-medium-qemu`, CANARY v25, KVM + swtpm): first boot M0/M1b/M1c + TPM enroll; second boot M0/M1b/M1c + the M2 phase + network-state — `control-plane M2: whoami installed air-gapped, whoami.local resolved, route + bind verified, content survived uninstall`, `medium-lane test: PASS`. The `main` baseline (M1b + M1c) was confirmed PASS before this slice; the air-gap (`restrict=on`) left M1b/M1c/network-state green.
- **Three bugs surfaced by the VM runs** (all fixed; each is exactly the kind of thing only the real lane reveals — the `install_diag` brain-log dump added in run 3 pinpointed the third):
  1. **`set -u` ordering** (run 1, aborted both boots before any verdict): `ADMIN_DOCS`/`MARKER` interpolated `$SETUP_USER` at top level, *before* the M1c block that sets it. Moved into `assert_app_install` as locals (it runs in second-boot, after that block).
  2. **Offline pin used a digest ref `compose up` couldn't resolve** (run 2, install rolled back → `whoami.local` 404): the override pinned `traefik/whoami@sha256:…`, but a `docker save`/`load` image has no RepoDigest, so `docker compose up` treated the digest ref as missing and tried to pull it (failing, air-gapped). Fixed in `internal/lifecycle/pinning.go`: in the offline-local case the override references the original **tag** (present locally); the trusted digest is still recorded in SQLite (`servicePin.ref` vs `.Digest`).
  3. **Well-known identities absent** (run 4, `install failed … well-known-identity-failed`): the brain calls host-agent `/v1/identity/well-known` for *any* folder app (to get the shared GID), and `usermgr.WellKnownIdentity` looks up the `malmo-app` user + `malmo-shared` group by name — neither existed on the test image (M1c provisioned only `malmo` + `sudo`). Added the fixed well-known IDs 2000/2001 (APP_ISOLATION.md) in `mkosi.postinst.chroot`, as a real box does at build.
- **`install_diag`** (dumps the install response, `docker ps -a`, `GET /apps`, and the brain log tail to stdout on an install failure) is kept — it is the lane's debugging surface for any future M2 regression and runs only on failure.
- CANARY bumped v20→v25 across these fixes (offline bundle + brain-image + baked-script + postinst changes are all canary-gated).

## Known gaps & deviations

- **`scope=personal`, not household.** The household/shared-tree full-stack install is a data-drive (level-1) scenario, explicitly out of this slice (#167 is level-0). Tracked with the data-drive full-stack variant.
- **The M0 "TEMPORARY" control-plane-images block stays.** Its comment invited removal once the full-stack lane lands; it still asserts `docker.service` active + the bundled images present, which remains a useful cheap check, so it's left in rather than churned.
- **Air-gap × SLIRP-DHCP is the one assumption to watch.** `restrict=on` preserving SLIRP DHCP for the NM LAN NICs is documented QEMU behavior, but the network-state assertions are the canary — if a `restrict=on` run reds them, that's where to look.

## What's next

- This closes #167 and the brain/app-install arc of the #161 epic. The data-drive (level-1) full-stack variant — household/shared-tree binds over the storage-assembly layer (mergerfs + `/srv/malmo`) — is the separately-tracked follow-up.
- The deferred real registry-pull/update path (epic #161) remains the way a non-air-gapped box gets app images; offline mode is the baked-box path.
