# Hosted first-boot seed delivery + `/setup` gate end-to-end (C3a cloud-lane)

- **Status:** done — **VM-boot acceptance PASSED** on the maintainer env (`sudo -E ./dev/cloud/run-cloud-tests.sh`, 2026-06-20, accel=kvm): all three boots report `MALMO_CLOUD_ASSERTIONS: PASS` — un-seeded `/setup` 503, seeded wrong-secret 401 + correct-secret 200 with `box_id=cindy-fox`, and a frozen-identity reboot where a re-delivered different seed (`rusty-hawk`) is ignored and `/login` still reports `cindy-fox`
- **Date:** 2026-06-20
- **Specs touched:** `ENVIRONMENT.md` (# Admin bootstrap — as built: the "Deferred" seed-onto-a-VM bullet flipped to realized), `TESTING.md` (# Full-stack control-plane integration — cloud-lane seed delivery + 3-boot scenario documented)

Completes the deferred half of C3a (#206, [hosted-setup-gate.md](hosted-setup-gate.md)) and extends the C2 boot harness ([cloud-vm-boot-proof.md](cloud-vm-boot-proof.md), #205). C3a built the brain-side seed ingestion + the `/setup` admin-bootstrap gate and unit-tested it, but nothing delivered `seed.json` onto a real booted VM and the gate had never run end-to-end. This issue (#220) is that missing half: deliver the seed the cloud-init way (a systemd credential over SMBIOS) and assert all four gate properties in QEMU. **No brain/Go change** — the brain already reads `/var/lib/malmo/seed.json` (overridable via `MALMO_SEED_PATH`) when `profile == hosted`, and `brainlaunch.runSpec` already bind-mounts `/var/lib/malmo` into the brain container, so a seed written to that host path before host-agent launches the brain is exactly what the containerized brain reads.

## What was done

### Seed materialization on the VM (systemd + shell)

- **`dev/cloud/test/malmo-seed.service`** — a first-boot oneshot, `Before=host-agent.service`, that `ImportCredential=malmo.seed` and runs the materializer. Ordered before host-agent so the seed is on disk before the brain (which host-agent launches) ingests it at startup.
- **`dev/cloud/test/malmo-seed-materialize.sh`** — `install -m 0600 -o root:root` the delivered credential to `/var/lib/malmo/seed.json`; no-op (exit 0) when no credential was delivered, so an un-seeded boot leaves the box unprovisioned and `/setup` returns 503. It deliberately overwrites any existing file (no "don't clobber" guard): the frozen-identity boot re-delivers a *different* seed, and overwriting makes the "the brain ignores it" assertion non-vacuous (the brain's identity lives in SQLite, not on this file).
- Enabled in `dev/cloud/test/mkosi.postinst.chroot` and staged in `dev/cloud/test/bootstrap.sh` (`CANARY_VERSION` bumped v11 → v12 to force a clean rebuild).

### Delivery + multi-boot assertion (QEMU harness)

- **`dev/cloud/run-cloud-tests.sh`** — refactored the single boot into a `run_boot <phase> <mode> [seed-cred…]` helper and a **3-boot sequence over one persisted qcow2 overlay** (so the brain's box-id + first admin survive boot→boot; the base artifact stays pristine). The seed is delivered as an SMBIOS type-11 systemd credential — `io.systemd.credential.binary:malmo.seed=<base64 JSON>` (binary/base64 so the JSON is comma/newline-safe; the same channel the medium lane uses for the LUKS passphrase, and the analogue of the production cloud-init `write_files`). The scenario to assert is delivered as a second credential, `io.systemd.credential:malmo.assert=<mode>`. The three boots:
  1. **un-seeded** — no seed → `/setup` ⇒ 503 (the standalone C2 control-plane-up proof, now phase 1).
  2. **seeded** — seed A (`box_id=cindy-fox`, random secret) → wrong secret ⇒ 401, correct secret ⇒ 200 with `box_id` in the body; first admin created.
  3. **frozen** — a *different* seed B (`box_id=rusty-hawk`) over the same overlay → the brain loads its persisted box-id and ignores the re-delivered seed; `/login` still reports `cindy-fox`.
- **`dev/cloud/cloud-assertions.sh`** — kept the control-plane-up checks (steps 1–8), replaced the single 503 check with a **mode switch** read from the `malmo.assert` credential (`unseeded` | `seeded` | `frozen:<box-id>`). Added a body-capturing `http_post` and a `json_str` field extractor (no `jq` in the lean image; the box-id assertions read the `/setup` and `/login` 200 bodies, which carry `box_id` via `fullUserDTO`). On PASS the script powers the box off cleanly (`systemctl --no-block poweroff`) so the brain's SQLite box-id write flushes to the overlay before the next boot — the serial-only analogue of the medium lane's SSH `systemctl poweroff`.
- **`dev/cloud/test/malmo-cloud-assertions.service`** — added `ImportCredential=malmo.assert` and **dropped the run-once `ConditionPathExists` marker** so the self-check runs on every boot of the sequence (this unit exists only in the boot-proof test image, never the lean production cut).

## How it maps to the specs

Realizes `ENVIRONMENT.md` # Admin bootstrap — as built: the seed is `{box_id, admin_bootstrap_secret, enrollment}` JSON at `/var/lib/malmo/seed.json`; the gate returns 503 unprovisioned, 401 wrong/missing secret, 200 on the correct secret (first admin), and the box-id is the install's **frozen identity** — a re-delivered or changed seed cannot re-key a provisioned box (`MALMO_NETWORK.md` "name frozen at enrollment"). The SMBIOS-credential delivery is the test-lane analogue of the production cloud-init path; both converge on the same on-box file. Extends `TESTING.md` # Full-stack control-plane integration (the cloud lane, serial-driven, no SSH).

## Known gaps & deviations

- **VM-boot acceptance passed**, but on **one** maintainer env (Ubuntu 20, KVM). #189 (the mkosi-26/Ubuntu-24.04 sandbox EPERM) does not apply here but still blocks this lane in CI / on a 24.04 box; the cloud lane is not yet wired into automated CI (same status as the medium lane).
- **`enrollment` still unconsumed.** The seed carries `enrollment` as reserved raw JSON (C3a). This issue does not consume it or pin its sub-shape; the wildcard-cert/DNS-01 pass (C3b) owns that. The seeds the harness generates omit it (it is optional in `ReadSeed`).
- **Cross-repo seed schema.** The canonical keys are `box_id` / `admin_bootstrap_secret` / `enrollment` (snake_case, owned by `internal/profile.Seed`). That type is in `internal/` and thus not importable by the cloud producer repo — making it importable (or pinning a shared contract) is a cross-repo design item, surfaced on the issue, **not** changed here.
- **`frozen` re-delivery distinctness** is a warning, not a hard failure: if seed B's box-id ever equalled A's, the identity assertion would still hold but be weaker. The harness uses distinct ids (`cindy-fox` vs `rusty-hawk`) so this never triggers.
- **Clean-shutdown timeout (self-review note).** `run_boot`'s PASS path waits 60s for the guest's clean poweroff, then force-kills QEMU; if a shutdown ever exceeded that under load, the kill could truncate the box-id flush and make boot 3's frozen check flake. Low probability for this minimal image (the real run shut down cleanly well inside the window), so left as-is — the first thing to revisit if the lane goes flaky in CI.

## What's next

- Wire the cloud lane into automated CI (blocked by #189 on 24.04 runners — shared with the medium lane).
- C3b: consume `enrollment` — Caddy wildcard cert for `*.<box-id>.malmo.network` via ACME DNS-01 from the seeded credentials (`ENVIRONMENT.md` # Networking & discovery).
- C4/C5: the trimmed hosted setup wizard + the seed→wizard→dashboard end-to-end.
