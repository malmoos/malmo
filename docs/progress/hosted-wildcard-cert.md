# Hosted always-on `.malmo.network` wildcard cert + sole URL scheme (C3b)

- **Status:** done — box-side machinery; real Let's Encrypt issuance is verified jointly in the cloud on-ramp (cloud #6 / CL6), not the inner loop (no real ACME/DNS locally)
- **Date:** 2026-06-21
- **Specs touched:** `ENVIRONMENT.md` (# Networking & discovery — added an "as built" subsection; # Admin bootstrap — as built: `enrollment` flipped from reserved to consumed), `DECISIONS.md` (hosted custom Caddy image; acme-dns endpoint a box-side constant)

Realizes **C3b (#207)** — the hosted box's always-on `<slug>.<box-id>.malmo.network` HTTPS scheme and its `*.<box-id>` wildcard cert. Closes the seam C3a (#206, [hosted-setup-gate.md](hosted-setup-gate.md)) left open: C3a ingested the seed but carried `enrollment` as opaque, unconsumed JSON. The blocker was the cloud-side DNS/ACME service, now shipped (cloud CL4 Route53 + acme-dns substrate "unblocks os#207", CL5 seed assembly) — the cloud registers a per-box acme-dns account and ships `{subdomain, username, password}` in the seed; this is what C3b consumes. The two repos meet at the seed's **JSON wire format**, not a shared Go type.

## What was done

### Typed enrollment, persisted and reloaded (`internal/profile`, `internal/store`, `cmd/brain`)

- `internal/profile/seed.go`: `Seed.Enrollment` changed from `json.RawMessage` to a typed `EnrollmentCredentials{Subdomain, Username, Password}` whose JSON tags mirror the cloud producer's `internal/seed.EnrollmentCredentials` byte-for-byte. Optional at parse (`Complete()` reports all three present); a hosted box seeded without it still gates `/setup`, it just gets no cert.
- `cmd/brain/main.go` `loadHostedEnvironment` now also returns the enrollment and persists it (`store.BoxMetaEnrollment`, JSON) in **hash → enrollment → box-id** order — box-id stays the crash-safe commit marker. Frozen-identity boots reload the persisted enrollment (the seed is ignored), so Caddy can be reconfigured without it. Persisting/reloading is best-effort and non-fatal: the `/setup` gate never depends on it.

### Caddy DNS-01 wildcard (`internal/caddy`, `cmd/brain`)

- `caddy.EnsureWildcardTLS` configures the `tls` app: one automation policy over the cert subjects, an ACME issuer whose DNS-01 challenge uses the `acmedns` provider with the seeded creds + a box-side endpoint constant, then PATCHes the malmo server's listen to add `:443`. Remove-then-put for idempotency (the file's `upsertRoute` idiom). `cmd/brain` calls it once at startup, gated on `profile == hosted && enrollment.Complete()` — appliance and un-enrolled boxes skip it and stay on `:80`.
- Cert subjects are **`<box-id>.malmo.network` + `*.<box-id>.malmo.network`** (`profile.CertSubjects`): the apex (dashboard host) is listed separately because a `*.<box-id>` wildcard does not cover the bare parent. Both share the one `_acme-challenge.<box-id>` record the cloud CNAMEs to acme-dns, so a single challenge issues the combined cert.
- The acme-dns API endpoint is a box-side constant — `MALMO_ACMEDNS_ENDPOINT`, default `https://auth.malmo.network` — not seeded (the same for every box; cloud `specs/ARCHITECTURE.md` Contract 2).

### Sole URL scheme via a profile-aware seam (`internal/profile`, `internal/lifecycle`, `internal/api`)

- New `internal/profile/appurl.go`: `HostedAppHost`/`HostedAppURL`/`HostedDashboardHost`/`CertSubjects` + `NetworkApex` — the single place the `<slug>.<box-id>.malmo.network` shape is named, importable by both `lifecycle` and `api` (leaf package).
- `internal/lifecycle`: `Manager` gains `profile`+`boxID` (set via `SetEnvironment`) and an `m.hosted()` guard. On hosted, install, `routeHost`, `publishHost`, and `MALMO_APP_URL` all use the public host and **skip the mDNS publish** (no LAN, slim host-agent has no Avahi). Appliance's `.local`/Avahi path is byte-for-byte unchanged.
- `internal/api`: `toDTO` is now a `*Server` method; on hosted it surfaces `https://<slug>.<box-id>.malmo.network` as the app's sole URL.
- `cmd/brain`: the dashboard host is `<box-id>.malmo.network` on hosted (the wildcard apex) instead of `malmo.local`.

### Custom Caddy build (`dev/control-plane`)

- Stock `caddy:2-alpine` ships no DNS-provider module, so DNS-01 needs a build with `caddy-dns/acmedns` compiled in. `dev/control-plane/caddy-acmedns/Dockerfile` is the xcaddy recipe; `compose.yml`'s Caddy image is now `${MALMO_CADDY_IMAGE:-caddy:2-alpine}`. The hosted profile sets `MALMO_CADDY_IMAGE` (the brain's env, which `docker compose` inherits) to the custom build; **appliance keeps stock Caddy** (it does no ACME yet). One env var selects the image so the future appliance toggle reuses the same build.

