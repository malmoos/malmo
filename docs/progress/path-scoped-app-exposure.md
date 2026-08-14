# Path-scoped app exposure: a token-authed API stays open while the UI keeps the box login

- **Status:** done - unit-tested locally; the real-Caddy proof is the hosted lane's `access` boot, which is CI-only (see Known gaps)
- **Date:** 2026-08-14
- **Specs touched:** `docs/specs/APP_MANIFEST.md` (# E2, new), `docs/specs/ENVIRONMENT.md` (# Per-app owner-only access), `docs/specs/DASHBOARD.md` (# Settings, the Access control), `docs/specs/UPDATES.md` (# Auto-apply unless permissions expand), `docs/architecture.md`

Closes #415. Follow-up to the closed epic #304: it builds on #306's central route builder and #305's verify endpoint, both of which stay as they were.

## The problem

Hosted exposure is whole-app and binary. An app is `restricted` (the box login in front of everything) or `public` (no login at all). That has no answer for the app that pairs a **token-authed API** with a **session-authed UI**, which is the normal shape for developer tooling:

- **Langfuse** ships today with the limitation `external-sdk-needs-public-app`: an application outside the box cannot send traces while the app is owner-only, because the box login sits in front of the tracing API as well as the UI.
- **Laminar** hits the same wall and worse. Its self-hosted frontend signs in **any email with no password**, so "make it public so the SDK can ingest" reads as "let anyone on the internet sign in as anyone". Its whole `/v1/*` surface is already project-API-key authed and needs no session at all.

## What was done

**One optional manifest field** (`internal/manifest`), with the validation carrying most of the weight:

```yaml
access:
  public_paths: ["/v1", "/v1/*"]
```

Two shapes and nothing else: an exact path, or a path plus everything under it. `/`, `/*` and `/**` are refused — they would make the whole app public while the dashboard still offers the owner an access toggle for it, so a manifest would be overriding a decision that is not the author's. `/v1*` is refused too, because it also matches `/v1admin`: an author reaching for "everything under /v1" would open a sibling path they never read. `%`, `?`, `#`, `\`, `//` and `..` are refused because Caddy matches a cleaned, decoded path while the app sees the original URI, so a declaration containing any of them means two different things on the two sides of the proxy. At most 16 entries, 128 characters each, de-duplicated case-insensitively (Caddy's path matcher is case-insensitive, so `/v1/*` opens `/V1/x` whether the author meant it or not).

**The validation is not the trust boundary, and the spec now says so.** These paths ship in the catalog, which malmo curates; a manifest that wanted to be hostile already chooses the app's images. The rules catch an author's mistake. Writing that down matters more than the rules, because the next person to read them would otherwise mistake them for a security control and relax them.

**One route, one `@id`, two branches** (`internal/caddy`). When a gated route has public paths, the flat `[forwardAuth, proxy]` becomes a `subroute`: the declared paths proxy straight through, and a matcher-less second route gates everything else exactly as before. `upsertRoute`'s remove-then-`PUT`-at-index-0 and the catch-all's evaluation order are untouched, because it is still one route. The proxy handler is **built once and used on both branches**, so the #335 per-cookie strip cannot end up on one and not the other.

**The identity scrub is now unconditional, and this is the part that was not in the issue's shape.** `X-Malmo-User` / `X-Malmo-User-Id` are deleted at the head of **every hosted app route**, before any gate runs. The gate keeps its own delete-before-set on the allow branch. The reason the outer scrub has to exist:

- The gate does not run on a public path, so nothing there would overwrite a caller-supplied header. The app would receive a brain-vouched header on one path and a forged one on another, with no way to tell them apart.
- The same hole already existed for a **fully public app**, and this closes it in the same line of code. The old argument — "a public app never receives vouched headers at all" — stops holding the moment an app can be both: an app that learns to trust `X-Malmo-User` while restricted keeps trusting it the second the owner flips it to Public.

