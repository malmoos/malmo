# Cloud lane asserts the hosted wildcard-TLS pass (close the #278 CI gap)

- **Status:** done — air-gapped assertion of config *application*; real Let's Encrypt issuance stays the cloud on-ramp's job (cloud #6 / CL6)
- **Date:** 2026-06-29
- **Specs touched:** `TESTING.md` (# Hosted cloud variant — the seeded boot now also asserts the wildcard-TLS application + `:443` bind)

Closes the CI gap [#278](https://github.com/malmoos/malmo/issues/278) named: a booted hosted box that ingests its seed but never **applies** the wildcard-TLS pass (no acme-dns DNS-01 issuer, no `:443` bind, no `*.<box-id>` cert) would pass the cloud lane green. The lane ([cloud-e2e-test.md](cloud-e2e-test.md), seed delivery in [cloud-seed-delivery.md](cloud-seed-delivery.md), wildcard machinery in [hosted-wildcard-cert.md](hosted-wildcard-cert.md)) is air-gapped (`restrict=on`) and its seed carried **no `enrollment` block**, so `cmd/brain`'s `EnsureWildcardTLS` was gated off (`enrollment.Complete()` false) and never ran in any test. A regression in that path shipped green; the cloud live on-ramp (`malmoos/cloud`, `-tags e2e_live`) covers it but was last green on an older image.

This is a **test-lane coverage** change — no brain/host-agent/UI behavior changed. The original #278 symptoms (box-id site unrouted, `:443` refused, no cert) did not reproduce on a re-test (the box came up reachable with valid SSL), so the standing defect, if any, was transient; the durable fix is making this regression class impossible to ship silently.

## What was done

### The seeded boot now delivers a complete enrollment (`dev/cloud/run-cloud-tests.sh`)

- `seed_cred()` adds an `enrollment` object (`subdomain`/`username`/`password`) to the seed JSON alongside `box_id` + `assertion_verification_key`. The values are **inert** in this lane: air-gapped, the box never reaches acme-dns or Let's Encrypt, so no real cert issues. Their only job is to make `enrollment.Complete()` true so the brain runs `EnsureWildcardTLS`. The header scenario comment for boot 2 records the added wildcard-TLS application step.
- Verified the brain's own parser accepts the new wire shape: a throwaway `internal/profile` test confirmed `ReadSeed` parses it and `Enrollment.Complete()` returns true (the os↔cloud meeting point is the seed's JSON, not a shared Go type).

### Two new seeded-boot assertions (`dev/cloud/cloud-assertions.sh`)

- **(a)** Polls `docker logs malmo-brain` for `caddy: wildcard TLS configured` — the line `EnsureWildcardTLS` emits only after both the `tls`-app PUT and the `:443` listener PATCH succeed against Caddy's admin API. Its presence proves the brain reached and applied the config.
- **(b)** A plain TCP connect to `127.0.0.1:443` (Caddy publishes `443:443`) must succeed — the socket is listening even though the TLS handshake can't complete without a cert. This is the issue's "`:443` never binds" symptom, asserted positively.

Both are in the `seeded)` branch, which is in the cloud-image **publish gate** (`MALMO_CLOUD_BOOTS` defaults include `seeded`; the publish gate runs `unseeded seeded`), so the new checks gate publish.

## How it maps to the specs

- `TESTING.md` # Hosted cloud variant: the seeded-boot description gains the wildcard-TLS application + `:443`-bind assertion and the air-gapped caveat (config applied, not a real cert). Matches `ENVIRONMENT.md` # Networking & discovery (always-on wildcard HTTPS) and `hosted-wildcard-cert.md` (real issuance verified jointly in the cloud on-ramp, not the inner loop).

## Known gaps & deviations

- **No real cert / DNS-01 round-trip.** Air-gapped by design; the lane asserts the brain *applies* the issuer + binds `:443`, not that a cert is obtained. Real ACME DNS-01 against acme-dns + a live `*.<box-id>` cert remains the cloud live on-ramp's acceptance (cloud #6 / CL6).
- **Original #278 defect not root-caused.** The reported boot did not reproduce (box reachable, SSL fine on re-test), so no standing code defect was found to fix; both reported symptoms reduce to `boxID == ""` at runtime (seed not ingested on that boot), consistent with a transient first-boot condition rather than a logic bug. If it recurs, box-side brain logs (VNC console — the lean image has no sshd) are the next step: whether `provisioning seed ingested` and `caddy: wildcard TLS configured` are logged pins it to ingest vs. application.
- **`:443`-bind is a connect probe, not a config read.** Caddy's admin API (`:2019`) is not host-published (only `80`/`443` are), so the bind is asserted by TCP connect rather than by reading the listen array. The brain log line (a) covers the config-applied half; together they bracket "reached, applied, bound."
- **Pre-existing TESTING.md staleness left untouched.** The same paragraph still describes the superseded `/setup` secret 401/200 gate (replaced by portal-to-box SSO in [portal-box-sso.md](portal-box-sso.md)); fixing that is out of scope for this issue.

## What's next

- When the cloud live on-ramp (`malmoos/cloud`, `-tags e2e_live`) is re-run green on a current image, fold its real-cert acceptance status into the cloud-lane story so the air-gapped/live split is documented in one place.
