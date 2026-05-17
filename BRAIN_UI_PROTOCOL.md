# Brain ↔ UI protocol

> The wire-level contract between the dashboard (browser TS client) and the brain. Sibling to `BRAIN_HOST_PROTOCOL.md` (which specs the brain ↔ host-agent boundary). Same four-pattern shape so engineers learn one model end-to-end.
>
> Companion to `CONTROL_PLANE.md` (architecture context), `AUTH.md` (session cookie), `WEB_UI.md` (client-side stack consuming this protocol).

## Scope

Everything the dashboard, a future CLI, a future third-party app store, or any external integrator does against the brain. This is malmo's **public API surface** from day one — there is no separate internal route table.

## Transport

HTTPS via Caddy → brain. Browser-native fetch / EventSource / WebSocket. No bespoke client library required.

## Wire format

HTTP/1.1 + JSON. Versioned URL prefix `/api/v1/...`. The UI bundle declares its expected API version (`X-Malmo-API-Version`); brain returns **426 Upgrade Required** on mismatch.

## API patterns

Four patterns, same rule as host-agent. Sync for short ops, jobs for anything that can exceed ~5 seconds, SSE for one-way streams, WebSocket reserved for future bidirectional needs.

### Pattern A — Sync request/response

For anything under ~5s and not needing progress.

```
GET  /api/v1/apps              → list installed instances
GET  /api/v1/apps/:id          → instance detail
POST /api/v1/users             → create user
GET  /api/v1/settings/network  → current network config
GET  /api/v1/health/issues     → active health issues (see HEALTH.md)
POST /api/v1/health/:id/:act   → invoke a remediation action attached to an issue
```

Plain HTTP. Errors: HTTP status + `{ "code": "...", "message": "...", "details": {...} }`. Codes are stable strings; messages are human-readable, not contractual.

### Pattern B — Jobs

For anything that can exceed ~5s or needs progress / cancel: app install, app update, mkfs, Tailscale enrollment, OS update, large config migrations.

```
POST /api/v1/apps
  { "manifest_id": "photoprism", "config": {...} }
→ 202 Accepted
  { "job_id": "j_a4f7b2", "kind": "app-install", "status": "running" }

GET  /api/v1/jobs/j_a4f7b2
→ 200 OK
  { "job_id": "j_a4f7b2", "kind": "app-install", "status": "running",
    "progress": 0.42, "step": "pulling_images", "started_at": "..." }

POST /api/v1/jobs/j_a4f7b2/cancel
→ 200 OK
  { "job_id": "j_a4f7b2", "status": "cancelling" }
```

Status values: `running`, `completed`, `failed`, `cancelled`, `cancelling`, `stalled` — same vocabulary as host-agent jobs. On `completed`, the response carries `result`. On `failed`, an `error` with `code` + `message`.

Some brain jobs internally delegate to host-agent jobs (an app install does brain-side work; a `mkfs` is essentially a passthrough). The brain owns its own job ID space; the host-agent job ID is an internal implementation detail.

**The rule:** if a route can exceed ~5 seconds, it's a job. Bias toward "make it a job" when uncertain.

### Pattern C — SSE (server → client streams)

Two distinct stream types:

**1. Per-resource log / progress tails.**

```
GET /api/v1/jobs/j_a4f7b2/log         — install/update/mkfs output
GET /api/v1/apps/:id/log              — container log tail (forwarded from Docker)
GET /api/v1/services/smbd/log         — Tier-2 service log (forwarded from host-agent journalctl)
```

For app and service logs, the brain is a transparent forwarder over the host-agent SSE stream (`BRAIN_HOST_PROTOCOL.md` Pattern C). Events flow end-to-end with no translation; the brain re-emits IDs from its own monotonic counter so dashboard `Last-Event-ID` replays work even across brain restarts.

**2. Global event stream — dashboard liveness.**

```
GET /api/v1/events
→ Content-Type: text/event-stream

  id: 1
  event: app.state_changed
  data: {"instance_id":"...","state":"running","prev":"installing"}

  id: 2
  event: update.available
  data: {"instance_id":"...","from":"2.4.1","to":"2.4.2"}

  id: 3
  event: drift.surfaced
  data: {"surface":"smbd","desired":"enabled","actual":"disabled"}

  id: 4
  event: health.issue_raised
  data: {"id":"data-drive-missing","severity":"error","summary":"Your data drive isn't connected."}

  id: 5
  event: health.issue_cleared
  data: {"id":"data-drive-missing"}
```

One long-lived stream per dashboard tab. Carries typed events for: app lifecycle transitions, updates available / applied / failed, drift surfaces, peer / mesh events (when mesh ships), Tier-2 service state changes, **health issues raised / cleared / updated** (see `HEALTH.md`), user notifications.

