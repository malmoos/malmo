# The brain serves no CORS headers

- **Status:** done
- **Date:** 2026-09-11
- **Specs touched:** docs/specs/AUTH.md

Closes #475. Found by the Greptile review on the v0.12.0 release PR (#474), against code that was already on `dev`.

## What was done

`internal/api.withCORS` wrapped the whole handler chain, on both profiles. It reflected whatever `Origin` arrived, set `Access-Control-Allow-Credentials: true`, and answered every `OPTIONS` with 204. It is gone.

**Why that was a real hole and not a theoretical one.** On hosted, apps are served at `<slug>.<box-id>.malmo.network` and the dashboard at `<box-id>.malmo.network`. `malmo.network` is not on the Public Suffix List — the same fact `ENVIRONMENT.md` # Certs uses to explain why the whole fleet shares one Let's Encrypt rate-limit budget. So those hosts are **same-site**, and the owner's `malmo_session` cookie being `SameSite=Lax` does not stop a fetch from an app origin to the dashboard host. The cookie went; the reflected header let the app read the answer. Any app on the box could act as the owner against the dashboard API, including `POST /api/v1/auth/elevate/challenge`.

**Two places in the tree already claimed this was impossible**, which is what makes it a defect rather than a design tradeoff:

- `AUTH.md` # Re-authentication: "minting one needs an authenticated JSON POST to the box's own API, which a cross-origin page cannot make."
- `confirm.go`: "the JSON content type forces a preflight the brain does not answer."

The brain answered it. The hosted confirm step's whole cross-site argument rested on a preflight being refused by a layer that was in fact returning 204.

**Removed rather than narrowed to an allowlist.** Nothing calls this API cross-origin in any lane. `web-ui/src/api.ts` fetches relative paths (`/api/v1${path}`); in production Caddy serves the dashboard and the brain on one host; in dev `web-ui/vite.config.ts` proxies `/api` to the brain with `changeOrigin`, so the browser only ever sees `localhost:5173`. The middleware's own comment said it existed for the Vite dev server, and the proxy had already made that untrue. An allowlist would have kept a knob that is only ever set wrong, to serve a caller that does not exist.

The `Handler` comment now carries the reasoning, so the absence reads as a decision rather than an oversight, and `AUTH.md` states it as a guarantee the brain keeps with the reason named.

## What was tested

- `internal/api/cors_test.go`, two tests. A foreign `Origin` on a real 200 gets none of the four `Access-Control-*` headers back, and a preflight `OPTIONS` on the challenge route is not answered 204. Both were run against the old middleware first and fail there, reflecting the attacker origin verbatim — they are regression tests, not assertions that happen to hold.
- The 200 case deliberately uses the appliance login picker, which answers without a session. Asserting header absence on a 401 could pass while a header was still attached further down the chain.
- `make check` green.

## Known gaps & deviations

- **The blanket `OPTIONS` 204 is gone with it.** That is intended, and no malmo code sent one, but a developer calling the brain directly from a browser page rather than through the Vite proxy would now see the preflight fail. Same-origin requests never preflight, so `make dev` is unaffected.
- **Not exercised on a booted box.** This is an in-process API test. The attack it closes needs a hosted box with an installed app to demonstrate, and the cloud lane has no such assertion.
- **Only the brain is covered.** Caddy still sets whatever headers it sets on app routes; app-to-app CORS is the apps' own business and `SPEC.md` # Origins is unchanged.
- The fix does not narrow what a *same-origin* compromised page can do. An XSS in the dashboard itself is a different boundary and is untouched here.

## What's next

1. The other two findings from the same review, stacked on this branch: the appliance mandatory SSH factor, and the host-side drift when a daemon call fails.
2. A cloud-lane assertion that an app origin cannot read the dashboard API, which is the only place this can be proved end to end.
