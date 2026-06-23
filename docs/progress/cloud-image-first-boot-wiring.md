# Production hosted cloud image: first-boot wiring promoted from the test lane (#242)

**Status:** done (code-complete; real-cloud boot verification is the CL6 live run — see Known gaps)

Closes the OS half of the cloud#6 / CL6 convergence: **#242**. Surfaced by the first real-cloud boot of the production C2 image — a tenant box provisioned from `MALMO_HETZNER_IMAGE` (built by `make build-cloud-image`) booted on Hetzner **completely unreachable**: `/etc/systemd/network/` empty → the NIC never got an IP, and none of the malmo runtime wiring was present. The cause: all the first-boot wiring lived only in the **test** lane (`dev/cloud/test/`, staged at boot-proof time), while the production lean config (`dev/cloud/`) was built, by its own comment, "with NO postinst/control-plane images." C1b (#203) only ever asserted leanness; C2 (#205) booted the *test* image; the production image that provisioning actually uploads had never been booted. This slice promotes the wiring into the production image so `make build-cloud-image` emits a self-bootstrapping tenant box.

Builds on C1a (#202, the profile marker), C1b (#203/#218, the lean image), C1c (#204, the slim host-agent), C2 (#205, the boot proof), C3a (#206/#220, the seed + `/setup` gate), C3b (#207, the wildcard cert), and the recommends/nftables lean-check fixes (#237/#241/#243).

## What was done

Tooling-only (`dev/cloud/`, `Makefile`, `.gitignore`); no Go, no brain/UI change. A **pure relocation**: the boot-proof test image already booted + served (#205/C2, #209/C5); its wiring just lived in the wrong directory. The refactor moves the generic wiring down into the production config and leaves only the serial self-check in the test lane, so the resulting **test image is byte-for-byte the same** image it was before — what changes is that `make build-cloud-image` now produces that same wiring (minus the self-check).

- **New `dev/cloud/stage-control-plane.sh`** — one sourced helper, `stage_control_plane()`, that builds + stages the runtime wiring into `dev/cloud/mkosi.extra.wiring/` (generated, gitignored): the slim host-agent (`-tags hosted`) + its unit + the cloud-brain drop-in, the PAM stack, the control-plane image bundle (brain/ui/proxy + the xcaddy `caddy-dns/acmedns` Caddy) + the first-boot loader, the control-plane compose + `caddy.json`, and the provisioning-seed materializer + its unit. Both lanes source it, so the wiring cannot drift between the production image and the image the test lane validates. Logic moved verbatim from the old test bootstrap.
- **New `dev/cloud/mkosi.postinst.chroot`** — the production first-boot enablement (moved from the test postinst, minus the self-check): enable docker/containerd/`systemd-networkd`/host-agent/`malmo-load-images`/`malmo-seed`, disable `networkd-wait-online`, write the `05-docker-unmanaged` + `20-dhcp` `.network` files, provision the `malmo`/`malmo-app`/`malmo-shared` identities, and pre-commit machine-id + mask `systemd-firstboot`.
- **`dev/cloud/mkosi.conf`** — adds `ExtraTrees=mkosi.extra.wiring` (the generated tree) alongside the committed `mkosi.extra` (the `/etc/malmo/profile=hosted` marker). Header/scope comments updated (it no longer "deliberately does NOT bake host-agent/brain bring-up").
- **`dev/cloud/bootstrap.sh`** — now stages the wiring (via the shared helper) + the Docker apt repo before `mkosi build`, then runs the unchanged lean check + marker source-sanity. Needs root (control-plane image build + mkosi disk ops) and resolves the caller for the unprivileged go/mkosi sub-builds — same machinery the test lane already used.
- **`dev/cloud/test/bootstrap.sh`** — slimmed: calls the shared staging, then stages only the assertions (`cloud-assertions.sh` + its unit) into `dev/cloud/test/mkosi.extra/`. `CANARY_VERSION` v15→v16 (structure changed → force a clean rebuild).
- **`dev/cloud/test/mkosi.postinst.chroot`** — slimmed to enabling the `malmo-cloud-assertions` oneshot only. The production wiring postinst runs for this lane too: `dev/cloud/test/mkosi.conf` does `Include=..`, and mkosi `chdir`s into the included directory and re-runs its auto-detection (`mkosi/config.py` `parse_config_one`, the Include branch), so `dev/cloud/mkosi.postinst.chroot` + `dev/cloud/mkosi.extra.wiring/` are picked up for the boot-proof build as well.
- **`dev/cloud/malmo-seed-materialize.sh` + `malmo-seed.service`** — `git mv`d from `dev/cloud/test/` to `dev/cloud/` (production owns them; the test lane inherits via the wiring tree). The materializer's header notes the SMBIOS channel is the only one wired and the real-cloud channel is the #246 follow-up.
- **`Makefile`** — `build-cloud-image` now runs under `sudo -E` (it builds control-plane images + does mkosi disk ops); help text updated.
- **`.gitignore`** — ignore the generated `dev/cloud/mkosi.extra.wiring/`.

## How it maps to the specs

- `ENVIRONMENT.md` # Provisioning & first-boot — new "Production image first-boot wiring (realized, #242)" bullet under # Admin bootstrap — as built records the promotion and that the image stays lean by the package manifest. # Networking & discovery's "single interface brought up by the minimal cloud-native path" is now realized (networkd DHCP, no NetworkManager).
- `TESTING.md` # Hosted cloud variant — updated to state the test lane boots the exact production image plus a serial self-check, not a test-only superset.

## Known gaps & deviations

- **No local build/boot verification.** mkosi 26 on Ubuntu 24.04 hits the `PR_CAPBSET_DROP` EPERM blocker (#189), so `make build-cloud-image` / `make test-cloud-qemu` cannot run on the maintainer box. Verification is by source review + the test-image-identity invariant (the wiring is the same, proven-booting content, just relocated) + `bash -n` on every script. Real green-build + Hetzner-boot acceptance is the **cloud#6 / CL6 live run**, the same deferral C3a (#206) and C4 (#208) took.
- **Real-cloud seed channel is out of scope (split to #246).** The promoted `malmo-seed.service` reads the seed only from the **SMBIOS / systemd-credential** channel. On Hetzner the seed arrives via the metadata service, not SMBIOS, so a real box comes up with the control plane but `/setup` stays 503 until #246 adds the metadata channel + the first-boot network ordering it needs. The SMBIOS seed service no-ops safely without a credential, so promoting it now is harmless; #246 is the last mile and is itself a CL6-verified, cross-repo (cloud#3 wire contract) change.
- **`make build-cloud-image` now needs root + docker + go.** A deliberate contract change (the image is no longer a lean base only): the cloud repo's `deploy/hetzner-image/build-and-upload.sh` must invoke it accordingly (it self-sudos via the Makefile). Flagged for cloud-side coordination.
- **nftables stays off the lean cut list** (the temporary #243 state, owned by #241/#245) — untouched here.

## What's next

- **#246** — ingest the seed from the real-cloud (Hetzner) metadata/user-data channel + resolve the first-boot network ordering; verified at CL6.
- **cloud#6 / CL6 live run** — boot a real seeded Hetzner VM from a `make build-cloud-image` image: confirm network, control plane up, seed ingested, and `<box-id>.malmo.network` served over HTTPS. Record the result.
- **Coordinate `deploy/hetzner-image/build-and-upload.sh`** (cloud repo) with the heavier, root-requiring `make build-cloud-image`.
