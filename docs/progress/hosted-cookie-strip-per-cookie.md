# Hosted app routes strip only `malmo_forward_auth`, not the whole `Cookie` header

- **Status:** done
- **Date:** 2026-07-16
- **Specs touched:** docs/specs/ENVIRONMENT.md, docs/specs/DECISIONS.md

## What was done

Closes #335. The hosted route builder stripped the **whole `Cookie` request header** from every app route (#306). That also removed the app's own session cookies, so a third-party app with a browser login could not authenticate on hosted at all: it re-issued a session on every request, and correct credentials bounced off the login form.

- `internal/caddy`: `RouteConfig.StripCookie bool` becomes `RouteConfig.StripCookieName string`. `AddRoute` emits `headers.request.replace` on `Cookie` instead of `headers.request.delete`, via a new `stripCookieReplacements(name)`. Two ordered passes: `(^|;\s*)<name>=[^;]*` removes the cookie wherever it sits, then `^;\s*` cleans the separator left behind when it sat first. The name is `regexp.QuoteMeta`'d.
- `internal/lifecycle`: `buildRouteConfig` sets `StripCookieName: auth.ForwardAuthCookieName`. Carrying the name rather than a bool keeps one source of truth: a rename in `internal/auth` cannot silently stop the strip and leak the token.
- Appliance is untouched: `StripCookieName` stays empty, so the emitted route is byte-for-byte the plain `reverse_proxy` (`TestAddRoute_Plain` asserts no `headers` block at all).
- The forward-auth subrequest still carries `Cookie` to the brain verify endpoint. The strip is on the app `reverse_proxy` only, which was already the case and stays asserted.

## How it maps to the specs

This is the follow-up the #306 decision named for itself. `DECISIONS.md` 2026-07-08 stated the whole-header tradeoff plainly and wrote: "per-cookie surgical stripping is the follow-up if a real app needs its own cookies." Nextcloud is that app, and it turned out to be the common case rather than an edge one, so the follow-up landed on first contact.

`ENVIRONMENT.md` # Public-by-default is updated on three points: the strip is per-cookie; the "standard oauth2-proxy / Authelia forward-auth shape" claim is corrected (those strip their own cookie and pass the rest through, which is what this now does, and is not what the whole-header delete did); and the cookie-tossing consequence is recorded. `DECISIONS.md` carries the flip, narrowing the first shape call of the #306 entry and leaving the second (hosted default `restricted`) untouched.

## How it was tested

`make check` green. Beyond that, per contributing.md # Step 4 (this integrates with a real external system, so unit tests are not enough):

- **Against real Caddy, with the config the code actually emits.** A throwaway program called the real `AddRoute` and dumped the emitted route JSON, which was loaded into a real Caddy container in front of a stock `nextcloud:34.0.1-apache` (managed MariaDB, `cap_drop: ALL`, non-root uid, mirroring the hosted sandbox) and an echo upstream. Re-run against the final config after review. Results: two sequential `GET /login` (both HTTP 200 on a freshly installed instance) return the **same** session id (`d80d58cf…`), where the whole-header strip returned a different id on every request; a `Cookie` carrying the token reaches the app as `app_sess=abc; other=1`; `evil_malmo_forward_auth=junk; oc_sessionPassphrase=xyz` arrives intact; duplicate `malmo_forward_auth` names both go; and a request with no `Cookie` header reaches the upstream with the header still **absent**, not created empty.
- **The reproduction, first.** Same rig on the pre-fix config reproduced the reported symptom exactly: `occ user:list` shows `admin`, basic auth cleanly separates a correct password (200) from a wrong one (401), and the browser login still fails, because the session cannot survive a request.
- **Adversarial table in CI** (`TestAddRoute_CookieStripBehaviour`): typical, token first/last/only, empty value, no cookies, nothing-to-strip, duplicate names in both orders, prefixed and suffixed names. Each asserts both halves of the invariant (the token never survives; nothing else is touched). It drives the **emitted** replacements rather than a copied pattern, so a regex change in `caddy.go` is one these tests follow.
- **Mutation-checked.** Both tests were confirmed to fail against a deliberately broken strip before being trusted: the unanchored regex fails `prefixed_name` with the real corruption (`evil_app_sess=abc`), and a non-global replace fails with "forward-auth token reached the app upstream".

## Known gaps & deviations

- **Cookie tossing between apps is now possible, and accepted.** An app can set a `Domain=<box-id>.malmo.network` cookie that a sibling app receives (session fixation). Inherent to subdomain-per-app hosting on one registrable domain; recorded in `ENVIRONMENT.md` and `DECISIONS.md` rather than left implicit. The whole-header strip blocked it only as a side effect of leaving no app working.
- **The CI table is not literally "through Caddy".** CI's Go job has no Caddy and no Docker, so the table applies the emitted `search_regexp`/`replace` pairs with Go's `regexp` and `ReplaceAllString` — the same engine and call Caddy uses for `search_regexp`. The real-Caddy run above is the proof that the semantics match; it is a manual step, not a CI gate.
- **Global replacement is relied on and pinned by test, not by config.** That Caddy replaces *every* occurrence (so an app cannot set its own copy of the name and keep the real token) is a property of Caddy's implementation, not something the emitted JSON states. `duplicate names` covers it in both orders, so a Caddy change here fails loudly rather than silently leaking.
- **The #308 login round-trip is not extended here.** The regression that would have caught this at the e2e layer (a real login through the gate, asserting session stability) belongs in that lane and is left to it; #335's acceptance is met by the real-Caddy verification above.
- **The empty-`Cookie` case is benign but untested against every app.** When the forward-auth cookie was the only one, the app receives `Cookie: `. Verified harmless against Nextcloud and the echo upstream; not exhaustively checked. (A request that carried *no* `Cookie` header is a different, verified case: the header stays absent.)

## Review

Self-review per contributing.md # Step 6 raised one Block and three Notes, all addressed in this branch: `docs/architecture.md` was stale in the same PR that changed the code (the `auth` row's "Imported by" column predated the new `lifecycle` → `auth` edge, and the `caddy` row still described a whole-header strip); `applyEmittedCookieStrip` type-asserted its way to the `replace` key without comma-ok, so a regression back to `delete` — the exact thing it guards — would have panicked instead of reporting; and the alternation was a capturing group, inert only because the replacement is empty, now `(?:…)`. The real-Caddy verification above was re-run against the post-review config rather than carried over.

## What's next

- #308: add the cookie-leak probe for **both** exposure modes and the session-stability login round-trip to the e2e lane.
- A `memos` install returning 502 on hosted was seen alongside this and is **not** explained by it (the gate 302s correctly; the 502 is an upstream dial failure after the gate passes). Needs its own investigation.
