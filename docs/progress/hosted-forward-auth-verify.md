# Hosted forward-auth verify endpoint + domain-wide forward-auth cookie

- **Status:** done
- **Date:** 2026-07-08
- **Specs touched:** `docs/specs/ENVIRONMENT.md` (# Public-by-default, auth-gated — added the per-app forward-auth mechanism subsection)

First slice of the hosted per-app access-restriction epic (#304), closing **#305**. In the hosted profile we want a per-app "Only me" mode where an app is gated by the box's own identity and clicking it from the dashboard opens it with no second login. The mechanism is `forward_auth` in the box's Caddy in front of each restricted app; this issue builds the **brain side** it calls — the verify endpoint and the second cookie that backs it. The Caddy route wiring + cookie-strip, the per-instance exposure state, the dashboard toggle, and the e2e lane are the later slices (#306/#307/#308); the hosted default is **not** flipped to owner-only here (that waits on #306 + the #308 proof, per the epic's sequencing).

## The cookie topology (the crux)

The dashboard session cookie (`malmo_session`) is deliberately **host-only** on the dashboard host, so a third-party app subdomain can never receive it and replay it as an admin session (`AUTH.md` # Sessions). A host-only cookie is therefore not sent to app subdomains and can't authorize a click-through. So a hosted box now mints a **second** cookie at session establishment:

- `malmo_session` — host-only, unchanged. Drives the dashboard / API.
- `malmo_forward_auth` — scoped `Domain=<box-id>.malmo.network`, so the browser sends it to app subdomains `<slug>.<box-id>.malmo.network` as well as the dashboard host. It is a **strictly lower-privilege** token: its value is a distinct random token stored in a distinct column, so replaying it as `malmo_session` never resolves to a dashboard session. Even if it leaked it grants app access, never dashboard control.

## What was done

### Store — the forward-auth token lives on the session row (`internal/store/store.go`)

- New `fa_token` column on `sessions` (added to `CREATE TABLE` and to the idempotent `ALTER TABLE` migration list, `DEFAULT ''`).
- `SetSessionForwardAuthToken(sessionToken, faToken)` stamps it; `GetSessionByForwardAuthToken(faToken)` is the reverse lookup the verify endpoint runs. The empty token is rejected up front so it can never match the `''` default every non-hosted (and pre-mint) row carries. Keeping the token on the session row ties its lifetime to the session — logout / expiry drop it for free (the `ON DELETE CASCADE` and the shared row), which is why a stateful column beat a stateless signed token.

### Auth — a second credential that reuses the session machinery (`internal/auth/auth.go`)

- `ForwardAuthCookieName = "malmo_forward_auth"`; `Manager.ForwardAuthDomain` holds the `Domain` attribute (the box apex; empty ⇒ minting disabled).
- `IssueForwardAuth(sessionToken)` generates a fresh 256-bit token, persists it on the session, and returns the `Domain`-scoped cookie. `ForwardAuthCookie` / `ClearForwardAuthCookie` carry the same hardening as the session cookie (`HttpOnly`, `SameSite=Lax`, `Secure` per `SecureCookies`) plus the `Domain`.
- `ValidateForwardAuth(faToken)` resolves the token to an `Identity` through the **same** lifetime + user checks as `Validate`, factored into a shared `resolveSession(sess, touch)`. The one deliberate divergence: the forward-auth path passes `touch=false`, so it does **not** bump `last_seen_at`. The box Caddy calls verify on a per-request `forward_auth` subrequest (potentially once per asset); touching the row there would be write amplification for no benefit. The dashboard session stays the liveness authority; app traffic rides its window rather than extending it.

### API — the verify endpoint + minting on every session-establishment path

- **`internal/api/forwardauth.go`** — `GET /_malmo/forward-auth/verify`, a raw handler outside the OpenAPI surface (a proxy-internal probe, not a client API). Reads the forward-auth cookie, `ValidateForwardAuth`, then enforces **owner-only** (v1): the identity must equal `BoxMetaOwnerUserID`, failing **closed** on any owner-lookup trouble. 200 + `X-Malmo-User` / `X-Malmo-User-Id` identity headers on allow (#306 chooses what to forward and strips the cookie), 401 otherwise → Caddy redirects to the box login. Hosted-only: on appliance it 404s exactly like `/_malmo/sso`. Public to the auth middleware (the cookie is the credential, and the middleware validates `malmo_session`, which app subdomains never send). Pure read, so it never audits. **Exempt from the per-IP request-rate bucket on hosted** (`ratelimit.go`): Caddy calls it at per-asset frequency, so the 30/min/IP allowlist plane would throttle legitimate app traffic; the cookie check is the abuse control (a 256-bit token isn't guessable), so there is nothing for a rate limit to defend. The exemption is scoped to hosted — on the appliance the endpoint always 404s, so it keeps the normal throttle. `GetSessionByForwardAuthToken`'s reverse lookup is backed by a partial index (`idx_sessions_fa_token ... WHERE fa_token != ''`) so the per-request path stays an indexed lookup, not a table scan.
- **`login`** (`internal/api/auth.go`) — on hosted, mints the forward-auth cookie alongside the session cookie (the `Set-Cookie` response header became `[]string` so huma emits both). **`logout`** clears both on hosted. Appliance issues/clears only the host-only session cookie, so its login/logout responses are byte-for-byte unchanged.
- **`ssoLanding`** (`internal/api/sso.go`) — the hosted owner's bootstrap path also mints the forward-auth cookie; computed before either cookie is set so a store failure 500s cleanly (no session cookie on a 500) and a fresh portal round-trip retries the whole exchange.
- **`Server.SetEnvironment`** now also sets `authMgr.ForwardAuthDomain` on hosted, so the cookie's `Domain` is wired from the resolved box-id in one place (and every hosted test harness gets it for free).

Owner-only policy is enforced in the API (`forwardauth.go`), not in `auth` — the auth layer stays free of box-identity concepts and only proves the session behind the token is live.

## How it maps to the specs

- `ENVIRONMENT.md` # Public-by-default, auth-gated gained a subsection describing the two-cookie topology and the verify endpoint as the realized mechanism, explicitly noting the default flip to owner-only is deferred to #306 + the #308 proof.
- Reinforces `AUTH.md` # Sessions (the host-only session cookie is unchanged; the forward-auth cookie is the additive, lower-privilege second credential) and the hosted profile's "no appliance impact" rule (`ENVIRONMENT.md` # Two layers) — every seam is behind `profile == Hosted`.

## Known gaps & deviations

- **Owner-only, single-user.** Only the box owner's session validates; box users the owner may later create are a follow-up (v1, per the issue). A non-owner session gets a forward-auth cookie but it fails verify — harmless, and simpler than gating the mint on ownership.
- **`Secure` is not forced on the forward-auth cookie.** It mirrors `SecureCookies`, which the brain does not yet wire on hosted (a pre-existing gap for `malmo_session` too, out of scope here). On hosted every origin is HTTPS via Caddy, so this is a hardening follow-up, not a live hole — noted so it isn't lost.
- **Verify reads owner box-meta per request.** A single indexed lookup on the hot path; not cached because the owner is written at SSO (after startup) and never changes in v1. Fine at hosted single-tenant scale; a cache-with-invalidation is a later optimization if it ever matters.
- **Not yet exercised end-to-end through Caddy.** #305 is the brain side only; the `forward_auth` directive, cookie strip, and the cookie-leak probe land in #306/#308.

## What's next

- **#306** — per-app `restricted`/`public` exposure state, the Caddy `forward_auth` + `Cookie`-strip route builder pointing at this endpoint, and the hosted default flip to owner-only (with the `ENVIRONMENT.md` # Deferred update).
- **#307** — the dashboard Only-me / Public toggle.
- **#308** — the hosted e2e lane proving both modes + the cookie-leak probe (the strip invariant), which gates the default flip.
