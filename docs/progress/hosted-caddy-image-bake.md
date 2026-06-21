# Bake the caddy-dns/acmedns Caddy into the hosted image + plumb MALMO_CADDY_IMAGE (CL6 prep)

- **Status:** done — box-side build/plumbing; real wildcard issuance is verified jointly in the cloud on-ramp (cloud CL6 / #6), not the QEMU lane (air-gapped, no real ACME)
- **Date:** 2026-06-21
- **Specs touched:** none (realizes the deferred half of [hosted-wildcard-cert.md](hosted-wildcard-cert.md) — "bake the custom Caddy image + set MALMO_CADDY_IMAGE in the hosted run-spec")

Closes the gap C3b ([hosted-wildcard-cert.md](hosted-wildcard-cert.md), #207) left for CL6: it shipped the box-side wildcard-cert machinery and the custom-Caddy *recipe* (`dev/control-plane/caddy-acmedns/Dockerfile`, xcaddy + `caddy-dns/acmedns`), but **nothing built that image, baked it into the hosted cloud image, or set `MALMO_CADDY_IMAGE`** — so a booted hosted box still ran stock `caddy:2-alpine`, which has no DNS-01 provider and cannot obtain the `*.<box-id>.malmo.network` wildcard cert. The hosted image was effectively unable to serve HTTPS. This unblocks the cloud-side CL6 live on-ramp (the joint convergence with cloud `malmoos/cloud` #18), which is the first and only real test of issuance.

## The missing link: host-agent never forwarded MALMO_CADDY_IMAGE

The control-plane `compose.yml` already selected `${MALMO_CADDY_IMAGE:-caddy:2-alpine}`, and the brain's `ControlPlaneUp` runs `docker compose` with the brain process's inherited environment — so on paper "set the env var and compose picks it up." But tracing the actual flow showed the var had **no path to the brain's environment**: host-agent curates the brain container's env in `brainlaunch.runSpec` (it does not pass its own env through wholesale), and `MALMO_CADDY_IMAGE` was not on that list. Setting it in the systemd drop-in alone would have been inert. So the fix is a small Go plumbing change plus the image bake.

## What was done

### Go: forward MALMO_CADDY_IMAGE host-agent → brain → compose (`internal/hostagent/brainlaunch`, `cmd/host-agent-real`)

- `brainlaunch.Config` gains `CaddyImage`; `runSpec` appends `MALMO_CADDY_IMAGE` to the brain container's env **only when non-empty** — so an unset value (appliance, `make dev`) leaves the control-plane compose on its stock-`caddy:2-alpine` default (the appliance does no ACME), and only the hosted profile flips it. Same conditional-emit idiom as `MALMO_OFFLINE_INSTALL` / `MALMO_CATALOG_DIR`.
- `cmd/host-agent-real` reads `MALMO_CADDY_IMAGE` (default empty) into `Config.CaddyImage`.
- The brain's `ControlPlaneUp` does not set `cmd.Env`, so `docker compose` inherits the brain's environment and substitutes `${MALMO_CADDY_IMAGE}` — no further change needed there.
- Tests: `brainlaunch_test.go` asserts the var is **absent** when `CaddyImage` is empty (the appliance/dev default) and **present** when set.

### Cloud lane: build + bake the custom Caddy (`dev/cloud/test/bootstrap.sh`, `dev/cloud/cloud-assertions.sh`)

- `bootstrap.sh` builds the xcaddy recipe (`docker build dev/control-plane/caddy-acmedns/` → `malmo-caddy-acmedns:dev`) and `docker save`s it **over the staged `caddy.tar`** — the copy under `mkosi.extra/`, **not** the shared `.dev/control-plane` bundle, which the appliance/medium lane (`test-medium-qemu`) keeps on stock Caddy. The first-boot loader docker-loads every `*.tar` regardless of filename, so the box loads `malmo-caddy-acmedns:dev` offline; the build runs on the build host (the VM never pulls — the air-gap is unaffected).
- The `10-cloud-brain.conf` host-agent drop-in sets `Environment=MALMO_CADDY_IMAGE=malmo-caddy-acmedns:dev`, the value `cmd/host-agent-real` reads.
- `cloud-assertions.sh`'s "four baked images loaded" check expects `malmo-caddy-acmedns` instead of `caddy` (the loaded image's Repository changed with the swap). The running-container check (`malmo-caddy`, the compose `container_name`) is unaffected.
- `CANARY_VERSION` v12 → v13 to force a clean rebuild.

## How it maps to the specs

- Realizes the deferred bake/plumbing from [hosted-wildcard-cert.md](hosted-wildcard-cert.md) (# What's next: "Bake the custom Caddy image into the hosted cloud image build + offline bundle and set `MALMO_CADDY_IMAGE` in the hosted run-spec"). No new contract — `MALMO_CADDY_IMAGE` and the recipe were defined there; this wires them.
- Keeps `ENVIRONMENT.md` # Networking & discovery intact: the hosted box obtains the `*.<box-id>` wildcard via ACME DNS-01 against its seeded acme-dns account, now with a Caddy that actually has the module.
- Appliance is byte-for-behavior unchanged (empty `CaddyImage` ⇒ no env var ⇒ stock Caddy), consistent with C3b's "appliance keeps stock caddy".

## Known gaps & deviations

- **Real issuance still unverified in-repo.** The QEMU cloud lane is air-gapped (`restrict=on`), so it proves the custom Caddy image loads and the control plane comes up with it — **not** that a cert issues (no Let's Encrypt/DNS reachable). Real `*.<box-id>` issuance is the cloud-side CL6 live run (`malmoos/cloud` #18, `docs/ops/e2e-onramp.md`). Re-running `sudo -E make test-cloud-qemu` here is a no-regression gate, not a cert test.
- **VM-boot acceptance pending.** The Go tests + `bash -n` are green and the build is clean, but `make test-cloud-qemu` (which rebuilds the image with the baked Caddy) has not been run in this slice's environment — the same VM-gated posture as the prior cloud-lane entries.
- **Caddy `/data` not persisted** in the control-plane compose (carried from C3b): a Caddy-only restart drops the issued cert until the brain re-asserts `EnsureWildcardTLS` on its next restart. Unchanged here; same class as #187.
- **xcaddy build needs build-host network** (Go module fetch) — fine, it runs where `make control-plane-images` already does; Docker's layer cache makes repeat builds cheap.

## What's next

- **Cloud CL6 joint live run** (`malmoos/cloud` #18): boot a real seeded Hetzner VM from this image, confirm Caddy obtains the wildcard cert and serves the dashboard + an app over real HTTPS at `<box-id>.malmo.network`. Record the result.
- **Re-run `make test-cloud-qemu`** on the maintainer env to confirm no boot/serve regression with the baked custom Caddy.
- **Persist Caddy `/data`** (a `caddy_data` volume) when #187's Caddy-reconnect handling is taken, so a Caddy bounce doesn't drop the cert.
