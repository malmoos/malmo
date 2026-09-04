# Persist Caddy's certificate store across a container recreate

- **Status:** done
- **Date:** 2026-09-04
- **Specs touched:** docs/specs/CONTROL_PLANE.md, docs/specs/ENVIRONMENT.md

Closes [#433](https://github.com/malmoos/malmo/issues/433). Closes the gap [hosted-caddy-image-bake.md](hosted-caddy-image-bake.md) recorded under "Known gaps & deviations" and carried from [hosted-wildcard-cert.md](hosted-wildcard-cert.md): the control-plane `caddy` service mounted only `caddy.json`, so Caddy's `/data` — the ACME account key and every issued certificate — lived in the container's writable layer and died with the container.

## What was done

### The compose change (`dev/control-plane/compose.yml`)

- `/data` is now the named volume `malmo-caddy-data`. It carries an explicit `name:` so the volume is stable whatever compose project the file is run under, the same idiom the `malmo-ingress` network already uses in this file.
- `/config` is a `tmpfs`, matching what `malmo-ui` right below it already does. Caddy really does write there — it saves `/config/caddy/autosave.json` at startup and again on every admin-API config change — but nothing ever reads it back: the container runs `caddy run --config /etc/caddy/caddy.json`, an explicit config, and never `caddy run --resume`. So `/config` has to be writable and is worth nothing across a recreate. The issue asked whether Caddy writes there; it does, and it still should not be a volume.

Config is deliberately the opposite of certificate state. Caddy's routes and the hosted TLS policy are set through the admin API and are re-asserted by the brain on its own startup (`Reconcile`, `EnsureCatchAll`, `EnsureWildcardTLS`), so losing them costs a re-PUT. A certificate cannot be re-asserted — it has to be re-issued.

### The measurement the issue asked for

Step 1 of the issue said to measure rather than read. Against real Docker, using the committed file:

- With the volume: write a probe into `/data`, `docker compose up -d --force-recreate caddy`, container id changes, probe is still there. Caddy's own `/data/caddy/` tree (instance uuid, locks, `last_clean.json`) survives with it.
- The control, stock `caddy:2-alpine` with no volume: the same probe is gone after the same recreate.
- `/config` on the new `tmpfs`: probe gone after recreate, which is the intent.

What happens on a hosted box after a Caddy-only recreate, read from the code: Caddy comes back on the bootstrap `caddy.json` alone. That config has no `tls` app and no `:443`, so the box serves nothing on HTTPS and places no ACME order at all. Issuance restarts when the **brain** next starts and re-runs `EnsureWildcardTLS` (`cmd/brain/main.go`) — there is no Caddy-reconnect path today, which is [#187](https://github.com/malmoos/malmo/issues/187). So the cost of a lost `/data` is not "a slow reload", it is a full re-issuance on the next brain start.

### The rate-limit finding (step 2 of the issue)

The exposure is fleet-wide, not per box:

- Let's Encrypt counts **50 new certificates per registered domain per 7 days**, plus **5 duplicate certificates (an identical identifier set) per 7 days**.
- "Registered domain" is resolved through the Public Suffix List. `malmo.network` is **not** on the PSL (checked against the published list), so it is one registered domain and **every hosted box shares that one 50-per-week budget**.
- A cold box costs **two** of those: the wildcard `*.<box-id>.malmo.network` via acme-dns DNS-01, and the apex `<box-id>.malmo.network` via Caddy's default issuer.
- Renewals coordinated by ARI are exempt from all rate limits. A re-issuance after a lost `/data` is **not** a renewal — there is no prior cert to renew from — so it gets no exemption.

Two consequences. Per box, a recreate loop hits the 5-duplicates-per-week wall and locks that box out of HTTPS for up to a week. Fleet-wide, ~25 cold boxes a week is the ceiling on the shared budget, and every avoidable re-issuance is spent out of the same pot as a real new customer's first boot. That is more than a tidy-up, and it is the part the original progress note did not weigh. Posted on the issue as the step-2 answer.

### Sequencing with #187 (step 4)

They are independent and this one landed alone. #187 is about network topology — apps off `malmo-ingress`, Caddy joining each per-app network, and re-attaching those dynamic connections after a Caddy restart. That is brain-side reconnect work. This is one volume in a compose file. The pairing in the old progress note was "do it while you are in there", not a dependency.

[#431](https://github.com/malmoos/malmo/issues/431) names this issue as the blocker for `read_only: true` on the Caddy container. Both of Caddy's write paths now have somewhere to go (`/data` volume, `/config` tmpfs), so that half of #431 is unblocked. Nothing else from #431 is done here.

### The appliance (step 5)

Nothing changes in behaviour. The appliance runs no ACME — plain-HTTP `.local` — so its `/data` holds only Caddy's local CA and instance id. Persisting it is free rather than load-bearing, and it keeps one compose file for both profiles instead of a profile-conditional mount. Both lanes bake this compose into their image, so both canaries are bumped (`dev/cloud/test/bootstrap.sh` v19 → v20, `dev/test-qemu/bootstrap.sh` v27 → v28) — without that a local re-run would silently test the previously built image.

### Cloud-lane assertion (`dev/cloud/cloud-assertions.sh`, seeded boot)

The issue's "Done when" asks to verify by watching for the absence of an ACME issuance. The lane is air-gapped (`restrict=on`), so no issuance ever happens there and its absence proves nothing. What the lane can prove is the property that absence rests on, and it now does, as check (c) after the two existing wildcard-TLS checks:

- `malmo-caddy`'s `/data` is a `volume` mount named `malmo-caddy-data`, and that volume exists.
- The mount is live, not merely declared: a file written from inside `malmo-caddy` under `/data` is read back from a **separate** container mounting the same volume. That is the difference between the store being in the volume and being in the container's layer. The probe container runs the image `malmo-caddy` itself runs, so nothing is pulled in the air gap, and the probe file is removed after.

It runs in the publish gate, since `seeded` is one of the gated boots.

## How it maps to the specs

- `CONTROL_PLANE.md` # Locked: Caddy is malmo substrate gains the split as a bullet: certificate state persists, config does not, and why the two are opposite.
- `ENVIRONMENT.md` # Networking & discovery — as built gains the same fact where the wildcard mechanism is described, with the shared-registered-domain budget spelled out, since that is what makes it matter on hosted. The adjacent "Wildcard cert via acme-dns" bullet is corrected in the same pass to the two-certificate, two-issuer model `DECISIONS.md` 2026-07-08 settled — the section could not carry both readings once this change said "two certs" beside it.
- No locked decision flips, so no `DECISIONS.md` entry.

## Known gaps & deviations

- **No real ACME anywhere in this change.** The re-issuance cost is derived from the code path and Let's Encrypt's published limits, not observed on a live box. Real issuance stays the cloud on-ramp's job, unchanged.
- **The cloud-lane assertion does not recreate `malmo-caddy`.** Recreating it mid-run would drop every admin-API route the earlier assertions depend on. The check proves the volume is real and live and leaves "a named Docker volume outlives its container" as Docker semantics, which the local real-Docker run above did exercise directly.
- **The QEMU lanes were not run here.** Both are VM-gated and this environment does not run them; the compose file is validated with `docker compose config` and exercised against real Docker, and the shell change passes `bash -n`. Same posture as the other cloud-lane entries.
- **The stale "one challenge issues the combined cert" line in `ENVIRONMENT.md` was fixed here after all.** It was first left out of scope and filed as [#437](https://github.com/malmoos/malmo/issues/437). Greptile then pointed out that the new bullet added by this change sits directly beside it and says the opposite, so the contradiction is one this PR introduced, not one it inherited. The wildcard bullet is now rewritten to what `EnsureWildcardTLS` does today: two certificates, `certificates.automate` naming the wildcard (the automate entry is what schedules the order; the policy only says which issuer), the acme-dns DNS-01 challenge solved at the delegated apex `_acme-challenge.<box-id>`, and the apex host left to Caddy's default issuer over tls-alpn-01 — so one order touches acme-dns and the two cannot contend. #437 is closed by this PR.
- **No already-deployed box re-issues because of this change** — the self-review raised it, and it is worth writing down because it rests on an assumption that was not recorded anywhere. Switching `/data` to a fresh volume does cost a recreate and a lost cert *on any box whose compose file is replaced*. No update path replaces it. `/var/lib/malmo/control-plane/compose.yml` is written into the image at build time (`dev/cloud/stage-control-plane.sh`), and the only thing that writes it at runtime is `controlplane.RewriteUIImage`, which edits the single `malmo-ui` `image:` line. Stream B updates the brain and UI images, not this file; stream A is `apt`, and no package ships it (`UPDATES.md` # Two streams). So this compose reaches a box only when that box is **created** from a new image, and such a box has no certificate to lose. The finding would become real when A/B images ship (`SPEC.md`, deferred to v2), where replacing the disk does replace the compose — but an A/B swap recreates the control plane wholesale anyway, so it is that transition's cost to weigh, not this change's.
- **`/config` is a `tmpfs`, not a volume.** If a future change ever adds `caddy run --resume`, that call has to be revisited; the comment in the compose file says so.

## What's next

- **[#431](https://github.com/malmoos/malmo/issues/431)** can take `read_only: true` on the Caddy container now that both write paths are mounted.
- Nothing outstanding from this slice. The `ENVIRONMENT.md` correction that was going to be [#437](https://github.com/malmoos/malmo/issues/437) is included here instead.
