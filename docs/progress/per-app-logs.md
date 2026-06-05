# Per-app Logs tab — live container log tail (SSE)

- **Status:** done
- **Date:** 2026-06-04
- **Specs touched:** `BRAIN_HOST_PROTOCOL.md` (added the `journal_follow` op to Pattern C; no decision flipped). Realizes `LOGGING.md` # Per-app logs + # Mechanisms and `BRAIN_UI_PROTOCOL.md` Pattern C stream 1 — both already specified; the implementation matches them, so neither needed editing.

Closes issue #6. A per-app Logs panel that live-tails one container's stdout/stderr end-to-end: host-agent `journalctl CONTAINER_NAME=<c> -f` → SSE → brain transparent forwarder with a per-instance ring/replay hub → brain `GET /api/v1/apps/{id}/log` → dashboard. The brain is containerized and can't read the host journal directly, so the tail flows brain → host-agent → journald exactly as `LOGGING.md` # Mechanisms requires. Live-tail only; the sibling `journal_query` (historical search) and `journal_export_range` (bundle dump) stay deferred.

## What was done

### Wire type + host-agent op (`journal_follow`)

- `internal/protocol/host.go` — `JournalLine` (`{ts, stream, line, lost}`), the `data:` payload of each Pattern-C frame. `lost=true` is the gap marker (no text).
- `internal/hostagent/agent.go` — `LogSource` interface (consumer-side, lives with its user), an `Agent.Logs` field, and `GET /v1/journal/follow?container=<name>`: `501` if no source is wired, `400` if `container` is missing, else `200` + `text/event-stream`. A reconnect carrying `Last-Event-ID` can't be replayed by a fresh per-connection follower, so the handler leads with one `{"lost":true}` frame then streams live. Frames carry host-agent's own per-connection monotonic `id`.
- `internal/hostagent/fake.go` — `FakeLogSource`: a synthetic ticker (one line per interval) so the all-native inner loop has a live stream with no real journald.
- `internal/hostagent/journalsource/` — the real `LogSource` for `host-agent-real`. Shells out to `journalctl CONTAINER_NAME=<c> -f -o json -n 100`, binds the process to the follow context (a brain disconnect kills it), parses each entry (`PRIORITY` 3 → stderr, else stdout; `__REALTIME_TIMESTAMP` µs → RFC3339; `MESSAGE` handled as both the JSON-string and non-UTF-8 byte-array forms journald emits), and skips unparseable lines rather than aborting the stream. Relies on Docker's daemon-wide `journald` log driver (`LOGGING.md` # Operational logs).
- `cmd/host-agent/main.go` wires `NewFakeLogSource(time.Second)`; `cmd/host-agent-real/main.go` wires `journalsource.New()`.

### hostclient streaming method

- `internal/hostclient/hostclient.go` — `JournalFollow(ctx, container) (<-chan protocol.JournalLine, error)`. Returns an error on a non-200 (so a `501`/`400` surfaces as an error, not a silent empty stream), else scans the SSE `data:` lines into the channel until ctx is cancelled or the stream ends. **Gotcha fixed:** the shared `http.Client` has a 30 s `Timeout` that would guillotine a long-lived follow, so `New()` now builds one transport shared by two clients — the existing 30 s `http` client and a new timeout-less `stream` client used only for follows (bounded by ctx instead).

### Brain log hub (authoritative ring + replay)

