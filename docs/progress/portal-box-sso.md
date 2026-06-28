# Portal-to-box SSO — box side (C-series #275)

- **Status:** done — box-side verifier, owner auto-create, session exchange, and the hosted unauthenticated→portal bounce. Real end-to-end (portal mints → box lands) is verified jointly with the cloud half in the on-ramp (cloud `docs/ops/e2e-onramp.md`), not the inner loop.
- **Date:** 2026-06-28
- **Specs touched:** `ENVIRONMENT.md` (# Admin bootstrap — replaced the one-time bootstrap-secret `/setup` flow with the SSO handshake on hosted; the seed now carries `assertion_verification_key`, not `admin_bootstrap_secret`), `docs/architecture.md` (new `internal/assertion` package; the `/_malmo/sso` brain route).

Realizes the **box half of the portal-to-box SSO handshake** (issue #275), the lockstep counterpart to cloud #51 / PR #52. A hosted box's owner now reaches the box dashboard through their existing `malmo.network` login — no box password, no copied secret, no `/setup` page. This **replaces** the hosted admin-bootstrap-secret `/setup` flow shipped in C3a (#206, [hosted-setup-gate.md](hosted-setup-gate.md)); the **appliance profile is unchanged** — it has no portal, so it keeps `/setup` + open-on-empty-box. The two repos are merged together: a hosted box booting the new seed (which drops `admin_bootstrap_secret`, adds `assertion_verification_key`) has no working bootstrap until this lands.

## What was done

### The assertion verifier (`internal/assertion`)

- New leaf package mirroring the cloud reference (`cloud internal/assertion.Verify`) byte-for-byte: a token is `base64url(claims-json) "." base64url(ed25519-sig)`, the signature covers the exact transmitted first-segment bytes, and verification is a single `ed25519.Verify` plus an expiry check — **not a JWT**, so the `alg`-confusion / `alg:none` footgun class is structurally absent (no JWT library on the path). `Verify` checks signature + `exp` only; issuer / box-id / replay are the handler's policy (typed sentinels `ErrMalformed`/`ErrSignature`/`ErrExpired` so the handler can log the reason without leaking it).

### Seed + ingestion (`internal/profile`, `internal/store`, `cmd/brain`)

- `profile.Seed`: `AdminBootstrapSecret` → `AssertionVerificationKey` (the portal's Ed25519 public key, standard base64). `ReadSeed` now requires `box_id` + `assertion_verification_key`.
- `loadHostedEnvironment` persists the **base64 key** (not a hash — it's a public key) in `box_meta` under `BoxMetaAssertionKey`, in **key → enrollment → box-id** commit order (box-id stays the crash-safe marker; a key-persist failure aborts before the marker so the seed re-ingests next boot). Frozen-identity boots reload the stored key and ignore the seed, exactly as before.
- `cmd/brain` decodes the base64 key to an `ed25519.PublicKey` once at startup (`decodeAssertionKey`, length-validated); an invalid key logs and disables SSO (nil key) rather than crashing. The decoded key is handed to the API via `SetEnvironment(prof, boxID, assertionKey)` (the old `bootstrapSecretHash` param is gone).
- New `store` keys `BoxMetaOwnerSub` / `BoxMetaOwnerUserID` (the SSO owner identity) and a `used_assertions(jti, expires_at)` single-use ledger with `UseAssertionJTI(jti, exp, now)` — insert-or-`ErrConflict`, pruning past-expiry rows on each write so the table stays bounded to the in-flight token set.

### The SSO landing handler (`internal/api/sso.go`)

- `GET /_malmo/sso?token=...`, registered raw on the mux (a redirect+Set-Cookie endpoint, outside the OpenAPI surface) and public to the auth middleware (the assertion is the credential). Hosted-only — appliance returns 404; an un-provisioned hosted box (no key) returns 503.
- On a valid assertion it applies box-side policy (`iss == NetworkApex`, `box == boxID`, single-use `jti`), then resolves the owner: the **first** valid assertion auto-creates a passwordless PAM admin (username derived from the email local-part, random discarded password — login is only ever via this handshake) following the brain-commits-first ordering /setup uses (create row → host SetPassword/SetRole → record owner meta as the commit marker, with rollback on host failure and a deterministic-username adopt path for a partial prior create). Every **later** assertion enforces owner-only (`sub` must match the recorded owner; v1 grants no other accounts) and reuses the stored admin.
- It then mints the box's own **host-only** session (`auth.Manager.Cookie` carries no `Domain`, so the cookie is scoped to `<box-id>.<apex>` and never sent to `<slug>.<box-id>.<apex>` app subdomains) and 303s to `https://<box-id>.malmo.network/`. Every failure path returns one opaque status, audits `sso.failure` (new action, mirrors `login.failure`), and never logs the token; success audits `sso.success`.

### `/setup` profile-gate (`internal/api/auth.go`)

- On hosted, `POST /setup` is disabled (403, audited) — the owner bootstraps via SSO, and an open `/setup` would be a second unauthenticated path to the founding admin. `gateBootstrap` and the `bootstrap_secret` body field are removed. Appliance `/setup` is byte-for-byte unchanged.

### Routing + UI

- `caddy.EnsureDashboard`: the brain leg now matches `/api/*` **and** `/_malmo/*` (so the SSO landing reaches the brain; everything else is still the SPA).
- web-ui: on the hosted profile there is no login or setup page — an unauthenticated visitor is bounced to `https://malmo.network` (`auth.ts` `redirectToPortal`, driven by a `watchEffect` in `App.vue` that fires on first boot and on any later 401). `AdminStep.vue` loses the hosted bootstrap-secret field + link-prefill (dead on hosted; the step is appliance-only). The wizard's later steps (time zone / telemetry / done) still run on hosted after SSO, since the admin already exists (Setup.vue skips the admin step).

## Tests

- `internal/assertion`: round-trip, tamper, wrong-key, expiry, malformed.
- `internal/api/sso_test.go`: valid→owner-create+host-only session+303; second owner assertion reuses the admin; non-owner 403; tampered/wrong-key 401; expired 401; wrong-box/wrong-issuer 403; replay 401; un-provisioned 503; appliance 404.
- `internal/store`: `UseAssertionJTI` single-use + prune.
- `cmd/brain`: `loadHostedEnvironment` ingest/frozen/abort matrix retargeted to the assertion key; `decodeAssertionKey` valid/invalid/wrong-length.
- `auth_hosted_test.go`: hosted `/setup` 403+audited; appliance `/setup` open + omits box_id.
- `make test-nopam`, `gofmt`, `go vet`, `make openapi-check`, and `web-ui` `vue-tsc` + `vite build` all green.

## What's next

- **Joint live run** with the cloud half (cloud #52, `docs/ops/e2e-onramp.md`): owner logs in once at `malmo.network`, clicks into the box, lands on the box dashboard. Must merge in lockstep with the cloud deploy — a hosted box on the new seed has no bootstrap until both sides ship.
- Deferred (tracked in cloud `NEXT.md`): granting box access to **other** `malmo.network` accounts (v1 is owner-only), portal-outage **break-glass** offline admin, signing-key **rotation** (the `kid` claim reserves it; the box trusts the one seed-carried key).
