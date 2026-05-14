# malmo Decisions Log

> Reverse-chronological log of decisions we made, decisions we *changed*, and the reasoning behind each. Not a changelog of code (there is no code). Not a list of locked decisions (those live at the bottom of each doc). This file captures the **evolution of thinking** — what we used to believe, what we believe now, and why we changed our mind.
>
> Read this when you're tempted to relitigate something. Add an entry whenever a load-bearing decision flips or a new one lands with non-obvious reasoning.

## Format

Each entry:

```
## YYYY-MM-DD — Short topic title

**Previously:** what we used to think / had written.
**Now:** what we believe now.
**Why:** the reasoning. Cite the specific friction, evidence, or principle that drove the change.
**Affected docs:** files updated as a result.
```

Keep entries skimmable. The detailed rationale lives in the affected doc; this file is the pointer + the *delta*.

---

## 2026-05-14 — Web UI deploy: dedicated container, release-manifest, API-versioned (not lockstep)

**Previously:** Two threads pointed in tension:
- `WEB_UI.md` left the deploy model open with three options: separate UI container; UI baked into brain image and served by Caddy from a shared volume; brain serves UI via `embed.FS`.
- `BRAIN_UI_PROTOCOL.md` had locked **lockstep versioning** — UI and brain ship as one unit with the OS release. The `426 Upgrade Required` path was framed as a transient-during-update artifact.

The lockstep posture made `embed.FS` look attractive (one binary, one update). But it also meant every UI tweak forced a brain release — wrong cost shape for the UI's natural iteration cadence, and inconsistent with malmo's "everything is a container" architecture.

**Now:** Three coupled decisions, locked together:

1. **Deploy model: dedicated `malmo-ui` container.** `caddy:alpine` base + UI bundle baked in at CI build time. Read-only FS, no secrets, no host privileges, no Docker socket. The simplest container in the stack. The existing LAN Caddy routes `/api/v1/*` → `malmo-brain`, everything else → `malmo-ui`.
2. **Versioning posture: API-versioned + additive-minor** (not lockstep). `/api/v1` minors only add fields; breaking changes go to `/api/v2`. UI declares its required minor in `version.json`; brain returns `426` only when it genuinely can't serve that minor. The `426` path is the in-tab safety net for "user had a tab open while the UI container updated," not a transient-during-OS-update artifact.
3. **Update mechanism: single release manifest, two artifacts.** One channel, one user-facing "auto-update malmo" toggle. The manifest names both `brain` and `ui` versions. Updater pulls + recreates only what changed: UI-only ship → recreate UI; brain-only → recreate brain; coordinated change → recreate both as one transaction with paired rollback.

**Why these vs. the alternatives:**

- **`embed.FS` (Option 3)** would have been the simplest engineering choice but breaks the "everything is a container" pattern we've been deliberate about, and forces every UI ship through a brain release — exactly the iteration-speed trap that drove the "separate codebase" lock in the first place.
- **UI baked into brain image + served by Caddy from shared volume (Option 2)** kept "one artifact" but introduced a shared-volume dance between brain and Caddy that's awkward semantics for marginal benefit.
- **Two separate update toggles (one for brain, one for UI)** was the obvious-but-wrong UX answer for Option 1. Rejected: the user has one mental model ("malmo updates"); the fact that internally there are two containers is implementation detail.
- **Lockstep versioning** was rejected because the UI ↔ brain relationship is the same shape as any web app ↔ backend: a versioned API with additive minors is the standard, well-understood discipline. Lockstep was over-coupling masquerading as conservatism.
- **`nginx:alpine` and `scratch + tiny Go static server`** were both viable UI-server bases. `caddy:alpine` won on consistency — we already run Caddy as the LAN reverse proxy; one HTTP server toolkit across the appliance.

**Headline calls:**

- **One channel, one toggle, two artifacts.** The release manifest is the primitive. The user never sees the brain/UI distinction at update time.
- **Most weeks, only the UI moves.** Brain stays running; no API gap. This is the iteration-speed payoff.
- **Additive-minor discipline is now load-bearing.** `/api/v1` fields can be added, never removed or repurposed. Event `kind` values are added, never removed (deprecation = stop emitting). CI enforces. This is the cost we pay for independent UI iteration.
- **Paired rollback on failed updates.** Brain + UI version pairs are tested together; a failed deploy reverts both, restoring the previous tested pair.