**The route builder takes the manifest** (`internal/lifecycle`). `buildRouteConfig(inst, man, host, upstream)`; all four call sites already had `man` in scope. The gate still fires only on `hosted && restricted`, so the appliance route is byte-for-byte what it always was — no scrub, no strip, no gate — even for a manifest that declares public paths.

**The dashboard says what is actually true.** `GET /api/v1/apps/{id}` and the exposure `PUT`'s echo both carry the declared paths — through one `withPublicPaths` helper, because these are the two responses that carry an app's exposure and the access label is built from the pair; a response with the exposure but not the paths says "Only me" about an app that is partly open. The Access control names those paths instead of claiming a bare "Only you can open it". The paths are read from the **instance's own manifest copy** — the same file the route was built from — not from the catalog, so the label cannot drift from the route when the catalog moves on.

**The proof runs through real Caddy.** The hosted lane's `access` boot already installs an app and drives both exposure modes end to end; its test-catalog whoami now declares `public_paths`, and the boot asserts the parts a unit test cannot reach: the declared paths answer anonymously, a forged `X-Malmo-User` never reaches the app on either branch while the vouched one still does, `malmo_forward_auth` is stripped on the public branch too, and a **bypass table** never reaches the app anonymously — `/v1extra` (the prefix footgun), `/v1/../`, `//v1/`, `/v1/%2e%2e/`, `/V1x`, `/admin`. The requests are written raw onto the socket, so no client-side normalization softens them. The traversal result is the one worth knowing: `/v1/../` is **gated**, which is the box confirming that Caddy matches a cleaned path rather than the raw one.

## How it maps to the specs

`APP_MANIFEST.md` gains # E2 for the field and its rules. `ENVIRONMENT.md` # Per-app owner-only access gains the subroute shape and the unconditional scrub. `DASHBOARD.md` records that the label must name the paths. `UPDATES.md` gains one line in the permission-expansion diff: a new or widened `public_paths` entry prompts the instance owner, because it exposes more of the app to anonymous callers even though it sits outside the `permissions:` block. That line is **specified, not built** — the permission diff itself is unimplemented, and this entry does not implement it.

## Known gaps & deviations

- **The bypass table cost two corrections on its first CI run, and both are worth reading.** First: `//v1/` does not reach the gate at all — Caddy collapses the duplicate slash and answers `301` to the normalized path *before* matching. The app is never reached, so the probe was safe and the **expectation** was wrong; the table now asserts the claim that actually matters (an undeclared path never reaches the app upstream anonymously) and allows the merge-redirect shape for that one entry. Second, and worse: that failure was reported as a **green** CI run. The lane matched its verdict with a `*PASS*` glob, and the failure reason read "PATH GATE **BYPASS**" — so a genuinely red access boot printed `boot access OK`. The harness now anchors on the exact verdict string. Any assertion whose failure text contained *bypass*, *passphrase* or *password* would have done the same thing to any boot in this lane, so this was latent well beyond this PR.
- **Nothing in the catalog declares the field yet.** Langfuse's `status.yml` limitation and Laminar's screening note both still describe the wall as permanent; both are accurate until a catalog manifest actually declares its ingestion paths, which is a `malmoos/store` change, not this one.
- **The 16-entry cap is not a boundary.** An author with enough entries can still cover an app. The cap keeps the list reviewable; catalog review is what stops the case it cannot.
- **No audit trail on the public branch.** Anonymous requests bypass the brain entirely, so Activity never sees them. Inherent to the design, now stated rather than assumed.

## What's next

- Declare the field on Langfuse in `malmoos/store` and move its `external-sdk-needs-public-app` limitation off `by-design`; revisit Laminar's sign-in limitation when it is imported.
- Build the permission-expansion diff (`UPDATES.md`), which now has a `public_paths` clause waiting for it.
- Decide whether the box should rate-limit an anonymous public path. Today an owner-only app can carry an unauthenticated internet-facing surface with no budget in front of it, and the only thing between it and abuse is the app's own token check.
