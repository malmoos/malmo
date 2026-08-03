# Trusted-proxy client IP — the per-IP rate-limit key stops being caller-controlled

- **Status:** done
- **Date:** 2026-08-03
- **Specs touched:** `docs/specs/AUTH.md` (# Rate limiting — added "Which IP"), `docs/specs/BRAIN_UI_PROTOCOL.md` (# Rate limiting & abuse — added "What 'client IP' means")

Closes **#329**, raised while reviewing #311 and named in [hosted-forward-auth-verify.md](hosted-forward-auth-verify.md) # Known gaps as the reason a per-IP bucket could not be used to bound the forward-auth verify path.

Every per-IP throttle in the brain — `POST /login`'s per-IP token bucket (`AUTH.md` # Rate limiting) and plane 2 of the request limiter (`BRAIN_UI_PROTOCOL.md` # Rate limiting & abuse) — keyed on `clientIP()`, which returned the **first** `X-Forwarded-For` hop. The first hop is the one the original client writes. A caller who could get that header through to the brain therefore got a fresh bucket per request, and the throttle stopped being a throttle for exactly the adversary it exists for.

## What was done

### Brain — `clientIP` reads only what a trusted proxy vouched for (`internal/api/clientip.go`, new)

`clientIP` moved out of `auth.go` into its own file and became a method on `Server`, because it now needs configured state. The rule:

- If the immediate peer (`RemoteAddr` — the one address on a request nobody can forge) is **not** in the trusted-proxy set, `X-Forwarded-For` is ignored entirely and the peer address is the key.
- If the peer **is** trusted, the chain is walked **right to left**, skipping hops that are themselves trusted proxies, and the first untrusted hop is returned — the address the last trusted proxy actually observed.
- A hop that won't parse **stops the walk** and the peer address is used: nothing to the left of an unparseable token is knowable, so failing closed is the only safe read.
- Hops are canonicalised (`Unmap`, zone stripped) so `::ffff:203.0.113.9`, `203.0.113.9`, and `2001:DB8::1` vs `2001:db8::1` can't be spelled three ways to hold three buckets.
- Repeated `X-Forwarded-For` header **lines** are flattened into one chain — Go keeps them separate and a chain split across lines would otherwise hide hops.

The trusted set is `MALMO_TRUSTED_PROXIES` (comma-separated IPs/CIDRs). Unset ⇒ loopback + the private ranges, mirroring Caddy's own `private_ranges` shortcut: the brain container publishes no port, so every peer it can have is on the box's own `malmo-ingress` network. Set to the empty string ⇒ trust nothing, key on the peer alone. An unparseable value is **fatal at startup**, not warned-and-defaulted — silently widening the set is a security downgrade the operator never asked for, and silently narrowing it to none pools every LAN client into one shared login bucket. `cmd/brain` reads it with `os.LookupEnv` (new `envRaw`) so "set to empty" stays distinguishable from "unset", which `env()` collapses.

The `Server` zero value trusts nothing, so any handler chain assembled without the startup call fails closed.

### Brain — the limiter and the audit log now agree on one derived address (`internal/api/ratelimit.go`)

Plane 2 called `clientIP(r)` a second time. It now reads back the value `authMiddleware` already put on the context (`audit.ClientIPFromContext`), falling back to `s.clientIP(r)` for a chain built without that middleware. The throttle and the audit log can no longer disagree about where a request came from.

`forwardAuthExempt`'s doc comment cited the spoofable key as a reason not to use a per-IP bucket there; that reason is gone, so the comment says so and leaves the choice as a separate call rather than quietly implying the old one still holds.

### Caddy — the edge trusts no client (`internal/caddy/caddy.go`, `dev/caddy.json`, `dev/control-plane/caddy.json`)

`EnsureServer` now declares `trusted_proxies: {source: static, ranges: []}` on the `malmo` server before resetting the routes, and both bootstrap `caddy.json` files declare the same key so the posture holds from Caddy's first second, before the brain connects. It is `POST`, not `PUT`: Caddy's admin API answers a `PUT` onto an existing object key with `409 key already exists` (verified against a live admin API), while `POST` creates-or-replaces, which keeps this idempotent across brain restarts. A rejected declaration fails `EnsureServer` rather than proceeding to publish routes under an unknown trust model.

### Tests

- `internal/api/clientip_test.go` — the trust boundary as a table: forged header inert without a trusted set and from an untrusted peer; right-to-left walk with forged hops behind the real one; trailing trusted hops skipped; chain split across header lines; garbage hop fails closed; all-hops-trusted falls back to the peer; IPv6 canonicalisation. Plus `ParseTrustedProxies` (bare address ⇒ /32, host bits masked, rejects junk) and a check that the shipped default covers Docker/loopback/private peers and no routable address.
- `internal/api/ratelimit_test.go` — `TestRateLimit_ForgedForwardedForSharesOneBucket` drives the **real chain** (`authMiddleware` → `rateLimit`) with N distinct forged `X-Forwarded-For` values from one peer and asserts the bucket still empties. Its mirror, `TestRateLimit_TrustedProxyForwardedForIsHonoured`, asserts two LAN clients behind a trusted Caddy still get their own buckets — a "fix" that just ignored the header would pool the whole LAN behind Caddy's address and let one attacker throttle everyone.
- `internal/api/auth_test.go` — `TestLoginThrottleIgnoresForgedForwardedFor` sprays 11 distinct usernames (so only the per-IP gate can fire) with a distinct forged header each, over real HTTP through the harness, and asserts the 429 arrives.
- `internal/caddy/caddy_test.go` — `EnsureServer` declares the empty trusted set as a `POST`, and fails without touching the routes when Caddy rejects it.
- `cmd/brain/main_test.go` — the env-var wiring: unset ⇒ the private-range default, explicitly empty ⇒ trust nothing, explicit spec ⇒ parsed. The unparseable case calls `fatal()` and exits the process, so it is deliberately uncovered.
- `dev/test-caddy-routing.sh` — the manual real-Caddy lane (`make test-caddy`, needs `make dev`) gains **TEST 1b**: a request to the installed whoami app carrying a forged `X-Forwarded-For` must not have either forged hop echoed back. `whoami` prints the headers it received, so this observes the trust boundary through a real Caddy rather than a fake admin API — the one thing the unit tests structurally cannot prove.

`make fmt-check`, `make vet`, `make openapi-check` and `make test-nopam` all green (the PAM cgo target doesn't build in this environment; it is untouched by this change).

### Verified against a real Caddy, and the issue's premise corrected

The issue states that Caddy without `trusted_proxies` *appends* to a client-supplied `X-Forwarded-For`, so a forged first hop survives to the brain. Tested against a real `caddy:2-alpine` (**2.11.4**) proxying to a local echo server:

- **No `trusted_proxies`** (malmo's config before this change): an inbound `X-Forwarded-For: 1.2.3.4, 5.6.7.8` reaches the upstream as the **peer address alone** — the forged chain is dropped. Same result with the explicit empty static set this change adds.
- **Peer in `trusted_proxies`**: the same request reaches the upstream as `1.2.3.4, 5.6.7.8, <peer>` — the forged hops survive, as they should for a genuinely trusted hop.

So on today's stock Caddy the header could not be smuggled through the proxy, and the exploit path in the issue's framing was narrower than described: it needed a peer that could reach the brain **without** traversing Caddy — a container on `malmo-ingress`, since the brain publishes no port. The bug in `clientIP` itself is real either way, and it is the half that matters: the brain was reading the attacker-writable end of the chain, and Caddy's replacement is an undeclared default that a version bump or a hand-edited `caddy.json` could change without anyone noticing. Both halves shipped.

## How it maps to the specs

- `AUTH.md` # Rate limiting keeps its per-username backoff and 10/min per-IP bucket unchanged; the added "Which IP" paragraph states what "per-IP" is allowed to mean, which the spec previously left implicit.
- `BRAIN_UI_PROTOCOL.md` # Rate limiting & abuse keeps all three planes and the 429 contract; the added paragraph pins the key-derivation rule and names `MALMO_TRUSTED_PROXIES`.
- No locked decision flipped, so no `DECISIONS.md` entry. `ENVIRONMENT.md` # Public-by-default is the reason this bites hardest on hosted (443 open to the internet) but needed no change.

## Known gaps & deviations

- **A peer inside the trusted set can still choose its own key.** On the appliance and on hosted, app containers share `malmo-ingress` with the brain, and the default trusted set covers that network, so a hostile *app container* could still forge a header the brain believes. Narrowing the default to Caddy's address alone needs the brain to resolve (and re-resolve) a container IP that changes on every Caddy restart; getting that wrong pools every user into one bucket, which is worse than the residual. An operator who wants the tighter posture can set `MALMO_TRUSTED_PROXIES` to Caddy's address today. Catalog apps are not in the threat model as adversaries (`THREAT_MODEL.md`), so this is deliberately left.
- **Not verified on a booted box.** The Caddy behaviour was verified against a real Caddy container as described above, and the brain side is covered by tests through the real middleware chain, but neither half has been exercised on a booted appliance or hosted image. The `trusted_proxies` key is declared in both bootstrap configs and re-applied by `EnsureServer`, so a box gets it from either path.
- **The real-Caddy assertion is in a manual lane, not CI.** TEST 1b lives in `dev/test-caddy-routing.sh`, which needs a running `make dev` stack and is not part of `make check`. So a future Caddy release that changed this behaviour would not turn CI red on its own — the explicit `trusted_proxies` declaration, not the test, is what actually pins the posture. The test's own assertion path (whoami echoing an `X-Forwarded-For:` line) was checked against a real `traefik/whoami` container; the lane itself was not run end to end here, because it installs and uninstalls an app in whatever dev stack it is pointed at.
- **The forward-auth verify exemption is unchanged.** #329 removes the reason it was written the way it was, but re-deciding it is #305's territory, not this issue's.

## What's next

1. Reconsider whether the hosted forward-auth verify path should carry a per-IP bucket now that the key is trustworthy (see [hosted-forward-auth-verify.md](hosted-forward-auth-verify.md) # Known gaps).
2. If app containers ever stop being trusted-by-assumption, narrow the default trusted set to Caddy's address and give the brain a way to keep that address current across a Caddy restart.
