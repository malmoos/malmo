# Cloud-image end-to-end test — seed → gate → first-run wizard → served dashboard (C5)

- **Status:** implemented; **VM-boot acceptance pending on the maintainer env.** The mkosi build + QEMU boot are blocked on this contributor box (#189, mkosi-26 / Ubuntu-24.04 `PR_CAPBSET_DROP` sandbox EPERM), so the lane was authored and statically verified here (`bash -n` clean; it reuses the C2/#220 boot harness whose 3-boot PASS shape already passed on the maintainer env) but **not executed**. The acceptance bar is `sudo make test-cloud-qemu` green on the maintainer env (KVM), per the C2 (#205, [cloud-vm-boot-proof.md](cloud-vm-boot-proof.md)) and #220 ([cloud-seed-delivery.md](cloud-seed-delivery.md)) precedent.
- **Date:** 2026-06-21
- **Specs realized:** `TESTING.md` (# Full-stack control-plane integration — the cloud lane now drives the first-run wizard to completion, not only the `/setup` gate), `FIRST_RUN.md` (# Phase 2/Phase 3 — the trimmed hosted wizard's steps + the first-run-complete marker asserted end-to-end), `ENVIRONMENT.md` (# Provisioning & first-boot — the seed → admin → served-box arc). No spec file edited (the flows already match as-built); tooling-only beyond this entry.

Closes the cloud track's boot-to-served-dashboard arc (#209/C5), the "boots as a proper hosted OS" bar that C2 (#205, control plane up) + C3a (#206/#220, seed + `/setup` gate) + C4 (#208, the trimmed wizard) were building toward. C2 + #220 already boot the hosted image, deliver the seed over SMBIOS, and assert the admin-bootstrap gate's four properties; this slice extends the **seeded** boot past first-admin creation through the rest of the wizard, and the **frozen** reboot to prove the wizard does not reappear. No new product capability — it proves the ones already built compose into a working box.

**Chained on C4 (#208/PR #229), not `main`.** C5 drives C4's `POST /api/v1/system/{timezone,telemetry,first-run-complete}` endpoints and the extended `GET /auth/state`, none of which are in `main` yet, so this branch is cut from the C4 branch (rebased onto current `main` so it also carries the C2/#220 cloud lane). The PR targets the C4 branch; it retargets to `main` once C4 merges.

## What was done

All changes are in `dev/cloud/` (tooling) plus this entry — no Go/web-ui change.

### End-to-end wizard assertions (`dev/cloud/cloud-assertions.sh`)

The in-VM, serial-driven self-check gains, in the **seeded** scenario, the wizard-completion block that runs after the existing correct-secret `/setup` 200 (first admin created):

- **PAM `/login`** — the freshly-created admin authenticates against `/etc/shadow` via host-agent `verify-password` (the real PAM path, distinct from the seed secret), and the `malmo_session` cookie is kept for the admin-gated wizard steps.
- **Wizard step — time zone** — `POST /system/timezone {"timezone":"Europe/Stockholm"}` ⇒ 200. This is a **real** pass-through to host-agent `timedatectl set-timezone` in the hosted build (`a.Timezone = timezone.New()` in `wiring_hosted.go`), so it proves the step works on the real box, not only the fake host-agent. Its status is the first C4-specific signal the lane sees, so the failure message discriminates the two likely setup mistakes (404 ⇒ stale pre-C4 brain image; 502 ⇒ tzdata missing — see below).
- **Wizard step — telemetry** — `POST /system/telemetry {"enabled":false}` ⇒ 200 (the founding admin's box-level choice; `TELEMETRY.md`).
- **Wizard step — Done** — `POST /system/first-run-complete` (bodyless) ⇒ 200 with `first_run_complete:true`.
- **Wizard will not reappear** — the public `GET /auth/state` now reports `first_run_complete:true` (the dashboard gates the wizard on this flag, not `has_users`).
- **box-id persisted/surfaced** — `GET /me` (authenticated) carries the provisioned `box_id`, proving it is persisted on the brain identity, not merely echoed by the one-shot `/setup` response.

The **frozen** reboot scenario additionally asserts `first_run_complete` survives the power-cycle (the marker written on the seeded boot persists across an actual reboot — the wizard does not reappear after a restart).

Added cookie-aware HTTP helpers in the existing `/dev/tcp` idiom (no curl/jq in the lean image): `session_cookie` (extract `malmo_session=<tok>` from `Set-Cookie`), `auth_post` (cookie + JSON or bodyless), `auth_get`, `status_of`.

### `tzdata` added to the lean hosted image (`dev/cloud/mkosi.conf`) — a finding

The wizard's time-zone step is a real `timedatectl set-timezone`, which **rejects any zone not present under `/usr/share/zoneinfo`**. systemd only *Recommends* `tzdata` and mkosi builds with `Install-Recommends=false`, so the lean image shipped no zoneinfo — the wizard's time-zone step would 502 on a real hosted box. Driving the wizard end-to-end surfaced this; `tzdata` is now an explicit dependency. It is not on the appliance cut-list (`network-manager`/`avahi`/`samba`/`mergerfs`/`cryptsetup`/`tpm2-tools`/`openssh-server`/`nftables`), so the lean-check still passes.

### Harness wiring (`dev/cloud/run-cloud-tests.sh`, `dev/cloud/test/bootstrap.sh`)

- `run-cloud-tests.sh` — header + per-boot narration updated to describe the wizard; **no driver logic change** (the seed delivery + 3-boot overlay + serial verdict grep are reused verbatim from C2/#220). The verdict mechanism (`MALMO_CLOUD_ASSERTIONS: PASS`) is unchanged.
- `dev/cloud/test/bootstrap.sh` — `CANARY_VERSION` v12 → v13 to force a clean image rebuild (the baked `cloud-assertions.sh` + the `tzdata` package both changed).
- The `make test-cloud-qemu` target (added by C2) already runs the driver; no Makefile change needed.

## Known gaps & operational notes

- **VM-boot acceptance not run here.** Blocked by #189 on this box; must be run on the maintainer env (`sudo make test-cloud-qemu`, KVM) to land green, as C2/#220 were. Until then this is implemented-but-unproven.
- **The baked brain image must include C4.** `dev/cloud/test/bootstrap.sh` reuses an existing `.dev/control-plane` bundle unless `MALMO_REBUILD_CP=1`. A bundle left over from a pre-C4 run carries a brain without the `/system/*` wizard endpoints, so the first C5 run needs `sudo -E MALMO_REBUILD_CP=1 make test-cloud-qemu` (or a clean `.dev/control-plane`) to rebake the brain. The time-zone step's `404` failure message points at this.
- **No telemetry/timezone read-back beyond the endpoints.** The lane asserts the wizard's writes return 200 and that `first_run_complete`/`box_id` are surfaced; it does not separately read `/etc/localtime` or the telemetry consent value back (the endpoints' own 200 + the brain unit tests in C4 cover persistence). Adequate for the end-to-end bar; a deeper read-back is possible later if needed.
- **Single env, no CI.** Like the medium lane and the C2/#220 cloud lane, this is not wired into automated CI (blocked by #189 on 24.04 runners).

## What's next

- Run `sudo make test-cloud-qemu` on the maintainer env and record the PASS (the acceptance bar) once C4 (#229) merges and this retargets to `main`.
- Wire the cloud lane into automated CI (shared blocker #189).
- B-track (#196 B4/B5): the same wizard shell gains the network/WiFi step for bare metal; the bare-metal end-to-end lane (B5) mirrors this one over the swtpm+LUKS rig.
