# Hosted per-app access modes — end-to-end cloud lane + cookie-leak probe

- **Status:** done
- **Date:** 2026-07-09
- **Specs touched:** `docs/specs/TESTING.md` (# Hosted cloud variant — the boot-proof lane gains a fourth `access` boot with a harness-held test portal)

Final slice of the hosted per-app access-restriction epic (#304), closing **#308** and building on the mechanism the three prior slices shipped: the brain forward-auth verify endpoint + domain-wide cookie (#305, `hosted-forward-auth-verify.md`), the per-app `forward_auth` + Cookie-strip route + owner-only default (#306, `hosted-forward-auth-route.md`), and the dashboard Only-me / Public toggle (#307, `hosted-access-toggle.md`). Those slices' runtime shape was only asserted structurally (against the recording admin fake in #306); this slice proves both access modes **end-to-end through real Caddy in a booted hosted box**, and lands the cookie-leak probe of the whole-header strip invariant — the coverage that (per #306's product call) proves the already-shipped `restricted` default rather than gating it.

## The problem: the box-only lane had no owner session

The hosted cloud lane (`dev/cloud/run-cloud-tests.sh` + `cloud-assertions.sh`) is a serial-driven QEMU boot-proof, air-gapped by design (`restrict=on`). It held **no portal private key**, so it could only exercise the SSO verifier's negative paths (a bad token 401s); `TESTING.md` scoped the positive path — a signed assertion → owner auto-create → box session — to the joint cloud on-ramp acceptance. But every per-app access assertion needs a real owner session: to **install** an app on hosted (the API is authenticated) and to prove the restricted click-through (the owner's forward-auth cookie must proxy through). So the box-only lane couldn't reach any of #308's requirements without one.

## The technique: the harness plays a test portal (zero brain changes)

The real portal-to-box trust model is exactly reproducible in a test: the box only ever receives the portal's **public** key (in its seed) and verifies genuine signatures. So the harness generates a throwaway Ed25519 keypair, seeds the box with the public key, and mints a valid owner assertion with the private key — which never enters the VM. The box verifies it through the same `internal/assertion.Verify` + `ssoLanding` policy path as a production assertion. **No brain code changes**: the brain already ingests a seed key, verifies assertions, and installs apps for an authenticated owner. The only new code is the test harness.

## What was done

### `dev/cloud/mkassertion` — the host-side minting tool

A small Go command the harness runs to print two lines: the standard-base64 seed public key, and a signed owner assertion token (`base64url(claims)."base64url(sig)`, the shape `internal/assertion` expects). Claims are minted to satisfy every box-side check (`iss == profile.NetworkApex`, `box == box-id`, non-empty `sub`/`email`/`jti`) with a generous `exp` (a real portal mints ~60s; this token must outlast a slow air-gapped boot). `main_test.go` round-trips a minted token through the real `assertion.Verify` **against the standard-base64-encoded-then-decoded key** — the exact harness→box wire path — so an encoding drift fails a unit test, not a 10-minute CI boot.

### `dev/cloud/run-cloud-tests.sh` — the `access` boot

A fourth boot scenario on its **own fresh overlay** and box-id (independent of the unseeded→seeded→frozen identity chain): mints the keypair + token, seeds the box with the test-portal key (`seed_cred_keyed`, factored out of the existing `seed_cred`), and delivers the token over a second SMBIOS credential (`malmo.sso_token`, binary-encoded). Kept in the default full run but **out of the publish gate** (like `frozen`) — it is an app-level e2e, not an image-schema regression check. `go` is resolved for the mint (sudo strips the caller's PATH) and the mint runs as the caller so it uses their warm build cache.

### `dev/cloud/cloud-assertions.sh` — the `access` scenario

After the shared control-plane-up + `/setup`-disabled prechecks, the in-VM self-check: drives `GET /_malmo/sso?token=…` **once** (the jti is single-use) and captures both `malmo_session` and `malmo_forward_auth` cookies; installs `whoami` air-gapped (`POST /api/v1/apps`); then asserts, through the box's own Caddy on `:80`:

- **the two-cookie safety model, on the wire** (#304's headline claim, and the one thing the prior slices left purely structural): from the real `Set-Cookie` headers the SSO landing returns, `malmo_session` carries **no `Domain`** (host-only — a `Domain` here would hand the *admin* session to every app subdomain for an app to replay as the owner) and `malmo_forward_auth` carries `Domain=<box-id>.malmo.network`. The lane already captured these headers but had only ever read their *values*; the attributes are what the whole design rests on;
- **restricted (the hosted default), with the owner's forward-auth cookie** → proxied through to the whoami echo (200, `Hostname:`), the `X-Malmo-User` identity header forwarded, and **no `Cookie` header** at the app upstream (the strip);
- **restricted, no session** → `302` to the box login (`https://<box-id>.malmo.network/`);
- **public (after `PUT /api/v1/apps/{id}/exposure`)** → reachable with no session (200);
- **public + a forward-auth cookie** → **still** stripped before the app (a public app must never receive the Domain-scoped cookie, or it could replay it against the owner's restricted apps — the reason #306 strips the whole `Cookie` header on every hosted route).

An extra throwaway cookie rides alongside the forward-auth cookie in both leak probes, so the assertion proves a **whole-header** strip, not just the removal of the one named cookie.

### Test-lane fixtures (`dev/cloud/test/`, boot-proof image only)

The lean production image ships no app; these are staged into the test ExtraTree (`dev/cloud/test/mkosi.extra/`), never the shared production wiring: the `traefik/whoami:v1.10.3` image tarball (loaded alongside the control-plane bundle — the first-boot loader globs `*.tar`); a pre-seeded last-good catalog snapshot generated by `mkcatalog` from a minimal hosted whoami package (`dev/cloud/test/catalog/whoami`, pure routing, no folder grant); and a host-agent drop-in (`20-cloud-test-catalog.conf`) switching the brain to offline install against an inert catalog URL, so it trusts the docker-loaded image's catalog-promised digest instead of pulling. The assertions unit imports the new `malmo.sso_token` credential.

## How it maps to the specs

- `TESTING.md` # Hosted cloud variant now documents the `access` boot and the harness-held test portal that drives the positive session path. This is a deliberate, scoped extension of the box-only lane: it proves the **box's** per-app access mechanism (verify endpoint, `forward_auth` route, whole-header strip) with a simulated portal; verifying the **real** portal round-trip remains the cloud on-ramp acceptance's job (the cloud repo holds the production signing key). The two lanes test different things and both stand.

## Known gaps & deviations

- **Crosses the boundary `TESTING.md` previously drew.** The prior prose said the positive path "is the joint on-ramp acceptance, not this box-only lane." #308 is the driver to add a test portal here; the spec is updated to match. The trust model is unchanged (the box only ever sees a public key), so this is a test-harness capability, not a weakening of the box.
- **Runs in the `CI / Cloud image` boot gate, not just locally.** The `access` boot is in the workflow's `MALMO_CLOUD_BOOTS` (`unseeded seeded bios access`), so a box that leaks its forward-auth cookie to an app upstream fails the gate and is never published. It is *not* left to the local-only run the way `frozen` is: the per-app gate is on by default for every hosted app (#306 flipped `restricted` ahead of this proof, per `DECISIONS.md` 2026-07-08), and a security invariant whose only net runs when a developer remembers to run it by hand is not a net. The boot's host-side assertion mint needs `go` on the runner (the lane runs under `sudo -E`, whose `secure_path` resets `PATH`), so the workflow resolves it and passes `GO` explicitly. Cost: one more TCG-only QEMU boot in the gate — it does the most in-guest work of any boot (SSO + app install + exposure toggle), which is why it carries a wider `VERDICT_TIMEOUT` (720s vs the 480s default).
- **Timing under CI's TCG-only QEMU is proven by the gate itself, not by a local KVM run.** The lane was exercised locally with KVM; the CI runner may fall back to TCG (the workflow tolerates both). The 720s verdict ceiling was sized for that, but the first CI run is the real characterization — if the `access` boot proves slow or flaky under TCG, the tradeoff (widen the ceiling vs. drop it from the gate) is a maintainer call, and the failure mode is a red gate, not a silent pass.
- **One app, toggled — not two.** A single whoami is installed and flipped restricted→public, exercising both modes and the toggle endpoint. Cross-app replay isolation (a public app can't reach a restricted app's upstream) is proven indirectly by the whole-header strip on the public route, not by a second installed app.
- **Owner-only, single-user.** Inherited from #305/#306/#307: the session is the box owner's. Restricting to box users the owner creates is the epic's additive follow-up (`ENVIRONMENT.md` # Deferred).
- **No live `:443`/TLS probe of the app route.** The app assertions use `:80` (the lean image has no TLS client — the same constraint the existing boots work under); the seeded boot already proves `:443` binds. The forward-auth login redirect target is asserted as a `Location` string, not followed.

## What's next

- This closes the per-app access-restriction epic (#304): #305→#306→#307 shipped the mechanism + default flip + dashboard control, and #308 proves both modes end-to-end. The remaining epic follow-up is **multi-user** access (box users beyond the owner), tracked as additive in `ENVIRONMENT.md` # Deferred — not v1.
