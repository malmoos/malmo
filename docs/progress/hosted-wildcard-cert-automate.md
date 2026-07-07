# Name the wildcard in Caddy's `certificates.automate` so the box actually obtains it

- **Status:** done (box-side logic + hermetic tests); confirmed against the failing real box's on-disk Caddy logs, fix verified on a real box is the follow-up
- **Date:** 2026-07-04
- **Specs touched:** `DECISIONS.md` (2026-07-04 entry — supersedes the 2026-07-03 serialize-the-orders call)

Fixes the same user-visible symptom [wildcard-cert-serialize-acme-orders.md](wildcard-cert-serialize-acme-orders.md) (#300) tried to fix — a hosted box gets its base cert but every app fails TLS with `ERR_SSL_PROTOCOL_ERROR` — but corrects that entry's **root-cause misdiagnosis**. The serialize-the-two-orders fix addressed a race that does not occur; the wildcard order was never being placed at all.

## What was actually wrong (ground truth from a real box)

A real box (`asd-elm`) reproduced the failure. External probing showed base `asd-elm.malmo.network` had a valid cert but `actual.asd-elm.malmo.network` failed the TLS handshake, DNS delegation was healthy (`_acme-challenge.asd-elm` CNAME + live acme-dns TXT), and Certificate Transparency showed **only the base name was ever issued**. The box has no SSH (hosted ships none) and no console login (appliance), so its Caddy container log was read off the disk via **Hetzner rescue mode**. It was unambiguous:

- Caddy obtained `asd-elm.malmo.network` via **tls-alpn-01** (2 attempts, success).
- Caddy tried to obtain the **exact app name** `actual.asd-elm.malmo.network` via **dns-01** (6 attempts, all failed): `could not determine zone for domain "_acme-challenge.actual.asd-elm.malmo.network"` then `timed out waiting for record to fully propagate`.
- **Zero** obtain attempts for the wildcard `*.asd-elm.malmo.network`. On-disk cert store held only `asd-elm.malmo.network.crt`. Brain `RestartCount` was 0. The brain logged `caddy: wildcard TLS configured` and, 3 minutes later, `wildcard cert not obtained … context deadline exceeded`.

### Root cause

`EnsureWildcardTLS` configured a Caddy `automation.policies[]` entry for `*.<box-id>.malmo.network` but never named it in `apps/tls/certificates/automate`. In Caddy an automation policy only defines *how* to manage a matching name (the issuer) — it does **not** schedule issuance. Caddy obtains a cert only for a name in `certificates.automate` or named by an HTTP route's Host matcher. So:

- The wildcard was in neither → **Caddy never placed the wildcard order.** The `#300` `waitForCert` then polled `:443` for a wildcard cert that was never being obtained, so it timed out every boot by construction.
- Each installed app adds a route whose Host is the **exact** `<slug>.<box-id>.malmo.network`. Caddy manages that exact name, matches it to the wildcard policy's acme-dns issuer, and tries dns-01 for it — placing the challenge at `_acme-challenge.<slug>.<box-id>.malmo.network`, which is **not delegated** to acme-dns (only the apex `_acme-challenge.<box-id>` is). Unanswerable → every app fails.
- The apex succeeded only because it is a real reachable host and Caddy's **default** issuer got it over tls-alpn-01, with no acme-dns involvement.

So there was only ever one (doomed, per-app) acme-dns order — never the two concurrent orders `#300` serialized.

## What was done

### Fix (`internal/caddy/caddy.go`)
- `EnsureWildcardTLS`'s phase-1 PUT now sets `certificates.automate: ["*.<box-id>.malmo.network"]` alongside the wildcard automation policy. That is the missing "WHAT to obtain"; the policy is the "HOW". Caddy now places the wildcard order at the delegated apex `_acme-challenge.<box-id>`, obtains `*.<box-id>`, and serves every app subdomain from it — and no longer attempts per-app issuance.
- The **apex is deliberately not routed through acme-dns**: it is served by Caddy's default issuer (tls-alpn-01/http-01) as soon as the dashboard route names it. So exactly one name (the wildcard) touches acme-dns — no order-vs-order contention, nothing to serialize.
- **Removed** the now-purposeless `#300` machinery: `addBaseSubjectAfterWildcard`, `waitForCert`, `dialCertReady`, `tlsAddr`, the `certReady`/`certPollInterval`/`certWaitTimeout` `Client` fields and their consts. `EnsureWildcardTLS` is now purely synchronous config (no background goroutine, no cert wait). The `caddy: wildcard TLS configured` milestone log and the `:443` bind are unchanged, so the seeded-boot CI assertions still hold.

### Docs
- `internal/profile/appurl.go` `CertSubjects` and `cmd/brain/main.go`'s call-site comment updated to describe the two-path model (wildcard via acme-dns, apex via the default issuer).
- `DECISIONS.md` 2026-07-04 entry records the misdiagnosis and the corrected model, superseding the 2026-07-03 call.

### Tests (`internal/caddy/caddy_test.go`)
- `TestEnsureWildcardTLS` now asserts the PUT includes `certificates.automate` containing the wildcard, exactly one wildcard policy with the acme-dns issuer, the `:443` patch, and **no** base-subject POST. This unit test is the regression guard for the exact bug (a policy without an automate entry). Removed the two phase-2 sequencing/timeout tests and `TestTLSAddr`.

## Known gaps & deviations

- **Not yet verified against real ACME on the fixed image.** The diagnosis is confirmed on the failing box; the fix is proven only by hermetic tests. The acceptance is: build an image from this branch, provision a real box, and confirm `*.<box-id>` issues and an app serves over HTTPS. Air-gapped CI cannot reach ACME, so it can only assert the config shape (which the unit test now does).
- **Air-gapped CI can't catch a real-issuance regression** — this whole class of bug (config that looks right but never issues) is invisible to the seeded-boot lane. The unit test asserting `certificates.automate` is the closest guard; a real-box smoke on deploy remains the only end-to-end proof.

## What's next

- Real-box acceptance on an image built from this branch (provision → install an app → app serves HTTPS from the wildcard).
- Revisit renewal (~60 days out) only if a renewed box ever loses the wildcard — now a single-order path, so the prior renewal-race question is moot.
