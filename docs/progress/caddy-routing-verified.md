# 0014 — Caddy subdomain routing verified + catch-all 404 wired

- **Status:** done
- **Date:** 2026-05-24
- **Specs touched:** `docs/specs/CONTROL_PLANE.md` (catch-all 404 invariant paragraph)

## What was done

Verification harness for the Caddy Host-header subdomain routing plus the
catch-all 404 invariant that was missing from the initial slice.

### Why the catch-all matters

Before this change, Caddy returned `HTTP 200 OK` with an empty body for any
unmatched hostname. That is a UX failure (a user visiting a stale or mistyped
URL sees a blank page with no explanation) and a test correctness failure (tests
that expected "non-200 for unmatched" could not distinguish "catch-all hit" from
"Caddy silently accepted the request"). Both problems are now fixed.

### `dev/caddy.json` — seed catch-all

`"routes": []` replaced with a single `molma-catchall` route — no matcher
(matches everything), `static_response` handler, HTTP 404, HTML body linking
back to the dashboard. Caddy starts with this in place so Test 0 works without
waiting for brain startup.

### `internal/caddy/caddy.go`

Three changes:

1. **Insert at index 0.** `upsertRoute` now POSTs to
   `/config/apps/http/servers/molma/routes/0` instead of appending, so each
   new per-app route is prepended and the catch-all stays at the tail.

2. **`EnsureCatchAll(ctx)`** — idempotent startup call. GETs
   `/id/molma-catchall`; if 200, logs "already present" and returns nil. If
   absent (any non-200), appends the catch-all via
   `POST /config/apps/http/servers/molma/routes`. Returns a wrapped error on
   transport failure (caller logs and continues — best-effort posture).

3. **`get(ctx, path) (int, error)`** helper — issues a GET to the admin API,
   returns the HTTP status code without erroring on non-2xx. Mirrors the
   existing `send` method; used only by `EnsureCatchAll`.

### `cmd/brain/main.go`

Calls `cd.EnsureCatchAll(ctx)` after `Reconcile` completes. Failure is
`slog.Warn` + continue — matches the "best-effort Caddy" posture in
`lifecycle.go:reassertRouting`.

### `dev/test-caddy-routing.sh`

Four changes:

1. **Defensive cleanup** (after preflight, before setup): removes stale
   `molma-app-*` routes from prior failed runs via jq + DELETE on each `@id`.
   Does not touch `molma-catchall`.

2. **[TEST 0]** (new, before install): unmatched hostname → expect exactly HTTP
   404 AND body contains "No app at this hostname". Proves the catch-all is
   wired with zero installed apps.

3. **Tightened negative tests** (Tests 2, 3, post-uninstall Test 4): changed
   "not 200" to "exactly 404" and added `grep -q 'No app at this hostname'` on
   the body. Proves the catch-all is responding — not an accidental HTTP error
   from another layer.

4. Updated header comment to list Test 0 and reflect the new exact-404
   expectations.

### `docs/specs/CONTROL_PLANE.md`

Added a "Catch-all 404 invariant" bullet to the Caddy section documenting the
`molma-catchall` route contract, the index-0 insert pattern, and
`EnsureCatchAll`.

## What was verified (automated)

- `bash -n dev/test-caddy-routing.sh` — syntax clean
- `CGO_CFLAGS=-D_GNU_SOURCE go vet ./...` — no issues
- `make test` — Go tests green
- `make build` — binary builds
- `jq . dev/caddy.json` — valid JSON

`make test-caddy` is the manual gate run against `make dev`.

## Dev port wrinkle

In dev, `dev/docker-compose.yml` maps container `:80` → host `:8088` because
host port 80 is taken on most laptops. In production Caddy listens on `:80`.
The `:8088` indirection means:

- `curl -H "Host: slug.molma.local" localhost:8088` works (correct Host header,
  port is just the TCP target).
- `curl --resolve slug.molma.local:8088:127.0.0.1 http://slug.molma.local:8088/`
  works (DNS override to loopback on the custom port).
- Browser via LAN IP on another device does **not** work against the dev stack:
  the browser would try port 80, not 8088. The secure-URL path
  (`MOLMA_NETWORK.md`) is the real-device compatibility path.

## Known gaps (loud)

- **HTTPS / Let's Encrypt for `.network` URLs not covered.** TLS termination,
  certificate provisioning, and the `<box-id>.molma.network` URL scheme are all
  deferred to `MOLMA_NETWORK.md`. This slice is HTTP-only.
- **Trust-proxy / X-Forwarded-For not validated.** Apps that depend on the
  real client IP see Caddy's container IP today. Not yet tested end-to-end.
- **No nspawn-lane automated coverage.** This script is a manual gate run
  against the dev inner loop. The nspawn and QEMU CI lanes in `TESTING.md` are
  not yet wired; subdomain routing isn't covered there.
- **Test user left behind after the run.** The script leaves the `caddytest`
  admin user in the database. `make clean` wipes state. Intentional and
  documented in the script header.

## What's next

- nspawn-lane: add a routing verification step once the lane is wired.
- HTTPS routing: validate Let's Encrypt + `<box-id>.molma.network` routes when
  `MOLMA_NETWORK.md` lands.
- X-Forwarded-For: add an assertion in `test-caddy-routing.sh` that the
  `X-Forwarded-For` header in the whoami echo contains `127.0.0.1`.
