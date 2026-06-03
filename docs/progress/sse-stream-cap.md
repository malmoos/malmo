# SSE per-session stream cap (≤16 → 429)

- **Status:** done
- **Date:** 2026-06-01
- **Specs touched:** none changed (realizes `BRAIN_UI_PROTOCOL.md` # Stream cap, lines 170/188/261 — already specified; no divergence)

Implements the ≤16-concurrent-SSE-streams-per-session cap (issue #47) that `BRAIN_UI_PROTOCOL.md` locks but nothing enforced. Surfaced as a follow-up by [system-live-sse.md](system-live-sse.md), which added `/api/v1/system/live` ("counts against the ≤16-stream cap") while the cap itself was unbuilt — there was no counter, no `429` path, and no per-session stream tracking anywhere, not even for the older `/api/v1/events` stream. Closes issue #47.

## What was done

The cap is **cross-cutting** — it must span every raw SSE handler and key off the session, not the endpoint — so it lives on `Server`, not inside either handler.

- `internal/api/streamcap.go` (new) — `streamCap`: a `map[token]int` of live streams guarded by a mutex, with `acquire(token) (release, ok)` and `release(token)`. `acquire` refuses (`ok=false`) once a session is at `maxStreamsPerSession` (16); `release` frees one slot and drops the map entry when a session's last stream closes (so churning sessions don't leak keys). `beginStream(w, r)` is the one-call front door for a raw SSE handler: it resolves the session via `auth.FromContext`, writes `401` if absent, reserves a slot, writes `429` if the session is capped, and otherwise returns a `release` the handler defers. `writeStreamCapExceeded` mirrors `writeUnauthenticated`'s hand-written error shape (the SSE handlers sit outside huma).
- `internal/api/api.go` — both raw SSE handlers (`events`, `systemLive`) now open with `release, ok := s.beginStream(w, r); if !ok { return }; defer release()` before any stream headers are written, so a refused stream gets a clean `401`/`429` status with no `200`/event-stream headers leaking first. `systemLive`'s prior standalone `auth.FromContext` belt-and-suspenders check folds into `beginStream` (no behaviour lost — it still double-guards auth, and now also keys the cap). `Server` gained a `streams *streamCap` field, constructed in `NewServer` (no signature change → no test-caller churn).

The slot is freed by the deferred `release()` when the handler returns, which happens on disconnect (`r.Context().Done()`) — so a closed tab or dropped connection frees its slot.

## How it maps to the specs

- `BRAIN_UI_PROTOCOL.md` # Stream cap (line 170, locked at line 261): "Brain enforces ≤16 concurrent SSE streams per session … Excess connections receive `429 Too Many Requests`." Realized exactly: the 17th concurrent stream on a session is refused with `429`.
- `BRAIN_UI_PROTOCOL.md:188` — "[`system/live`] still counts against the ≤16-stream cap." Both `/api/v1/events` and `/api/v1/system/live` increment one shared per-session count, so the cap is a per-session budget, not per-endpoint.
- Auth is the `molma_session` cookie (`AUTH.md`): the cap keys on `Identity.Session.Token` from `auth.FromContext`, the same identity the middleware attaches. A second session has its own budget.
- CLAUDE.md # Go code discipline: small self-contained type for one concern, no premature abstraction (the limit is a const, injectable only so a test can shrink it); no new dependency; the `429` body is hand-written to match the sibling raw-handler error, not a new error framework.

## Known gaps & deviations

- **`Retry-After: 0` on the `429`.** `BRAIN_UI_PROTOCOL.md` # Rate limiting locked decision requires `Retry-After` on every `429`; for the stream cap the slot frees when a tab closes rather than after a fixed delay, so the value is `0`. The response body uses the locked `{code: "rate-limited", message, details: {scope: "session"}}` envelope.
- **The cap counts only the two raw SSE handlers that exist today** (`events`, `system/live`). The per-resource log/progress tails in `BRAIN_UI_PROTOCOL.md` Pattern C stream 1 (`/api/v1/jobs/:id/log`, `/api/v1/apps/:id/log`, `/api/v1/services/:svc/log`) are not implemented yet; when they land they must also call `beginStream` to participate — noted here so it isn't silently assumed handled. `beginStream` is the single seam to wire them through.
- **Not exercised under a real browser.** Verified over real HTTP with the Go client (long-lived streams held open concurrently); the `EventSource`-stops-on-429 behaviour is a browser property, not tested here.
- **Schema/`oasdiff` impact: none.** The SSE endpoints are raw mux handlers outside huma, so they're absent from the generated OpenAPI; adding a `429` path changes no schema and is additive regardless (`BRAIN_UI_PROTOCOL.md` # CI enforcement).

## Tests

- `internal/api/streamcap_test.go` (new) — unit tests on `streamCap` (acquire up to the limit then reject, a release frees exactly one slot, per-token independence, map entry dropped at zero, and the production limit pinned to the spec-locked 16) plus the Done-when end-to-end over real HTTP: filling a session's budget across **both** `/api/v1/events` and `/api/v1/system/live` refuses the next stream with `429`; a second session keeps its own full budget; closing a stream frees a slot for a new one. Race-clean. The HTTP test shrinks the cap to 3 (swapping `Server.streams` before any stream opens) so it holds a handful of connections, not 17.

## What's next

- **Wire the log/progress-tail SSE handlers through `beginStream`** when they're built, so all SSE streams share the per-session count.
- **Rate-limit / abuse posture** (`BRAIN_UI_PROTOCOL.md` # Public-API posture, `NEXT.md`): the stream cap is a per-session backstop, not a request rate limiter — the broader public-API rate-limit story is still a `NEXT.md` follow-up.
