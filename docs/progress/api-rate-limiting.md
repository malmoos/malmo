# General API rate limiting (per-session + per-IP request throttling)

- **Status:** done
- **Date:** 2026-06-03
- **Specs touched:** none — implements the already-locked posture (`BRAIN_UI_PROTOCOL.md` # Rate limiting & abuse, # Locked decisions; `DECISIONS.md` 2026-06-02). No divergence, so no spec edit.

Closes issue #53. The brain↔UI API is molma's public surface from day one, but until now there was no general request throttling — a runaway dashboard tab, a tight CLI loop, or a compromised LAN device could grind a modest single-node box into CPU/goroutine/memory pressure. The 2026-06-02 spec drop locked the posture (throttle-not-ban, in-memory, three orthogonal planes) but only plane 3 (the ≤16-stream SSE cap, issue #47/`streamcap.go`) was built. This adds the two request-rate planes — per-session and per-IP — with the locked `429` + `Retry-After` contract.

## What was done

- **`internal/api/ratelimit.go`** (new) — a token-bucket limiter, in-package alongside `streamcap.go` (its sibling plane). `bucketSet` is a keyed family of lazily-refilled token buckets sharing one capacity (= instantaneous burst) and refill rate (= sustained rate); `allow(key)` returns `(true, 0)` when a token was free or `(false, retry)` when empty, where `retry` is the time until one token refills. `rateLimiter` bundles the two planes:
  - **Plane 1 — per-session:** keyed on the `molma_session` token, **120 req/min sustained, burst 60**.
  - **Plane 2 — per-IP:** keyed on `clientIP(r)`, **30 req/min/IP**, governing the unauthenticated allowlist only.
- **`rateLimit` middleware** — wired into the served chain as `withCORS(authMiddleware(rateLimit(mux)))` (`api.go`). It sits *after* auth resolves identity and *before* the mux, so it keys on the session token when authenticated and falls back to client IP otherwise. Long-lived streams (`/api/v1/events`, `/api/v1/system/live`, and the future streaming `/api/v1/files/content`) are exempt — they're governed by the per-session stream cap (plane 3), not the request-rate bucket, so opening a stream never draws from plane 1.
- **`429` contract** — `writeRateLimited` emits the locked envelope `{ "code": "rate-limited", "message": "...", "details": { "scope": "session"|"ip", "retry_after_s": N } }` plus a `Retry-After` header in whole seconds (floored at 1). The message is plain-English for the dashboard ("molma is busy — please retry in a moment."); the raw scope/IP/retry detail goes to `slog` only (plane 2 logs the IP at `warn`, plane 1 the user at `info` — neither bans).
- **GC of idle buckets** — `allow` opportunistically sweeps buckets untouched for longer than `rateLimitIdleTTL` (5 min), gated to run at most once per `rateLimitReapEvery` (5 min) via the injected clock. No goroutine, no persistence: the whole limiter resets on restart, mirroring the login throttle and `streamcap`.

## Design decisions (within the locked spec)

- **Kept in `internal/api`, not a new `internal/ratelimit` package.** The spec offered either ("a small `internal/ratelimit` package if the state/GC warrants it, else keep it on `Server`"). There is exactly one consumer (the api middleware), and the GC is a few lines, so per CLAUDE.md's "no premature abstraction / no interface until two consumers" it lives next to its only sibling plane (`streamcap.go`), consumer-side. Promote to a package if a second consumer ever appears.
- **Opportunistic, clock-gated reap instead of a background goroutine.** Bucket-map growth is *traffic-coupled* (a new key only appears on a request), so the reap is too — it piggy-backs on `allow` and runs at most once per interval. This needs no `cmd/brain` wiring, no goroutine lifecycle, and is fully deterministic under a fake clock. The honest trade-off (below): a plane that goes *completely* silent keeps its last keys until traffic resumes — bounded and trivial at LAN scale.
- **Injected clock (`now func() time.Time`)** so refill and reap are testable without sleeping; production passes `time.Now` from `NewServer`.

## How it maps to the specs

- `BRAIN_UI_PROTOCOL.md` # Rate limiting & abuse — both request-rate planes at the locked thresholds (120/min burst 60 session; 30/min IP), the exact `429` + `Retry-After` contract, in-memory mutex-guarded buckets with periodic GC, throttle-not-ban, resets-on-restart. Plane 3 (SSE concurrency) is left untouched as the separate budget it's specced to be.
- # Mechanism — middleware "after auth resolves the session but before handlers", keying on session token or falling back to client IP on the allowlist, is implemented exactly (`withCORS(authMiddleware(rateLimit(mux)))`).
- `CLAUDE.md` # Go discipline — in-package, consumer-side; `log/slog` only with standard fields (`host`, `user_id`); no audit on a throttled request (pure throttling is not an elevation-class mutation); no speculative abstraction.

## Known gaps & deviations

- **Plane 2 governs the `publicPaths` allowlist only — by design, not a gap.** A request to a non-public route without a session is rejected by `authMiddleware` (cheap in-memory token validation) *before* it reaches the limiter, so it consumes no bucket. The spec scopes plane 2 to "the unauthenticated allowlist only" for exactly this reason: the expensive routes are either authenticated (plane 1) or on the allowlist (plane 2); a flood of bare 401s does negligible work and is intentionally ungoverned.
- **`/files/content` exemption is forward-looking.** The file-manager transfer endpoints (`FILES.md`, issue #49) aren't built yet, so no test drives a real one; `isStreaming` lists the path prefix now so the exemption is honest the moment those handlers land. A unit test pins that `isStreaming` returns true for it.
- **Login-throttle (#8) composition is untested here.** Plane 2 sits *above* the per-username/per-IP login backoff (`AUTH.md`, issue #8); `/login` keeps its own stricter throttle. #8 is unmerged, so the two per-IP mechanisms on `/login` aren't exercised together yet — when #8 lands, login's stricter bucket simply wins (it runs inside the handler, plane 2 in the middleware above it). No code here touches `/login`'s throttle.
- **Reap is traffic-coupled.** A plane with zero traffic never sweeps — but it also never grows, so the only residue is the finite set of keys that were active before traffic stopped (a handful at LAN scale). Growth-under-churn, the case the GC exists for, always coincides with traffic and is swept.
- **No per-route budgets, no whole-box ceiling, no file-transfer cap** — all explicit v1 non-goals (`BRAIN_UI_PROTOCOL.md` # Explicit non-goals); not built.

## Tests

`go test ./internal/api/` green (full suite, plus the non-PAM package list). New `ratelimit_test.go`:

- **Token bucket (fake clock, deterministic):** burst-then-throttle, refill-over-time (incl. capped at burst), per-key isolation, retry-after reflects the refill rate, retry-after floored at 1s, idle-entry reap, and selective reap (dormant dropped, active + fresh kept). A production-params test pins the spec thresholds (120/60, 30) so a careless constant edit fails loudly.
- **Middleware (httptest, limiter-only `Server`):** per-session throttle with the full `429` body + `Retry-After` header assertions, per-session isolation, the per-IP plane on an unauthenticated request (with per-IP isolation), and streaming exemption across all three exempt paths (asserting the session bucket is never even touched).
- **End-to-end wiring:** a real request through `Handler()` returns the locked `429` once a fixed-clock per-session budget is spent — proving the limiter is actually in the chain and keys on the resolved session.

(`go vet ./...` and `make test-nopam` still trip on the pre-existing root-owned `dev/test-qemu/mkosi.tools/boot/loader` permission error; ran the explicit non-PAM package list instead. The `cmd/host-agent` PAM-cgo build needs `libpam0g-dev`, absent in this dev box — pre-existing, unrelated.)

## What's next

- **Dashboard surface for the `429`.** The TS client should treat `code: "rate-limited"` as a non-alarming "molma is busy — retrying…" and back off on `Retry-After` (TanStack Query retry), per the spec — a `web-ui` follow-up, not this backend issue.
- **Login-throttle composition (#8).** When the login backoff lands, add a test that `/login`'s stricter per-IP bucket wins over plane 2.
- **File-transfer concurrency cap.** Deferred to `NEXT.md` (Tier-4 control plane); the streaming `/files/content` endpoints are ungoverned by all three planes until then.
