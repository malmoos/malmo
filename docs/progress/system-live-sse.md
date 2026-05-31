# Live system-resources SSE + top-bar dropdown

- **Status:** done
- **Date:** 2026-06-01
- **Specs touched:** none changed (realizes `LOCAL_ANALYTICS.md` # Real-time system resources, `BRAIN_UI_PROTOCOL.md` Pattern C stream 3, `BRAIN_HOST_PROTOCOL.md` `GET /v1/system/resources`, `DASHBOARD.md` # the top bar — all already specified; no divergence)

Implements the all-users live system-resources view (issue #5): a top-bar chevron that opens a compact CPU / RAM / network / disk-IO panel backed by an on-demand SSE stream. The brain never reads `/proc` (it's containerized) — host-agent serves raw cumulative counters and the brain diffs them into rates and fans them out from one ref-counted poller. Closes issue #5. The admin Settings → System deep-view stays deferred (`NEXT.md`).

## What was done

### Two-leg data flow

**host-agent → brain (Pattern A).** `GET /v1/system/resources` returns raw cumulative counters (`/proc/stat` jiffies, `/proc/meminfo` levels, `/proc/loadavg`, per-interface `/proc/net/dev`, per-device `/sys/block/<dev>/stat`) plus a monotonic `ts_ns`. host-agent is stateless — it reads on request and computes no rates, and applies the interface/device allowlist (physical LAN NICs + mesh; whole-disk devices) so the brain never sees container-bridge noise.

- `internal/protocol/host.go` — `SystemResources` + `CPUCounters`/`MemCounters`/`NetCounters`/`DiskCounters`, JSON tags matching the spec payload at `BRAIN_HOST_PROTOCOL.md:84-92` exactly. Counters are `int64` (`/proc` byte/jiffy counters exceed 2^31).
- `internal/hostagent/agent.go` — registered the route + `systemResources` handler. The real binary reads `/proc`/`/sys`; the fake synthesizes counters that climb off `a.startedAt` (and a fresh `ts_ns` per call), so two successive 1 Hz polls always diff to a non-zero, plausible rate in the inner dev loop. Stateless — no new `Agent` field, mirroring the spec's requirement and the sibling `systemStatus`.
- `internal/hostclient/hostclient.go` — `SystemResources` method (mirrors `SystemStatus`, uses the shared `do` helper).

**brain → UI (Pattern C, SSE).** `GET /api/v1/system/live` streams `event: sample` frames of derived rates. The new `internal/systemlive` package owns the mechanics:

- **`Hub`** — a ref-counted fan-out. The first SSE subscriber starts a 1 Hz poll goroutine; the last to leave cancels it (zero idle cost on both brain and host-agent). One upstream poll fans out to every subscriber — never one host-agent poll per browser tab. Backed by a consumer-side `sampler` interface (satisfied by `*hostclient.Client`; tests pass a fake). Because the upstream read is the slow part, `poll()` re-checks its context after the read returns: a poll whose generation was cancelled mid-read (the close-then-reopen-the-dropdown gesture) is dropped before it can repopulate the baseline or leak a frame into the next generation's cold start — preserving the null-first-frame invariant across a poller restart.
- **`derive()`** — turns raw cumulative counters into rates. `RestartCount`-style counters are cumulative, so it thresholds the delta against the previous sample with the **`ts_ns` delta as the rate denominator** (not an assumed 1 s tick). CPU% is `busy/total` from jiffy deltas (normalized across cores, 0–100). Net/disk entries are joined to the previous sample by interface/device name.
- `internal/api/api.go` — `systemLive` raw SSE handler (mirrors the existing `events` handler), registered on the mux right after `/api/v1/events`; auth is enforced by the wrapping middleware (the `FromContext` check is belt-and-suspenders). `Server` gained a `live *systemlive.Hub` field + `NewServer` param.
- `cmd/brain/main.go` — constructs one hub (`systemlive.New(pollCtx, host, time.Second)`; `pollCtx` bounds any poll to process lifetime) and passes it to `NewServer`.

### Top-bar dropdown — `web-ui/src/LiveResources.vue` (new) + `TopBar.vue`

The fourth locked top-bar element (`DASHBOARD.md` # the top bar), a chevron next to the avatar menu, mirroring `NotificationBell.vue` (open/close, click-outside, scoped CSS). `watch(open)` opens an `EventSource('/api/v1/system/live', {withCredentials})` on expand and tears it down on collapse — the UI half of the "only while watching" contract. It listens for the named `sample` event, renders CPU%/RAM bars + per-interface and per-device throughput + load + uptime, and does the human formatting (GiB, KB/s) from the SI wire units. The cold-start frame's null rate fields render as "—" until the second sample.

### Tests

- `internal/systemlive` (12, race-clean) — `derive()` rate math (cold-start nulls, delta rates, `ts_ns`-delta denominator, counter-reset → null, new-interface → null, non-positive Δt → null, CPU reset → null) and the hub (cold-first-frame-then-rates, ref-count start/stop + baseline reset, fan-out to all subscribers, poll-error skips broadcast, cancelled-generation poll is a no-op).
- `internal/hostagent/agent_test.go` — `systemResources` returns an allowlisted sample (one NIC, one whole disk) with a positive monotonic `ts_ns`.
- `internal/hostclient/hostclient_test.go` — `SystemResources` decodes the wire shape.
- `internal/api/systemlive_test.go` (new) — the Done-when over real HTTP: unauthenticated `GET /api/v1/system/live` → 401; an authenticated client receives `event: sample` frames whose JSON carries the live levels. Wires a real hub + canned sampler into the shared `newHarness`.

## How it maps to the specs

- `LOCAL_ANALYTICS.md` # Real-time system resources: realizes the two-leg flow, the ref-counted/zero-idle-cost poller, the host-side allowlist, and the all-users (no role gate) visibility.
- `BRAIN_UI_PROTOCOL.md` Pattern C stream 3: `event: sample`, SI wire units (`*_bps` bytes/s, `*_bytes` bytes, floats), no `id:`/no replay; the first post-connect frame's rate fields are null.
- `BRAIN_HOST_PROTOCOL.md` Pattern A: `GET /v1/system/resources` raw-counter shape, byte-for-byte with the spec payload.
- `DASHBOARD.md` # the top bar: the chevron is the fourth locked element, next to the avatar menu, quiet by default.
- CLAUDE.md # Go code discipline: consumer-side `sampler` interface in `systemlive`; new package for a self-contained concern (own goroutine lifecycle + diff logic + tests); `log/slog` with the standard `err` field on the poll-failure skip; layer boundaries respected (`systemlive` imports only `protocol`; `api`/`cmd/brain` import `systemlive`).

## Known gaps & deviations

- **≤16-concurrent-SSE-per-session cap is not implemented** — and does not exist anywhere yet, not even for the pre-existing `/api/v1/events` stream (confirmed by an exhaustive search: no counter, no 429 path, no per-session tracking). `BRAIN_UI_PROTOCOL.md:179` says `system/live` "counts against" the cap, but the cap itself is unbuilt. Building it is cross-cutting (it must span both SSE handlers and key off the session token) and out of this issue's scope and Done-when. Filed as a follow-up issue; surfaced in the PR body. This PR adds a stream that *will* participate in the cap the moment it lands, without itself introducing the cap.
- **Null-first-frame interpretation.** The spec says "the first event after any connect reports rate fields as null (no prior sample to diff against)." The hub implements this as the cold-start property: `prev` is nil on the first poll after the poller starts, and is reset to nil when the last subscriber leaves — so every time a viewer opens the panel (which, by close-on-collapse, stops the poller) the first frame is null. A *second concurrent* viewer joining an already-warm poller receives real rates immediately rather than a synthetic null frame. This follows the spec's stated rationale ("no prior sample to diff against" — a warm hub *has* a prior sample) and its "one upstream poller fans out the same derived rates to all subscribers" mechanism; it is strictly better UX than forcing a stale null on the late joiner. Per-connect null tracking was considered and rejected as contradicting the single-poller fan-out. Surfaced in the PR body for the maintainer.
- **Multi-interface/device rendering is untested against a real host.** The fake host-agent reports one NIC and one disk; the real `/proc`-reading implementation (and its allowlist) is the production binary's job and is not exercised in the inner loop. The brain renders whatever rows arrive — it does not re-filter.
- **Units.** Memory is shown in GiB (binary); network/disk rates in KB/s / MB/s using a 1024 base with conventional labels. The spec says "the UI does the human formatting (KB/s, GiB)" without pinning binary-vs-decimal; this is a UI choice, documented here. The storage pill placeholder uses decimal TB — a minor inconsistency to reconcile when the capacity endpoint lands.
- **No graceful HTTP shutdown.** Pre-existing: `cmd/brain` calls `ListenAndServe()` bare, so SSE connections drop on process exit. Out of scope; unchanged.
- **`version.json` API-minor declaration is still unbuilt** (`WEB_UI.md`): the UI declares no required API minor and the brain has no `426` path. Adding `/api/v1/system/live` is an additive minor, so this is pre-existing debt, not introduced here — noted so the gap isn't silently assumed handled.

## What's next

- **SSE per-session stream cap** (filed follow-up): a session-keyed counter spanning `/api/v1/events` and `/api/v1/system/live`, returning 429 past 16, per `BRAIN_UI_PROTOCOL.md` # Stream cap.
- **Settings → System admin deep-view** (`NEXT.md`): full 60 s graphs over the same stream, all interfaces and drives broken out.
- **Real host-agent `/proc` reader** with the allowlist, replacing the synthetic fake counters, exercised under the QEMU medium lane.
