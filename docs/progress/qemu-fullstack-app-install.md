# QEMU full-stack app-install assertions, air-gapped (M2 capstone)

- **Status:** done (closes #167; VM-boot acceptance via `sudo make test-medium-qemu`)
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
- **VM-boot acceptance is the gate and runs on the user's host** (`sudo make test-medium-qemu` — KVM + swtpm + an mkosi rebuild at CANARY v23). Not run in this environment; the run output is the acceptance signal. The medium-lane `main` baseline (M1b + M1c) was confirmed PASS by the user before this slice; this branch rebuilds the image (whoami + test catalog baked, air-gapped) and adds the M2 phase.
- **Two bugs surfaced by the VM runs** (both fixed; each is exactly the kind of thing only the real lane reveals):
  1. **`set -u` ordering** (run 1, aborted both boots before any verdict): `ADMIN_DOCS`/`MARKER` interpolated `$SETUP_USER` at top level, *before* the M1c block that sets it. Moved into `assert_app_install` as locals (it runs in second-boot, after that block).
  2. **Offline pin used a digest ref `compose up` couldn't resolve** (run 2, install rolled back → `whoami.local` 404): the override pinned `traefik/whoami@sha256:…`, but a `docker save`/`load` image has no RepoDigest, so `docker compose up` treated the digest ref as missing and tried to pull it (failing, air-gapped). Fixed in `internal/lifecycle/pinning.go`: in the offline-local case the override references the original **tag** (present locally); the trusted digest is still recorded in SQLite (`servicePin.ref` vs `.Digest`). This is a brain-image change, so CANARY v22→v23 (the brain image is baked + canary-gated).

## Known gaps & deviations

- **`scope=personal`, not household.** The household/shared-tree full-stack install is a data-drive (level-1) scenario, explicitly out of this slice (#167 is level-0). Tracked with the data-drive full-stack variant.
- **The M0 "TEMPORARY" control-plane-images block stays.** Its comment invited removal once the full-stack lane lands; it still asserts `docker.service` active + the bundled images present, which remains a useful cheap check, so it's left in rather than churned.
- **Air-gap × SLIRP-DHCP is the one assumption to watch.** `restrict=on` preserving SLIRP DHCP for the NM LAN NICs is documented QEMU behavior, but the network-state assertions are the canary — if a `restrict=on` run reds them, that's where to look.

## What's next

- This closes #167 and the brain/app-install arc of the #161 epic. The data-drive (level-1) full-stack variant — household/shared-tree binds over the storage-assembly layer (mergerfs + `/srv/malmo`) — is the separately-tracked follow-up.
- The deferred real registry-pull/update path (epic #161) remains the way a non-air-gapped box gets app images; offline mode is the baked-box path.
