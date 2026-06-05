# One-shot-job restart override (#92)

- **Status:** done
- **Date:** 2026-06-05
- **Specs touched:** `APP_LIFECYCLE.md`, `DECISIONS.md`

Closes the override bug the managed-services slice surfaced (`managed-services-postgres.md` left `kan`'s end-to-end boot blocked on it). The brain-generated `compose.override.yml` force-stamped `restart: unless-stopped` onto **every** service — catastrophic for services designed to terminate. A one-shot init/migrate/seed job that Docker restarts never reaches the "completed" terminal state, so a `depends_on: {condition: service_completed_successfully}` gate waiting on it blocks `docker compose up -d` forever. `kan` (`migrate` job → `web` waits on it) hung past a 600s test timeout. Two independent layers: layer 1 fixes the known shape (don't force-restart jobs), layer 2 is a containment backstop (bound `compose up`). Design rationale in `DECISIONS.md` 2026-06-05 (override forces restart except terminating jobs).

## What was done

### Layer 1 — don't force-restart terminating services (`internal/lifecycle/lifecycle.go`)
- `parseComposeServices` replaces the old names-only `composeServices`: it now extracts each author service's `restart:` and `depends_on` (handling both the short list form and the long map form — only the long form carries `condition:`).
- `completionGateTargets` collects every service named as the target of a `service_completed_successfully` gate.
- `isTerminatingJob` is the union of two signals: (a) the author set `restart: "no"` or `restart: "on-failure"` (prefix-matched, so `on-failure:5` counts), or (b) the service is a completion-gate target (catches an omitted `restart:`, whose Compose default is `no`).
- `writeOverride` now stamps `restart: unless-stopped` only when the service is **not** a terminating job, with one override: `main_service` is **always** forced long-running, so a paranoid or buggy author can't accidentally exempt the actual app. For a real job the `restart` key is omitted from the override entirely, so the author's `compose.yml` value wins verbatim.

### Layer 2 — bound `compose up -d` (`internal/lifecycle/lifecycle.go`)
- Install step 10 now runs `ComposeUp` under a context bounded by the existing health-wait budget (`m.healthWait`, default 120s) and cancels it immediately after. A pathological app whose completion gate never completes fails the install cleanly (full rollback) instead of wedging the brain indefinitely — independent of the layer-1 detection, so it also covers future one-off-job shapes not yet enumerated.

### Tests
- `lifecycle_jobrestart_test.go`: unit coverage of `parseComposeServices`, `completionGateTargets`, `isTerminatingJob`; end-to-end override generation asserting a `restart: "no"` job and a no-restart completion-gate target are both exempted, an ordinary service and a job-shaped `main_service` are both forced, and a hung `compose up` fails+rolls back within the health-wait budget (fake `composeUp` now receives the context so the bound is observable).
- `dockerlive_test.go`: `TestLiveKanBoot` un-skipped. Verified live — `kan` installs, the `migrate` job completes, `web` boots against managed Postgres, and the provisioned DB carries kan's tables (`--- PASS: TestLiveKanBoot (19.06s)`).

## What's next

- **Optional authoring-time lint** (parked, separate): `molma manifest check` could warn when `main_service` carries `restart: "no"`, or when a completion-gate target would have been force-restarted under the old rule — catching the mistake before publish rather than at install. Not built this slice.