**Blocked-operation responses.** When a request is refused because a health issue's `blocks_writes` / `blocks_apps` / `blocks_users` flag is set, the brain returns `409 Conflict` with `{code: "blocked-by-health-issue", issue_id: "...", message: "..."}`. The UI uses `issue_id` to link the user from the failed action back to the banner explaining why.

**Event `kind` values are enumerated in the API schema.** No untyped `{type, data}` blobs. Adding a new event kind is an API-version-bumping change.

**Reconnect resilience.** Same as host-agent: monotonic event `id`, rolling buffer (~256 KB per stream), client sends `Last-Event-ID: <n>` on reconnect, brain replays from `n+1`. If the gap exceeds the buffer, brain emits one `{"lost": true}` event and resumes from current.

**Stream cap.** Brain enforces ≤16 concurrent SSE streams per session — backstop for buggy dashboards or many open tabs. Excess connections receive `429 Too Many Requests`.

### Pattern D — WebSocket (future, reserved)

Reserved for the web terminal (`NEXT.md` Tier 3). HTTP upgrade on the same server. No v1 pre-design. The terminal has its own security implications (root PTY = root on host) that need separate design — `AUTH.md` already locks the gating gesture (re-type dashboard password for a root shell).

## API discipline

**Authentication.** Opaque `malmo_session` cookie (per `AUTH.md`). No bearer tokens, no JWTs. The cookie carries the SSE handshake and the future WebSocket upgrade — no separate auth path. CSRF is handled by `SameSite=Strict` on the cookie plus an `Origin` check on state-changing requests.

**Versioning.** The API is **versioned and additive**, not lockstep. Brain serves under `/api/v1/...`. Minor versions are additive — fields are added, never removed or repurposed. The UI bundle declares the API minor it requires (in `version.json`); brain accepts any UI built against `v1.X` where `X ≤ current_minor`. Breaking changes go to `/api/v2`, which the brain serves alongside `/api/v1` during the deprecation window.