- `internal/applog/` — `Registry` of one `hub` per instance id. `Subscribe(instanceID, container, lastID) → (replay, live, release)`. The first subscriber opens one upstream `JournalFollow`; subsequent subscribers of the same app **share that one follow** (no second `journalctl`). Each frame is re-stamped with the brain's own monotonic id and pushed into a ~256 KB rolling ring. Replay logic: `lastID==0` → whole backlog, no `{lost}`; a `lastID` still buffered → tail replay; an evicted `lastID` → one `{lost}` then the full ring. When the last subscriber leaves, a short linger keeps the follow warm; if it elapses idle the hub tears down and the next reader reopens cold (a cold hub + `Last-Event-ID` always emits `{lost}`, so id-reset is safe). A subscriber that can't keep up is closed (forcing a reconnect + ring replay) rather than silently dropped — an unmarked gap would be a correctness bug. Mirrors `internal/systemlive`'s ref-counted-poller shape.
- `internal/lifecycle/lifecycle.go` — `MainContainerName(id)` resolves `molma-<id>-<main_service>` from the on-disk instance manifest, the container the brain hands to the follow.

### Brain endpoint + visibility

- `internal/api/applogs.go` — `GET /api/v1/apps/{id}/log`, registered raw (like `events`/`systemLive`) because the `id:`/`data:`/`Last-Event-ID` reconnect format is the point and huma streaming would obscure it. Order: authenticate (401) → `store.Get` (404) → `logVisibility` (403/404) → `MainContainerName` (500) → `beginStream` (429, the per-session SSE cap) → write 200 → replay backlog → fan out live.
- `logVisibility` is **stricter than `canSee`** and pins `LOGGING.md` # Per-app logs's visibility rule: admin → 200; owner of a personal instance → 200; non-admin + household → **403** (a member may *see* a household app in the launcher, but its logs can leak another member's activity, so logs are admins-only); non-admin + someone else's personal → **404** (leak guard, mirroring `getApp`).
- Wired through `cmd/brain/main.go` (`applog.NewRegistry(pollCtx, host)`) and `NewServer`.

### Frontend