## How it maps to the specs

- Realizes `ENVIRONMENT.md` # Networking & discovery: `<slug>.<box-id>.malmo.network` is the sole, always-on scheme; the wildcard cert is obtained via ACME DNS-01 with the seeded acme-dns credential; no toggle, no `.local`.
- Consumes the seed's `enrollment` field (`ENVIRONMENT.md` # Admin bootstrap — as built; the field C3a reserved).
- Honors the cloud contract: renewal is box→acme-dns directly (cloud `specs/ARCHITECTURE.md` Contract 2); the brain never calls the control plane on the cert path.

## Known gaps & deviations

- **Not verified against real ACME.** The DNS-provider JSON shape, the acme-dns endpoint, and actual issuance are pinned but exercised only in the cloud on-ramp (cloud #6 / CL6) — there is no real Let's Encrypt/DNS in the inner loop or the QEMU cloud lane. Box-side tests assert config generation + URL surfacing, not a real cert.
- **Public acme-dns API face not deployed cloud-side.** The cloud deploy binds acme-dns's HTTP API to `127.0.0.1:4443` (internal `/register` only); no public face exists yet for boxes to push TXT updates to, and `https://auth.malmo.network` is a chosen default, not a confirmed deployed endpoint. Filed as `malmoos/cloud` #14; the box value is overridable via `MALMO_ACMEDNS_ENDPOINT`.
- **Enrollment creds stored plaintext** in `box_meta`, matching the cloud producer's MVP posture — a leaked pair only lets an attacker renew certs for that one box. At-rest encryption is a deferred hardening item (`NEXT.md`).
- **Appliance secure-URLs toggle not built.** This is hosted-only and always-on. The profile-aware seam is structured so the appliance toggle (`MALMO_NETWORK.md` # The toggle) can reuse it, but interactive enrollment + the Settings toggle are a separate later issue.
- **Custom Caddy image wiring into the hosted image / offline bundle** is not landed here — the compose is parameterized and the recipe exists, but baking the built image into the hosted cloud image and setting `MALMO_CADDY_IMAGE` there is part of the hosted-image / CL6 work.

## What's next

- **CL6 (cloud #6) joint verification** — boot a real seeded Hetzner VM, confirm Caddy obtains the wildcard cert and serves the dashboard + an app over real HTTPS at `<box-id>.malmo.network`.
- **Bake the custom Caddy image** into the hosted cloud image build + offline bundle and set `MALMO_CADDY_IMAGE` in the hosted run-spec.
- **Pin the public acme-dns endpoint** once the cloud deploys its public acme-dns API face (cross-repo).
- **Appliance secure-URLs toggle** — interactive enrollment + Settings → Network toggle, reusing this PR's profile-aware URL seam.
