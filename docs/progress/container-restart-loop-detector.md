# container-restart-loop detector (locus D)

- **Status:** done
- **Date:** 2026-06-01
- **Specs touched:** docs/specs/HEALTH.md (# Detector catalog — locus-D row marked *(built)*)

Implements the `container-restart-loop` detector from `HEALTH.md` # Detector catalog (locus D). A crash-looping app — one whose container keeps exiting and being restarted by `restart: unless-stopped` — now raises a per-app health issue keyed to its `instance_id`, and clears once the app stabilizes or is uninstalled. Closes issue #35. Brain-only: no host-agent change and no socket-proxy allowlist change.

## What was done

### Docker seam — `internal/lifecycle/docker.go`

Added `RestartCounts(ctx) (map[string]int, error)` to the `DockerDriver` interface and the production `cliDocker` impl, reusing the existing lifecycle Docker seam rather than opening a parallel client (issue #35: "do not add a parallel Docker client"). The impl lists managed containers (`docker ps -aq --filter label=malmo.managed=true`) and one `docker inspect --format '{{index .Config.Labels "malmo.instance_id"}} {{.RestartCount}}'` maps each container's cumulative `RestartCount` back to its owning `instance_id`. An instance with several containers takes the **max** across them — raise if any one container is crash-looping. The fake in `fakes_test.go` gained a matching `restartCounts` field + method.

### Detector — `cmd/brain/main.go`

`restartLoopDetector` samples `RestartCounts` on the health-poll cadence (`cfg.healthPollPeriod`, 60s) and reconciles the `container-restart-loop` issue per instance. It depends on a narrow consumer-side `restartCountReader` interface (satisfied by `lifecycle.DockerDriver`), so the test drives it with a fake. The brain constructs one shared `dock := lifecycle.NewCLIDocker()` used by both the lifecycle manager and the detector.

Reconcile per poll: for each instance, raise when the within-window restart delta exceeds the threshold; then clear any active `container-restart-loop` issue whose instance is **not** looping this poll — which covers both *stabilized* (still present, delta back under threshold) and *uninstalled* (absent from the count map entirely) apps. Transitions reuse the existing `emitHealthTransitions` (one audit record per raise/clear) and `emitHealthNotifications` helpers.

### Definition — `internal/health/health.go`

Registered the `container-restart-loop` definition: `CategoryVersion`, `warning`, Tier-2, blocks nothing. The app is already failing, so the issue surfaces it (Tier-2 action = view logs / stop the app) rather than gating writes/apps/users.

### Tests — `cmd/brain/main_test.go`, `internal/health/health_test.go`

`health`: `TestList_ContainerRestartLoopDefinition` pins the definition metadata (version/warning/Tier-2/blocks-nothing) and per-app keying. `cmd/brain`: nine detector tests driving `restartLoopDetector.check()` through a `fakeRestartReader` + hand-advanced `stepClock` — raise-keyed-to-instance, clear-on-stabilize, clear-on-absent (with history GC), no-false-raise-on-historical-count, strict-threshold (`>` not `>=`), per-instance isolation, container-recreation baseline reset, docker-error-leaves-state-unchanged, and no-bell-notification (staging).

## Open sub-decisions (recorded here per the issue)

The issue left two constants/approaches to "pick conservatively and document; the spec pins the shape, not the constants."

- **Threshold / window: more than 3 restarts in a 5-minute sliding window** (`restartLoopThreshold = 3`, `restartLoopWindow = 5 * time.Minute`). Matches the issue's worked example. The threshold is strict (`delta > N`), so exactly 3 restarts in the window does not trip. Tunable at first soak if noisy.
- **Poll, not events.** The socket-proxy allowlist (`CONTROL_PLANE.md`) grants `CONTAINERS=1` but **not** `EVENTS`, so `docker inspect → RestartCount` needs no proxy change; `docker events` would. We poll on the existing health cadence, which the issue prefers.
- **Cumulative counter → sliding-window delta.** `RestartCount` is cumulative since container creation, not per-window. The detector keeps per-instance `(time, count)` samples, prunes those older than the window, and thresholds `current − oldest-within-window`. Consequences: the **first** sample for an instance has delta 0 (no false raise from a high historical count, e.g. after a brain restart), and the window self-heals — a container that crashed a lot an hour ago but is quiet now reads as zero recent restarts and clears.
- **Container recreation resets the baseline.** An app update / reinstall recreates the container, resetting `RestartCount` to 0. A sampled count below the instance's last sample is treated as a fresh container: the window restarts from the new value, so a stale high baseline can't produce a negative/masking delta.

## How it maps to the specs

- `HEALTH.md` # Detector catalog: realizes the locus-D `container-restart-loop` row (now marked *(built)*), keyed per-app by `instance_id`, warning + Tier-2 + no block flags as the # Issue catalog row specifies.
- `HEALTH.md` # Cross-cutting detector policy: locus-D signals are **1-shot, no debounce**. The detector honours this — it raises on the first poll whose windowed delta crosses the threshold, with no "2 consecutive bad samples" requirement. (The sliding window is restart *accumulation*, not debounce: a single poll observing the condition is sufficient to raise.)
- `CONTROL_PLANE.md`: reads Docker through the socket-proxy's `CONTAINERS` family only; no `EVENTS` grant added.
- `APP_LIFECYCLE.md` / `internal/lifecycle/lifecycle.go`: maps loops to instances via the `malmo.instance_id` label the lifecycle layer already stamps on every managed container, alongside `restart: unless-stopped` (the premise that makes a crash *loop* rather than a single exit).
- CLAUDE.md # Go code discipline: consumer-side `restartCountReader` interface in `cmd/brain`; the new Docker method lives on the existing `lifecycle.DockerDriver` seam; `log/slog` with the standard `err` field on the docker-unreachable skip.

## Known gaps & deviations

- **No bell notification yet.** `container-restart-loop` is not on the `internal/notify` `healthRules` allowlist, so a raise surfaces in `GET /api/v1/health` (and the dashboard health surface) but emits no notification. This matches `NOTIFICATIONS.md`, which stages app-lifecycle notifications "as those sources are implemented" — wiring the allowlist entry (and choosing its action route / member-transparency stance) is a separate change. `TestRestartLoop_RaiseEmitsNoBellNotification` pins the current behaviour so the staging is intentional, not an oversight.
- **Multi-container max is integration-only.** The "max `RestartCount` across an instance's containers" logic lives in `cliDocker.RestartCounts`, which shells out to `docker` and so isn't exercised by the unit tests (they inject the already-reduced `map[string]int`). It's covered by code review + the inline comment, not a test, until the brain test pyramid grows a Docker-backed lane.
- **Sample history is in-memory.** The per-instance `(time, count)` window lives in the detector struct, not SQLite. A brain restart re-baselines every instance from its next poll (first sample → delta 0), so an in-progress loop takes up to one window to re-detect after a restart. Acceptable: the health *issue* itself is persisted by the manager, so an already-raised loop survives the restart; only the in-flight sample window is rebuilt.
- **No `app-unresponsive` overlap.** This detector reads Docker restart counts only; the manifest HTTP health-probe detector (`app-unresponsive`, locus D) is still deferred (needs a manifest field) and is unrelated.

## What's next

- **Notification allowlist entry** for `container-restart-loop` (`internal/notify` `healthRules`): pick the action route (likely the app's own page / logs) and whether members see a transparency notice, then drop the `TestRestartLoop_RaiseEmitsNoBellNotification` guard.
- **Other locus-D detectors** reuse this shape: `app-image-partial` (image pull reports incomplete) and `app-unresponsive` (manifest health-probe, once the manifest field lands).
- **Tune N/window at first real soak.** The constants are conservative guesses; production restart patterns may want a different threshold or window.