This is deliberately *not* lockstep with the brain version. The UI and brain ship as separate images on a shared release channel (`WEB_UI.md` # "deploy + update flow") and iterate at independent cadences. Most UI ships don't move the brain; most brain ships don't move the UI.

**The `426 Upgrade Required` path is the in-tab safety net.** When the user has a dashboard tab open and the UI container updates underneath them, the next API call from the stale tab may declare a `version` the brain no longer supports (or the inverse — the UI just updated to require an API minor the brain doesn't yet serve, during a coordinated ship between minor pull and brain restart). On `426`, the UI shows "malmo updated — refresh to continue."

**Public-API posture.** The API the dashboard uses **is** the API a future CLI, third-party app store, or external tool will hit. Concretely:

- Stable URLs, stable error codes, stable event `kind` values.
- No hidden auth shortcuts the dashboard uses but external callers can't (e.g. no "internal" routes outside `/api/v1/`).
- Rate-limit and abuse posture is a `NEXT.md` follow-up — public-callable from day one means we'll need it before third-party stores ship.

**Codegen.** Split. **Server-side OpenAPI 3 emission lands from day one** — the brain is written using [`huma`](https://huma.rocks), a Go web library that produces an OpenAPI schema as a byproduct of handler registration. Typed request/response Go structs *are* the schema; there is no hand-maintained `openapi.yaml`. The schema is the substrate for the CI enforcement described below. **Client-side codegen is deferred** — the UI keeps hand-rolled TS types in v1; the generated TS client (`openapi-fetch` or similar) lands as a follow-up before the public-API surface goes external, when schema stability makes hand-maintenance cost more than codegen.

### CI enforcement

The additive-minor discipline above is a *contract* — with in-flight UI tabs during a malmo update, and with external callers (third-party stores, CLI, future tooling) per the public-API posture. The cost of breaking it is paid by callers, not the change author. Discipline-by-convention decays; CI is the mechanism that internalizes the cost so a breaking change can't merge silently.

**Mechanism: generated OpenAPI + `oasdiff breaking`.**

On every PR, CI:

1. Builds the brain and writes `openapi.json` (a `make openapi` target or a tiny Go binary that calls `api.OpenAPI()`).
2. Runs `oasdiff breaking origin/main:openapi.json HEAD:openapi.json`. Non-zero exit = breaking change detected = build fails.

[`oasdiff`](https://github.com/tufin/oasdiff) (Tufin, Apache-2.0) has a closed, rule-based notion of "breaking": response field removed; response field type changed or narrowed; enum value removed; endpoint removed; request field newly required; optional response field becoming nullable; error-code enum value removed; etc. The check is **structural, not heuristic** — it sees the declared schema, not a sampled set of responses. A field declared in a Go struct is in the schema whether any test exercises it or not; an enum value listed in a Go type is in the schema whether any test fires it or not.

**Two nudges to make this work cleanly:**

- **Event `kind` and error `code` are first-class Go enum types**, each registered in a single file (`events/kinds.go`, `errors/codes.go`). Each appears in OpenAPI as a named enum schema; oasdiff catches removals from either set the same way it catches field removals.
- **PR template includes a "does this change `/api/v1`?" checkbox.** If checked, the reviewer is on the hook for confirming the change is deliberate — mechanical CI catches the schema, the checkbox catches *intent* (e.g., a copy-paste accident that happens to produce a clean diff).

**Why generated, not hand-written OpenAPI.** A hand-written `openapi.yaml` becomes a second source of truth that drifts from the Go code — and the drift isn't caught until a PR breaks something real for a caller. Generated-from-types makes drift structurally impossible: the schema is the byproduct of the code that serves it.

**Why this over snapshot-testing responses.** Snapshot tests verify what they call. Coverage gaps — endpoint never tested, enum value never observed, error response shape, omitted optional fields — silently let breaking changes through. The schema diff is the contract diff; the snapshot diff is "did anything I happened to look at change?"

**No escape hatches.**

- No "let this one through" CI flag. Bypass is "move to `/api/v2`," not "skip the check."
- No grace period for newly-added enum values to be removed later. Once landed, additive forever.
- No "internal" routes outside `/api/v1` that escape the discipline. Public-API posture is from day one.

**Debuggability is a first-class design constraint** (inherited from `BRAIN_HOST_PROTOCOL.md`). Anything the dashboard does is reproducible with `curl` and a session cookie:

```
curl -b "malmo_session=..." http://malmo.local/api/v1/apps
curl -b "malmo_session=..." -N http://malmo.local/api/v1/events
```

Future changes that would make the protocol harder to debug from `curl` need explicit justification.

## Locked decisions

- **Transport:** HTTPS via Caddy. No direct brain port exposure.
- **Wire format:** HTTP/1.1 + JSON, versioned URL prefix `/api/v1/...`.
- **API patterns:** sync request/response (A) for <5s; jobs (B) for longer or progress-reporting ops; SSE (C) for one-way streams; WebSocket (D) reserved for future bidirectional needs.
- **Authentication:** opaque `malmo_session` cookie. No bearer tokens. SSE/WS auth via the same cookie.
- **CSRF:** `SameSite=Strict` cookie + `Origin` check on state-changing requests.
- **Versioning:** API-versioned, additive-minor. `/api/v1` minors only add fields; breaking changes go to `/api/v2`. UI and brain ship independently on a shared release channel (`WEB_UI.md`). UI declares `X-Malmo-API-Version`; brain returns 426 if it can't serve that minor.
- **Additive-minor discipline.** Fields in `/api/v1` are never removed or repurposed. New fields are always optional. Event `kind` values are added, never removed (deprecation = stop emitting). **CI enforces via generated OpenAPI + `oasdiff breaking`** (see # CI enforcement). Bypass is `/api/v2`, not a skip flag.
- **Errors:** HTTP status + `{code, message, details?}` body. Codes are stable strings.
- **Event `kind` values are enumerated in the schema.** Adding a new kind is an API-version-bumping change.
- **SSE reconnect:** monotonic `id`, ~256 KB per-stream rolling buffer, `Last-Event-ID` replay, single `{"lost": true}` event on overflow.
- **Stream cap:** ≤16 concurrent SSE streams per session.
- **Codegen split.** Server-side: OpenAPI 3 emitted by the brain via [`huma`](https://huma.rocks) from day one (substrate for CI enforcement). Client-side: TS types hand-rolled in v1; generated TS client (`openapi-fetch` or similar) lands before public-API surface goes external.
- **Public-API posture from day one.** Dashboard uses the same routes any external caller will hit. No internal carve-outs.
- **Debuggability is a first-class design constraint.** Future changes that hurt `curl`-debuggability need explicit justification.

## Knock-ons to other docs

- `CONTROL_PLANE.md` — points to this doc as the authoritative spec for the brain↔UI boundary.
- `BRAIN_HOST_PROTOCOL.md` — sibling protocol; SSE/jobs patterns deliberately identical.
- `AUTH.md` — `malmo_session` cookie semantics live there.
- `WEB_UI.md` — client-side consumers of this protocol (`@tanstack/vue-query`, `useEvents()`, `useJob()`).
- `NEXT.md` — carries the OpenAPI codegen-timing and rate-limit / abuse-posture follow-ups.
