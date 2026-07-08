# Hosted per-app forward-auth route + Cookie-strip + owner-only default

- **Status:** done
- **Date:** 2026-07-08
- **Specs touched:** `docs/specs/ENVIRONMENT.md` (# Public-by-default, auth-gated — Per-app owner-only access rewritten to "as built"; # Deferred bullet narrowed to multi-user), `docs/specs/DECISIONS.md` (2026-07-08 entry)

Second box-side slice of the hosted per-app access-restriction epic (#304), closing **#306** and building directly on the brain forward-auth verify endpoint + domain-wide cookie of **#305** (`hosted-forward-auth-verify.md`). #305 gave the brain a `GET /_malmo/forward-auth/verify` endpoint and a lower-privilege `malmo_forward_auth` cookie; this slice wires the box Caddy to *use* them: a per-app exposure state, the central route builder that gates a restricted app and strips the cookie, the toggle endpoint, and the flip of the hosted default to owner-only.

## Two product calls (both diverge from the issue's literal text — see `DECISIONS.md`)

1. **The `Cookie` strip is the whole header, on every hosted app route** (restricted *and* public), not just restricted. The forward-auth cookie is `Domain=<box-id>.malmo.network`, so the browser sends it to every `<slug>.<box-id>` subdomain; stripping only restricted routes would leak it to a public app that could replay it against the owner's restricted apps. **Tradeoff:** a hosted app never receives *any* browser cookie, so an app relying on its own cookie session/CSRF won't see it — the box identity (`X-Malmo-User` headers) is the session for owner-only apps. Per-cookie surgical stripping is the follow-up if a real app needs its own cookies.
2. **The hosted default is flipped to `restricted` now**, not gated on the #308 e2e. The mechanism (#305 + #306) is complete and unit-tested; #308 becomes the end-to-end *proof* of an already-shipped default. Keeps "secure by default": a new hosted app is owner-only until the owner opts it public.

## What was done

### Store — a per-instance `exposure` column (`internal/store/store.go`)

- `exposure TEXT NOT NULL DEFAULT 'public' CHECK (exposure IN ('public','restricted'))` on `instances`, added to the `CREATE TABLE` and the idempotent `ALTER` migration list. Existing rows (appliance + any pre-#306 hosted) migrate to `public`, so an upgrade never surprises a live box by gating an app — only *new* hosted installs default `restricted`.
- `ExposurePublic` / `ExposureRestricted` consts; `Instance.Exposure` field (scanned + in `instanceColumns`). `Create` normalizes an empty exposure to `public` and rejects an out-of-range value (defense-in-depth, since the ALTER path can't carry the CHECK — the same pattern as `scope`). `SetInstanceExposure(id, exposure)` is the desired-state setter.

### Caddy — the route builder renders the policy (`internal/caddy/caddy.go`)

- `AddRoute` now takes a `RouteConfig{InstanceID, Host, Upstream, StripCookie, ForwardAuth}`. `StripCookie` adds `headers.request.delete: [Cookie]` to the app reverse_proxy; a non-nil `ForwardAuth` prepends the `forward_auth` handler in front of it.
- `forwardAuthHandler` is the native-JSON form of the Caddyfile `forward_auth` directive (malmo drives Caddy over its admin API — no Caddyfile): a reverse_proxy to the brain verify endpoint, rewritten to `GET <VerifyPath>`, with a `handle_response` policy — a 2xx copies the identity headers onto the request and falls through to the app; a catch-all block turns any other status (the brain's 401) into a 302 to the box login. The strip lives on the *app* proxy, not this subrequest, which must forward the cookie to the brain to be verified. `caddy` stays profile-agnostic (concrete value types only; the policy is decided by the caller).

### Lifecycle — one central route builder, reused everywhere (`internal/lifecycle/lifecycle.go`)

- `buildRouteConfig(inst, host, upstream)` is the single place the strip + gate are decided — "one central route builder is the safety boundary" (the spec's phrase). Appliance returns a bare reverse_proxy; hosted always sets `StripCookie` and, for a `restricted` instance, the `ForwardAuth` (verify upstream, `profile.ForwardAuthVerifyPath`, `X-Malmo-User`/`-Id` copy-headers, `https://<box-id>.malmo.network/` login). All three upstream-flip sites (install, `Start`, reconcile's `reassertRouting`) call it, so a restart never drops the gate.
- `defaultExposure()` = `restricted` on hosted, `public` elsewhere, set on the install row. `SetExposure(ctx, id, exposure)` persists the column and re-applies the route for a running app (a stopped app has only a splash and picks up the change on next start). `SetBrainUpstream` wires the verify dial from `cmd/brain` (the same `MALMO_DASHBOARD_BRAIN_UPSTREAM` the dashboard route uses); default `malmo-brain:8080`.
- `profile.ForwardAuthVerifyPath` is the one canonical verify-path string, shared by `internal/api` (which serves it) and `internal/lifecycle` (which points Caddy at it) since the two packages can't import each other.

### API — the toggle endpoint (`internal/api/appexposure.go`)

- `PUT /api/v1/apps/{id}/exposure` `{exposure: "restricted"|"public"}`, owner-or-admin (`authorizeAppMutation`), **hosted-only** (404 on appliance — exposure is a public-subdomain concept the appliance lacks), elevation-class so it audits success *and* failure (`app.exposure.set`). Echoes the updated app DTO. `InstanceDTO.exposure` is surfaced on every app response so #307's toggle can render the current mode.

## Known gaps & deviations

- **Whole-header strip, not per-cookie.** As above — deliberate for v1 (chosen this session). An app that needs its own cookies won't get them in either mode; surgical stripping of just `malmo_forward_auth` is the follow-up.
- **Login redirect drops the return URL.** A 401 redirects to the box dashboard root (`https://<box-id>.malmo.network/`), not back to the app after login. A `?next=` round-trip needs the SPA to honor it (candidate for #307 or later).
- **Runtime shape unverified in this slice.** The `forward_auth` JSON is asserted structurally against the recording admin fake (matching the known-good Caddyfile expansion), but real Caddy behavior — the subrequest, the 401→login redirect, the strip actually keeping the cookie off the app — is #308's e2e + cookie-leak probe.
- **Owner-only, single-user.** Inherited from #305: only the box owner's session validates. Multi-user is the additive step (# Deferred).
- **Default flip precedes its e2e proof.** By choice (`DECISIONS.md`): #308 proves an already-shipped default.
- **The authz-guard 403 isn't audited.** `setAppExposure` audits the `SetExposure` success/failure paths (elevation-class), but the owner-or-admin 403 from the shared `authorizeAppMutation` is not audited — a **pre-existing** gap shared with `stopApp`/`startApp` (only `uninstallApp` audits its inline household guard). Left as-is here to stay surgical; centralizing the audit inside `authorizeAppMutation` (benefiting all three callers) is the right follow-up rather than a local fix.

## What's next

- **#307** — the dashboard Only-me / Public toggle, calling `PUT /apps/{id}/exposure` and rendering `InstanceDTO.exposure` (the backend is in place here).
- **#308** — the hosted e2e lane proving both modes end-to-end through real Caddy + the cookie-leak probe of the strip invariant.