**Resolves:** the last open piece of `NEXT.md` Tier 1 "Web UI framework + deploy model."

**Open follow-ups (parked in `NEXT.md`):**

- Concrete release-manifest schema and publishing pipeline (the JSON shape is sketched in `UPDATES.md`; the build + signing infrastructure isn't).
- CI enforcement for additive-minor discipline (regression test that compares `openapi.yaml` between commits and fails on field removal / repurpose).

**Affected docs:** `WEB_UI.md` (Option 1 locked, deploy section rewritten), `BRAIN_UI_PROTOCOL.md` (lockstep replaced with additive-minor versioning, `426` reframed as in-tab safety net), `UPDATES.md` (§3 covers brain + UI as one stream with two artifacts; release manifest carries both versions), `NEXT.md`.

---

## 2026-05-14 — Web UI stack: Vue 3 + Tailwind + shadcn-vue + TanStack Query

**Previously:** `NEXT.md` Tier 1 "Web UI framework + deploy model" was open. The "UI is a separate codebase" / Vite / `/api/v1` proxy / version-handshake were locked, but the framework and the libraries on top were unspecified.

**Now:** Framework + library stack locked in `WEB_UI.md` § "Locked: stack." Deploy model (bundled in brain image vs. separate static-files container vs. served by the brain directly) remains open — narrowed Tier 1 item in `NEXT.md`.

**The lock list:**

| Layer | Pick |
|---|---|
| Language | TypeScript, `strict` |
| Framework | Vue 3, Composition API + `<script setup>` only (no Options API) |
| Build | Vite 5+ |
| Routing | Vue Router 4, history mode |
| State (client) | Pinia |
| State (server / cache) | `@tanstack/vue-query` v5 |
| HTTP client | Native `fetch` + ~30 LOC wrapper, designed to be swapped for `openapi-fetch` when codegen lands |
| SSE | Native `EventSource`, wrapped in a `useEvents()` composable that dispatches Query invalidations |
| Jobs polling | `useJob(jobId)` composable wrapping `useQuery` with terminal-status `enabled` |
| Styling | Tailwind CSS 4 (CSS-based config, no `tailwind.config.js`) |
| Components | shadcn-vue (copy-paste, owned in repo) on **reka-ui** (headless primitives — Vue port of Radix) |
| Icons | lucide-vue |
| Package manager | pnpm |
| Lint / format | ESLint (`eslint-plugin-vue` + `@typescript-eslint`) + Prettier |
| Node | Latest LTS, pinned via `.nvmrc` |

**Why these vs. the alternatives:**

- **Framework:** Vue, React, Svelte 5, and HTMX were all live candidates.
  - **React** was the safest ecosystem bet — most libraries, biggest hiring pool, best AI-assist code quality. Rejected because the velocity/identity wins it offers are smaller for a single-team admin SPA than its verbosity costs over a multi-year codebase.
  - **Svelte 5** was the runtime-feel and DX winner. Rejected because (a) the ecosystem is materially thinner — particularly for OpenAPI ↔ TanStack-Query integration that lands in the next codegen step — and (b) AI-assist code quality is materially worse in 2026 (less training data, Svelte 4 patterns leak in).
  - **HTMX + Go templates** was a serious wildcard for an admin dashboard. Rejected because the future web terminal needs a true SPA, and HTMX would force a hybrid model later.
- **Component library:** PrimeVue (fast MVP, hard to escape its look), Naive UI (best defaults, but its own theming system fights Tailwind), Vuetify (Material lock-in, wrong aesthetic), Element Plus (enterprise CRUD aesthetic), Headless UI Vue (too thin) were all weighed. **shadcn-vue on reka-ui** won because malmo's "polished, non-technical user, distinctive brand" target argues against off-the-shelf-looking libraries, and the shadcn ecosystem is where modern admin-app design language is being formed.
- **Data-fetching:** rolling our own thin composables (~200 LOC) was the alternative. Rejected for a 30+ screen dashboard: cache, dedup, retries, and optimistic-update plumbing get rewritten per call site otherwise. TanStack Query is the library specifically built for the cache-heavy, mutation-heavy, push-invalidated shape we just specced.
- **HTTP client:** axios is heavy and irrelevant in 2026. ofetch is nicer but pulls Nuxt-adjacent deps. Native `fetch` + tiny wrapper is right-sized and forward-compatible with `openapi-fetch`.
- **Tailwind 4 vs. 3:** v4 is current; v3 is legacy. Cost: occasional internet examples written for v3 config (now obsolete). Worth it.
- **pnpm vs. npm/yarn:** smaller `node_modules`, faster installs, friendlier if the repo ever grows a second package.

**Headline calls:**

- **One Composition style.** `<script setup>` everywhere. No Options API. Discipline at code-review time.
- **Server state lives in Query, not Pinia.** Pinia is for genuinely client-side state (UI mode, in-progress form drafts, the toggle for "show advanced settings"). API data goes through TanStack Query — single source of truth, one cache.
- **The SSE event stream is the cache-invalidation channel.** `useEvents()` subscribes once at app mount; `event.kind` switches drive `queryClient.invalidateQueries(...)`. Components consume `useQuery` and stay fresh without knowing about events.
- **Forms, dark mode, i18n, testing stack** are downstream decisions, not part of this lock. Tracked separately.

**Resolves:** the framework portion of `NEXT.md` Tier 1 "Web UI framework + deploy model."

**Open follow-up:** deploy model (UI bundled into the brain image vs. separate `nginx:alpine`/scratch container vs. brain serves the static files itself). Tracked as a narrowed Tier 1 item in `NEXT.md`.

**Affected docs:** `WEB_UI.md` (new), `NEXT.md`.

---

## 2026-05-14 — Brain ↔ UI API: REST + SSE, jobs pattern mirrors host-agent

**Previously:** `NEXT.md` Tier 1 listed "Brain HTTP/RPC API style" — REST+WS vs. gRPC+WS vs. all-WebSocket RPC framing — as the blocking item gating UI work, manifest tooling, and the future malmo-store API.

**Now:** Locked in `BRAIN_UI_PROTOCOL.md`. Mirrors `BRAIN_HOST_PROTOCOL.md` deliberately — one API discipline across the whole stack.

- **Transport:** HTTPS via Caddy → brain. Browser-native fetch / EventSource / WebSocket. No bespoke client library required.
- **Wire:** HTTP/1.1 + JSON. Versioned URL prefix `/api/v1/...`. UI sends `X-Malmo-API-Version`; brain returns **426 Upgrade Required** on mismatch (per existing UI deploy-model lock).
- **Four patterns:**
  - **A — Sync request/response** for ops under ~5s (list apps, get user, update setting).
  - **B — Jobs** for anything that can exceed ~5s (app install/update, mkfs, Tailscale enrollment). Same shape as host-agent jobs: `POST` returns `{job_id, status}`; `GET /api/v1/jobs/:id` polls; `POST /api/v1/jobs/:id/cancel` cancels. `status ∈ {running, completed, failed, cancelled, cancelling, stalled}`. `result` on completion; `error` on failure.
  - **C — SSE for streams.** Two distinct stream types: **per-resource log/job tails** (`/api/v1/jobs/:id/log`, `/api/v1/instances/:id/log`) and a **global event stream** (`/api/v1/events`) for dashboard liveness — app state changes, update-available, drift surfaces, peer-online. Each event carries a typed `kind` and resource ID. Same reconnect resilience as host-agent SSE: monotonic `id`, rolling buffer, `Last-Event-ID` replay.
  - **D — WebSocket reserved** for the future web terminal. No v1 pre-design.
- **Auth:** opaque `malmo_session` cookie (per `AUTH.md`). Same cookie carries the SSE/WS upgrade. No bearer tokens, no JWTs.
- **Errors:** HTTP status + `{code, message, details?}` body. Codes are stable strings; messages are human-readable but not contractual.
- **Codegen: deferred.** Hand-rolled Go structs ↔ TS types in v1. OpenAPI 3 spec + generated TS client as a follow-up build step before the public-API surface ships.
- **Public-API posture from day one.** The API the dashboard uses is the same API a future CLI, third-party app store, or external tool will hit. No "internal-only" carve-outs. Concretely: stable URLs, stable error codes, stable event `kind` values, no hidden auth shortcuts the dashboard uses but external callers can't.

**Why these vs. the alternatives:**

- **tRPC end-to-end (Umbrel's path)** is the most ergonomic option *if both ends speak TypeScript*. The brain is Go (`DECISIONS.md` foundational), so the central tRPC advantage — zero codegen, types flow through TS project references — is unavailable. Adopting tRPC would require either rewriting the brain in TS (rejected; orchestration daemons are Go's niche) or a TS shim daemon in front of the Go brain (pure overhead).
- **gRPC + grpc-web (CasaOS-adjacent)** gives schema enforcement and native streaming, but loses `curl`-debuggability, requires a codegen step from day one, and is awkward in browser devtools. The performance argument doesn't apply at home-server scale.
- **All-WebSocket with RPC framing** collapses transports but loses HTTP's free toolchain — proxies, caches, devtools, `curl`. Streams that are naturally one-way (95% of our streaming surface — logs, events) don't benefit from full-duplex. Reserved for the terminal, not the default.
- **Separate MessageBus service (CasaOS's pattern)** is good for microservices but malmo-brain is one binary (`CONTROL_PLANE.md`). A single SSE endpoint on the brain delivers the same affordance without a second daemon.
- **Reconstructing job progress from polling state** (Umbrel + CasaOS both do this) was rejected — a typed `Job` resource with progress + terminal result is a strict improvement, and we get it free by mirroring host-agent's existing pattern.
- **Codegen from day one** was tempting (compile-time TS types catch drift). Rejected for v1 because hand-rolled types let us iterate on the schema at editing speed; the OpenAPI step lands when the surface is stable enough that drift cost > codegen cost.

**Headline calls:**

- **One API discipline across the stack.** Brain↔UI and brain↔host-agent use the same four patterns. Engineers learn one model. Jobs, errors, SSE reconnect — all identical shapes.
- **Debuggability is a first-class design constraint** (inherited from `BRAIN_HOST_PROTOCOL.md`). Anything callable from the dashboard is callable from `curl` over the LAN with a session cookie. Bug reports include curl commands.
- **Streams are first-class, not afterthoughts.** Every event `kind` is enumerated in the schema. No untyped `{type, data}` blobs.
- **Third-party stores are a v1 design input.** The store-API surface is the dashboard's API; there is no second public API to maintain.

**Resolves:** `NEXT.md` Tier 1 item #1 "Brain HTTP/RPC API style."

**Open follow-ups (parked in `NEXT.md`):**

- Timing of the OpenAPI codegen build step.
- Concrete event `kind` enumeration (post-MVP UI surfaces will pull this in).
- Rate-limit / abuse posture for third-party API consumers.

**Affected docs:** `BRAIN_UI_PROTOCOL.md` (new), `NEXT.md` (Tier 1 #1 resolved, follow-ups added).

---

## 2026-05-14 — Brain ↔ host-agent failure semantics: four categories, four mechanisms

**Previously:** Failure modes were deliberately deferred when the happy-path protocol landed. `NEXT.md` Tier 1 carried "Brain ↔ host-agent failure semantics" as the explicit follow-up.

**Now:** Locked in `BRAIN_HOST_PROTOCOL.md` § Failure semantics and `APP_LIFECYCLE.md` § "same reconciler pattern extends to all host-managed state." Treated as four distinct problems with four distinct mechanisms — not one unified framework.

| # | Problem                                                            | Mechanism                                                                                  |
|---|--------------------------------------------------------------------|--------------------------------------------------------------------------------------------|
| A | Hung ops, dangerous ops, concurrent destructive ops                | Per-job declared attributes (`MaxDuration`, `Dangerous`, `ResourceClass`), enforced uniformly by host-agent |
| B | Multi-step crash recovery + drift from manual host changes         | Reconciler pattern — desired in brain SQLite, actual via host-agent summary, 60s heartbeat |
| C | SSE stream resilience across brain restarts                        | `Last-Event-ID` + ~256 KB per-job rolling buffer; standard SSE replay                      |
| D | host-agent self-update, FD limits, cross-class destructive locks   | Orchestration rules at the lifecycle level — not protocol surface                          |

**Headline calls:**

- **Stalled and failed are distinct job statuses.** "We're not sure" vs. "we know it broke" — different UI tones.
- **Cancellation: SIGTERM → 10s grace → SIGKILL. Final result wins** — if the op completes before kill, the job ends `completed` regardless of pending cancellation.
- **Cross-class dangerous lock:** any `Dangerous: true` job blocks all other jobs while it runs and waits for everything to drain before starting. Catches "disk format and apt upgrade are technically different resource classes but you really don't want both at once."
- **Drift policy is asymmetric.** Brain auto-reconciles when *it* made the last change (handles crash-mid-step). Brain surfaces — doesn't auto-fix — when *something else* changed state. Respects users who deliberately SSHed in to fix something; avoids fight-loops.
- **Dangerous ops are excluded from auto-reconcile.** An interrupted `mkfs` is not safely retryable. UI surfaces; user decides.
- **Heartbeat is 60 seconds.** Brain polls host-agent's `GET /v1/state/summary`. One tiny request per minute. Quieter than 30s, more responsive than 120s.
- **host-agent self-update drains all jobs first**, with a 5-minute hard cap. If a job is still running, the OS update fails with "retry later" rather than risking corruption.
- **CI tests guard the discipline.** Every `JobKind` must declare its attributes (type-enforced at registration). SSE reconnect, reconciler convergence, and malmo-group membership all have round-trip tests.

**Why these vs. the alternatives:**

- **Auto-fix drift always** (the box does what its settings say) was simpler but lost — it would fight users who deliberately changed something via SSH and create reset-loop bugs. Surface-don't-auto-fix is the right respect-the-user posture.
- **Unified failure-handling framework** (one mechanism for all the problems) was tempting but conflates concerns. The four problems have genuinely different shapes; one mechanism per shape is simpler than a generic one.
- **No heartbeat** (only reconcile on startup and after ops) would miss drift entirely until the next user-initiated action. 60s heartbeat catches it within a minute for no real cost.
- **Auto-resume dangerous ops** (try to be helpful after a crash) is exactly the kind of thing that takes a small problem and turns it into a corrupted disk. Surface and let the user decide.
- **Treat SSE reconnect as application-layer** (custom protocol, ack-based) was rejected because spec-defined `Last-Event-ID` + a ring buffer does everything we need with zero new protocol surface.

**Resolves:** `NEXT.md` Tier 1 item "Brain ↔ host-agent failure semantics."

**Affected docs:** `BRAIN_HOST_PROTOCOL.md` (§ Failure semantics replaces the deferred-stub), `APP_LIFECYCLE.md` (reconciler pattern extended from apps to all host-managed state), `NEXT.md`.

---

## 2026-05-14 — Brain ↔ host-agent: HTTP/JSON over UNIX socket, two API patterns, lockstep versioning

**Previously:** `NEXT.md` Tier 1 listed the brain↔host-agent protocol as open. `CONTROL_PLANE.md` mentioned the boundary informally.

**Now:** Full happy-path protocol locked in `BRAIN_HOST_PROTOCOL.md`. Headline calls:

- **Transport:** UNIX socket at `/var/run/malmo/agent.sock`, `root:malmo` `0660`. Brain's container UID is in the `malmo` group. Kernel-enforced authn, no app-layer token.
- **Wire:** HTTP/1.1 + JSON, versioned URLs (`/v1/...`). Debuggable with `curl --unix-socket` from any shell on the host.
- **Two API patterns, clear rule:** plain HTTP request/response for ops under ~5 seconds; explicit `Job` objects with poll + cancel for anything longer or anything needing progress reporting. Bias toward "make it a job" when uncertain.
- **SSE for one-way streams** (app container logs, job log tail, journalctl). Browser-native, end-to-end through the brain.
- **WebSocket reserved** as the future-shape for bidirectional needs (web terminal). Additive via HTTP upgrade; no v1 pre-design.
- **Versioning: lockstep with OS release.** Brain N talks to host-agent N. No protocol negotiation. Resolves the `UPDATES.md` "brain↔host-agent protocol versioning" open item.

**Why these vs. the alternatives:**

- **gRPC over UNIX socket** was the main contender. Schema enforcement and native streaming are real wins, but the code-gen step and lost `curl`-debuggability outweigh them for two binaries we ship together. We can switch later if hot paths ever warrant it; nothing about HTTP/JSON forecloses gRPC.
- **Loopback TCP** would be slightly easier to call from outside the brain's container but exposes port-allocation problems and weakens authn (anyone who can reach the port can talk to host-agent, vs. anyone in the kernel-checked group).
- **Direct shared-state via SQLite** (brain writes "desired" rows, host-agent polls) gives free persistence but adds latency and is clunky for streaming. Rejected.
- **Everything is a job** (uniform model, one pattern) was considered. Rejected because read-only / fast routes don't need the "is this done? where's the result?" overhead. The dividing line is explicit per route.
- **Protocol-version negotiation** is unnecessary given lockstep release; would only matter if we ever upgraded brain independently of host-agent, which the OS-update model doesn't do.

**Notes:**

- Failure semantics (timeouts, drift detection, reconciler discipline, SSE reconnect, dangerous-op resume) are **deliberately deferred** to a separate design pass. Tracked as a Tier-1 item in `NEXT.md`. The protocol is not implementation-ready without those.
- The "debuggability is a first-class design constraint" line in `BRAIN_HOST_PROTOCOL.md` is load-bearing: future changes to this protocol that would make it harder to inspect from `curl` need an explicit justification.

**Affected docs:** `BRAIN_HOST_PROTOCOL.md` (new), `CONTROL_PLANE.md` (host-agent reference points here), `AUTH.md` (malmo-group test invariant), `UPDATES.md` (protocol-versioning question resolved), `NEXT.md`.

---

## 2026-05-14 — Tier-2 apps: native Debian + systemd, UI lives in the dashboard

**Previously:** Implicit assumption that all malmo-managed software runs as Docker containers. `SERVICE_PROVISIONING.md` left Tier-2 deployment open: *"a privileged container, a host service managed by host-agent, or a combination."* The malmo session ↔ Tier-2 admin UI auth story was complicated by the prospect of forward-auth across per-app subdomains.

**Now:** **Tier-2 apps install as Debian packages, run under systemd. No upstream admin UI is exposed at its own subdomain.** The malmo dashboard surfaces a curated UI for each Tier-2 service at `/settings/<service>/*`. The brain edits config files and toggles systemd units via host-agent. Tier-1 (managed data services) stays containerized; Tier-3 (regular apps) stays containerized.

**Why:**
- Native is what upstream docs recommend for these services. Tailscale's Docker path needs `--privileged` and `/dev/net/tun` gymnastics; native Samba sidesteps uid-mapping container ugliness.
- Auth collapses: Tier-2 routes are same-origin as the dashboard, so the `malmo_session` cookie just works. No forward-auth, no Authelia-style redirect dance, no embedded iframes.
- We control the UX completely. Curated set means we choose which knobs to expose; the user never has to learn a third-party admin UI per Tier-2 service.

**Knock-ons:**
- Limits Tier-2 to "what's in Debian (or what we package as .deb)." Acceptable for a small curated set.
- Host-agent gains real responsibility — writing config files, calling `systemctl`. Correct: host-agent is the thing that should hold host-level privilege.
- Tier-2 updates ride apt (aligned with `UPDATES.md` v1).
- The "Tier-2 update model — rides OS channel vs. independent" item in `NEXT.md` is now answered: rides OS channel via apt.

**Affected docs:** `AUTH.md` (new), `SERVICE_PROVISIONING.md`, `CONTROL_PLANE.md` (host-agent scope), `NEXT.md`.

---

## 2026-05-14 — Auth & session model: password-only, opaque cookies, recovery code

**Previously:** Auth was an undocumented "Tier 1" topic in `NEXT.md`. We had only the no-SSO-into-apps decision from `SPEC.md`.

**Now:** The full auth model is locked in `AUTH.md`. Headline calls:
- **Password is the only identity primitive in v1.** No passkeys (origin-bound, would break across the toggle), no TOTP, no email-based recovery (no email on file).
- **Sessions are server-side opaque cookies** in a SQLite `sessions` table, 30-day rolling, 90-day hard cap. Not JWTs.
- **Cookie is host-scoped** (no `Domain` attribute). Critical for preserving subdomain isolation between Tier-3 apps and the dashboard.
- **Login UX is a user list** (macOS / Plex style), not a username field. Household device, small known user set.
- **Admin recovery code: opt-in toggle, default on.** Shown once at admin creation, hashed in SQLite, single-use. Phone-photo backup encouraged.
- **No physical-access reset.** Box gets stolen → TPM auto-unlocks LUKS → physical-access reset would hand the thief admin. Rejected.
- **Dashboard password and SSH password are separate.** SSH is off by default; users opt in from Settings.

**Why these vs. the alternatives:**
- **Passkeys** were the main "interesting" idea ruled out. The toggle flips origins (`.local` ↔ `.malmo.network`); passkeys are origin-bound by design, which means re-enrollment per scheme. Bad UX. Password works on either origin.
- **JWTs** offer non-revocable tokens — useless for a single backend with logout / "sign out everywhere" requirements. Opaque cookies + DB lookup is right at this scale.
- **Wildcard `.malmo.local` cookie** would have been the "easy" way to share sessions with Tier-2 subdomains; would have defeated Tier-3 subdomain isolation that `SPEC.md` paid for. Hard no.
- **Same password for dashboard + SSH** would have forced the brain to round-trip every login through host-agent → PAM, doubling failure modes for non-technical users who never use SSH.
- **Cross-origin session handoff token** (drafted earlier) is dropped under the global-toggle model — toggle flips are the only routine origin transitions, re-auth there is accepted.

**Affected docs:** `AUTH.md` (new), `FIRST_RUN.md` (Step 2 + recovery code sub-step), `MALMO_NETWORK.md` (toggle-flip sharp edge points to AUTH), `CONTROL_PLANE.md` (brain session store, host-agent credential mutations), `NEXT.md` (Tier-1 item resolved).

---

## 2026-05-14 — URL access model: collapse to one scheme at a time

**Previously:** "Two URLs always exist per app — `.local` and `.malmo.network`. `.local` is canonical and user-facing; `.malmo.network` is HTTPS plumbing that the brain transparently routes to for apps with `requires_https: true`. A power-user toggle in Settings could flip the dashboard to surface `.malmo.network` URLs everywhere."

**Now:** **One URL scheme at a time, governed by a single global toggle.** Default off → all app URLs are `.local`. Toggle on (which implies enrollment) → all app URLs become `.malmo.network`. There is no per-app routing override and no transparent mixed-scheme tile behavior. Apps that need HTTPS-gated browser APIs (cameras, mic, PWAs, secure cookies) declare `needs_secure_context: true` in the manifest — this triggers a **warning at install time** on a toggle-off box, not a routing override or install block. The user can install anyway and choose whether to flip the toggle.

**Why:**
- The previous model had the brain silently send the user to a *different origin* for some apps. That meant a `requires_https` app would have a different session, different cookies, and different sharing semantics from neighboring apps on the same dashboard, with no UI cue. Inconsistent mental model.
- The "transparent routing" idea also required the brain to know the user's intent (LAN vs. remote, casual vs. secure) — which it doesn't have.
- A global toggle frames the choice honestly: *"Use secure HTTPS URLs for my apps"* is one decision the user makes once, applies to everything, and matches how they actually think about the box ("is my whole house on HTTPS or not?").
- Cross-origin session friction (cookies don't carry across `.local` ↔ `.malmo.network`) only happens once per toggle-flip rather than every time the user opens a `needs_secure_context` app. Re-auth on a deliberate mode switch is acceptable; re-auth on a tile click is not.
- Hard-blocking install of `needs_secure_context` apps on un-enrolled boxes was paternalistic. A warning + user agency is the right shape — some apps degrade gracefully on HTTP, the user can judge.

**Knock-ons:**
- The "brain-mediated session handoff token" idea (drafted but not written into the docs) is **dropped**. Under the one-scheme-at-a-time model, the only origin transition is the deliberate toggle flip, where re-auth is acceptable.
- Naming: `requires_https` → `needs_secure_context`. The new name describes the *cause* (browser secure-context requirement), not the *mechanism* (HTTPS). Better hint for app authors deciding whether to set the flag.
- The dashboard always shows one URL per app. No "two URLs to share with my partner" problem.

**Affected docs:** `MALMO_NETWORK.md`, `APP_MANIFEST.md`, `FIRST_RUN.md`, `APP_LIFECYCLE.md` (Caddy registration).

---

## 2026-05-14 — Auth: no SSO into apps, no session handoff

**Previously:** Open question whether malmo would mediate sessions across `.local` and `.malmo.network` via a one-time handoff token, so users wouldn't re-authenticate when crossing origins.

**Now:** **No SSO, no handoff.** Each app keeps its own auth (already locked in `SPEC.md:64`). The malmo dashboard has its own session. Cross-origin re-auth happens only when the user flips the global URL-scheme toggle, which is a rare deliberate action.

**Why:**
- The one-scheme-at-a-time model eliminates the day-to-day cross-origin case the handoff was solving.
- SSO into apps would require every app to implement an OIDC-style flow against malmo — a real ask we'd be making of app authors, for v1, with no concrete user demand.
- Keeping app auth fully independent preserves the "apps work as upstream authors designed them" principle that drove subdomain routing (`SPEC.md` § "Why subdomain").

**Affected docs:** none yet — this is a confirmation of the existing position, captured here because we considered changing it.

---

## 2026-05-13 — Hooks: deferred from MVP

**Previously:** The manifest included a `hooks:` block for `pre_install`, `post_install`, `pre_update`, `post_update`, etc., running as shell scripts inside the app container.

**Now:** **Hooks are out of v1.** Every concrete use case we could name was tied to managed services or backups — both deferred. When hooks return, they'll be **one-shot container images**, not in-container scripts.

**Why:**
- In-container scripts force app authors to ship a shell + the malmo-specific glue inside their image. That's a real ask for commercial / closed-source apps that don't want shell-execution paths in their distribution images (IP concerns).
- One-shot container images let the app vendor publish a separate migrator image (`photoprism/migrator:2.4.1`) that the brain runs as a transient container with the app's volumes mounted. Clean integration boundary, no in-image patching.
- Deferring entirely (rather than shipping a half-formed in-container version) avoids locking ourselves into the wrong shape.

**Affected docs:** `APP_MANIFEST.md` § F, `APP_LIFECYCLE.md` (lifecycle hooks).

---

## 2026-05-13 — Tier-3 apps cannot use `cap_add`

**Previously:** Apps could declare needed Linux capabilities in the manifest; the brain would enforce.

**Now:** **No `cap_add` for Tier-3 (store) apps, period.** Apps that genuinely need Linux capabilities (VPN clients like Tailscale, FUSE mounts, raw sockets) belong in **Tier 2** — OS integrations curated by malmo with a separate install path.

**Why:**
- Tier-3 is the "anyone can submit, third-party stores allowed" path. Granting Linux capabilities to apps from that channel expands the attack surface beyond what we want to underwrite by default.
- The apps that legitimately need caps (VPN, SMB, DLNA, mount tooling) are a small, identifiable set — they fit Tier 2's "curated by malmo" model naturally.
- Splitting cleanly at the tier boundary is simpler than per-capability allowlists and matches how Umbrel/Synology handle the same problem (system services vs. user apps).

**Affected docs:** `APP_MANIFEST.md` § E, `SERVICE_PROVISIONING.md`, `APP_ISOLATION.md`.

---

## 2026-05-13 — Compose + manifest stay two files

**Previously:** Considered collapsing `docker-compose.yml` and `manifest.yml` into a single file to reduce author cognitive load.

**Now:** **Two files. The compose file is held verbatim by the brain.** The manifest holds malmo-specific metadata only.

**Why:**
- Authors already know `docker-compose.yml`. Keeping it unchanged means "test locally with `docker compose up`, then publish" works without translation steps.
- Verbatim compose means the brain never has to round-trip user-authored YAML through a parser/emitter, which would mangle comments, formatting, and edge-case syntax.
- The override-file pattern (`docker compose -f compose.yml -f override.yml`) gives the brain a clean place to inject env vars, drop capabilities, and bind networks without touching the author's file.
- Two files also gives us a clean schema-versioning story for the manifest without affecting the compose contract.

**Affected docs:** `APP_MANIFEST.md`, `APP_LIFECYCLE.md`.

---

## How to add an entry

When you write one, ask:
1. Did this *flip* a previous position, or is it net-new? (Flips are the highest-value entries.)
2. Will future-me wonder "why did we do this instead of the obvious thing?" If yes, write the entry — capture the obvious-thing-we-rejected explicitly.
3. Is the reasoning derivable from reading the affected doc today? If yes, you may not need an entry — the doc itself is sufficient.

If in doubt, write the entry. The cost of an extra paragraph here is much lower than the cost of relitigating six months later.
