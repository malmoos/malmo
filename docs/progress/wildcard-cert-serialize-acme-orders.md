# Serialize the box's two ACME orders so the wildcard cert stops racing itself

- **Status:** done (box-side logic + hermetic tests); real Let's Encrypt issuance is proven only on a real hosted box, not the inner loop (no real ACME/DNS locally)
- **Date:** 2026-07-03
- **Specs touched:** `DECISIONS.md` (wildcard + apex certs are issued one at a time)

Corrects a concurrency bug in the hosted box's cert acquisition surfaced by a real box that obtained its base cert but never its `*.<box-id>` wildcard, so every app served over the box failed TLS while the dashboard worked. Follow-up to [hosted-wildcard-cert.md](hosted-wildcard-cert.md), whose "a single challenge issues the combined cert" framing was wrong.

## What was done

### Root cause
`caddy.EnsureWildcardTLS` put both cert subjects (`<box-id>.malmo.network` and `*.<box-id>.malmo.network`) in one Caddy TLS automation policy. Caddy/certmagic issues one certificate per SAN, so this spawned two independent ACME orders that ran concurrently at box startup. Both solve DNS-01 at the same name (RFC 8555 puts the wildcard's challenge at the base label, where the base name's challenge also lands), against the same acme-dns account, whose TXT store keeps only a small fixed number of recent values. A third write from either order (a propagation recheck, an authorization retry) can evict the sibling order's still-unvalidated value and fail its validation with no visible error. The simpler base order tended to win; the wildcard lost.

### Fix: serialize the two orders (`internal/caddy/caddy.go`)
- `EnsureWildcardTLS` now issues the subjects one at a time. Phase 1 configures the **wildcard** as the only automation policy and adds the `:443` listener, then blocks on `waitForCert` until Caddy has actually obtained a certificate covering the wildcard. Phase 2 appends the **base** subject as its own policy (`POST` to `/config/apps/tls/automation/policies`), so its order never runs while the wildcard's is presenting or cleaning up a DNS-01 record. Only one order ever writes the acme-dns account at a time, so there is no third-write eviction.
- `waitForCert` polls a `certReady` seam (a `Client` field, stubbed in tests) and is bounded by the passed context, so a box whose wildcard never issues fails the call instead of hanging.
- `dialCertReady` is the real `certReady`: it dials the box's own already-listening `:443` (not a new exposed endpoint) with SNI set to a concrete name the wildcard covers, and reports whether the presented certificate lists the wildcard among its SANs. `InsecureSkipVerify` is deliberate and safe here, it inspects the loaded cert's SANs and never trusts or sends data; a dial error (no cert loaded yet) is "not ready," not a failure. `tlsAddr` derives the `:443` address from the admin address host.
- Small helpers factor the shared pieces: `splitCertSubjects` (order-independent wildcard/base split), `acmeIssuer` (the shared acme-dns issuer config), `certPolicy` (a single-subject policy).
- Self-review follow-up: `waitForCert` was retrying on a `certReady` error without ever surfacing it, so a persistent probe error (e.g. a malformed admin address) would burn the whole `wildcardCertTimeout` budget and come out the other end as an opaque "context deadline exceeded" with no clue why. It still retries on error (a probe can fail transiently, same as a plain "no cert yet" dial failure), but the last error is now folded into the timeout error. Added direct unit tests for the two small previously-untested pure functions: `splitCertSubjects`'s reject path (no wildcard / no base / empty) and `tlsAddr`'s host-derivation and no-host error path.

### Caller budget (`cmd/brain/main.go`)
`EnsureWildcardTLS` now runs two sequential ACME round-trips instead of one overlapping pair, so it gets its own `wildcardCertTimeout` (3 minutes) on an independent `context.Background()` context rather than sharing what remains of the reconcile-and-routes budget. Still best-effort: a box that gets no cert in the window logs a warning and keeps serving on `:80`, unchanged.

### Doc correction (`internal/profile/appurl.go`, `docs/progress/hosted-wildcard-cert.md`)
`CertSubjects`' doc comment and the prior progress entry both said the two names produce "one combined cert." Corrected to "two certs, issued one at a time."

## How it maps to the specs

Realizes the `DECISIONS.md` 2026-07-03 call (wildcard + apex certs are issued one at a time, not from one automation policy). No `ENVIRONMENT.md` change: the always-on `<slug>.<box-id>.malmo.network` HTTPS scheme and DNS-01 mechanism are unchanged; only the ordering of the two orders is.

## Known gaps & deviations

- **Not verified against real ACME here.** Tests are hermetic: they stub `certReady` and assert the two-phase sequencing (wildcard policy first, base appended only after the wildcard reports ready). Real issuance, and that the race is actually gone, is proven only on a real hosted box against real Let's Encrypt + acme-dns.
- **Renewal is out of scope.** This serializes first-boot acquisition. Whether certmagic's background renewal (~60 days out) can re-introduce a two-order collision is internal to certmagic once both certs are configured and is left as an open question, not assumed fixed.
- **`dialCertReady` assumes the admin host also serves `:443`.** True for the box (Caddy's admin API and public listener are one process), but it is an assumption baked into `tlsAddr`.

## What's next

- **Real-infrastructure re-run** on an image built from this branch: provision a real hosted box and confirm it obtains both certs (base and wildcard) and serves an app over HTTPS.
- **Revisit the renewal-race question** if a renewed box is ever seen to lose a cert.