- `web-ui/src/useLogStream.ts` — opens an `EventSource` over `/api/v1/apps/{id}/log` (`withCredentials` for the session cookie) on mount, closes on unmount (the "only-while-watching" contract the brain ref-counts). EventSource auto-reconnects and resends `Last-Event-ID`; the composable just appends, capped at 1000 in-memory lines (the brain's ring is the replay source of truth).
- `web-ui/src/components/AppLogs.vue` — monospace panel: stderr in red, the `{lost}` marker rendered as an amber "some earlier lines were dropped" divider, auto-scroll pinned to the bottom unless the user scrolls up (with a "jump to latest" affordance), and Waiting/Connecting empty states.
- `web-ui/src/views/SettingsView.vue` — a per-row **Logs** toggle in the installed-apps list (one panel open at a time), shown only when the viewer may see the logs (`canViewLogs`: admin, or a member's own personal app). The list is already visibility-scoped server-side, so the toggle is a pre-gate, not the security boundary.

### Tests

Across every layer, all race-clean: host-agent handler (nil→501, missing-container→400, monotonic ids, `Last-Event-ID`→`{lost}`-then-live, source-error→500); `journalsource` parse units (priority→stream, µs→RFC3339, byte-array message, garbage skipped); hostclient (parses frames then closes, non-200→error, ctx-cancel→channel close); `applog` hub (late joiner gets backlog, two readers share one upstream, tail-replay vs `{lost}`-on-eviction, linger teardown reopens upstream); api (`logVisibility` matrix + the four HTTP denial paths — 401/403/404/404).

## How it maps to the specs

- `LOGGING.md` # Per-app logs — Logs tab, live SSE tail, plaintext monospace no-parsing, and the exact visibility rule (Tier-3 owner + admins; Tier-1 shared → admins only).
- `LOGGING.md` # Mechanisms — realizes the called-for "new section in `BRAIN_HOST_PROTOCOL.md` for journal operations" by adding `journal_follow` (read-only; `journal_query`/`journal_export_range` deferred). The brain → host-agent → journald path and the Docker `journald`-log-driver dependency are exactly as specified.
- `BRAIN_UI_PROTOCOL.md` Pattern C stream 1 — `GET /api/v1/apps/:id/log` as a transparent forwarder; the brain re-emits ids from its own monotonic counter so `Last-Event-ID` replay survives brain restarts. Counts against the ≤16-per-session stream cap via `beginStream`.
- `BRAIN_HOST_PROTOCOL.md` Pattern C — frame shape (`id:` + `data:`, no `event:`), monotonic ids, `{"lost":true}` on gap.
- CLAUDE.md # Go code discipline — consumer-side `LogSource` (in `hostagent`) and `follower` (in `applog`) interfaces; new `applog` package for a self-contained concern (goroutine lifecycle + ring + tests), mirroring `systemlive`; `log/slog` with the standard `err`/`container` fields; layer boundaries respected (`applog` imports only `protocol`; `api`/`cmd/brain` import `applog`; `MainContainerName` added on `lifecycle`, the manifest owner).

## Known gaps & deviations

- **Two-tier replay split.** The generic Pattern-C contract puts the ~256 KB ring + `Last-Event-ID` replay in host-agent. For `journal_follow` it lives in the **brain** hub instead; host-agent is a thin per-connection streamer (one `{lost}` on a reconnect with `Last-Event-ID`, then live — no cross-connection buffer). The brain owns the ring shared across all dashboard subscribers of one app and is the side the browser reconnects against. A host-side shared-follower buffer (so two brain consumers share one `journalctl`) is deferred until a second consumer exists. Documented in `BRAIN_HOST_PROTOCOL.md` # Pattern C.
- **`CONTAINER_NAME` replica-suffix mismatch.** Docker's `journald` driver tags lines with the *running container* name, which compose suffixes with a replica number — `molma-<id>-<service>-1`. `MainContainerName` returns the un-suffixed stem `molma-<id>-<service>`, so an exact `CONTAINER_NAME=` match misses the line on a real box until the brain passes the replica-qualified name. The brain-side resolution (or a `CONTAINER_NAME` prefix/glob match) is a follow-up; the fake host-agent doesn't reproduce this, so the inner loop is unaffected. Noted in the `journalsource` package doc and surfaced in the PR body.
- **Logs attach to the Settings installed-apps list, not an app detail card.** `LOGGING.md` # Per-app logs says the Logs tab "lives on each app's card." No per-app detail view with tabs exists yet (the installed-apps list in Settings is where per-instance management lives, with a `// when the app detail page lands, this moves there` note already in the file). The Logs toggle was added there alongside Uninstall; it moves to the detail card when that lands — no behavior change, just a host surface.
- **api 200/streaming happy-path is covered by hub + handler unit tests, not one end-to-end HTTP integration test.** The `applog` hub tests prove replay/fan-out/linger, the api tests prove auth + the four denial statuses, and the hostclient/host-agent tests prove the wire. A single test that stands up the full manifest-on-disk + host scaffold and reads a live 200 stream over HTTP was judged not worth the fixture cost given that coverage; recorded here as the deliberate seam.
- **Member-visible household logs (`logs.member_visible: true`) not implemented.** The manifest opt-in that would relax the admins-only rule for household apps is deferred per `LOGGING.md` # Per-app logs.
- **"No logs received" empty-state hint** (`LOGGING.md` # Apps are expected to log to stdout — the sliding-window "this app may be logging to a file" card) is not built; the panel shows a neutral Waiting state instead. Out of issue scope.

## What's next

- **Replica-qualified container name** so real-box logs match (`molma-<id>-<service>-1`), or a `CONTAINER_NAME` prefix match in `journalsource`. This is the one gap that blocks the feature on a real host (the fake works today).
- **`journal_query` + `journal_export_range`** for the System logs view and the diagnostic bundle (`LOGGING.md` # System logs, # Diagnostic bundle).
- **Logs tab on the app detail card** when that view lands; retire the Settings-list toggle.
- **Real journald exercise under the QEMU medium lane** — the `journalsource` parser is unit-tested but the live `journalctl` follow + the Docker log-driver config aren't yet run against a real journal.
